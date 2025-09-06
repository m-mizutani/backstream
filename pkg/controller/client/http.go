package client

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/m-mizutani/backstream/pkg/model"
	"github.com/m-mizutani/backstream/pkg/service/tunnel"
	"github.com/m-mizutani/backstream/pkg/utils/logging"
	"github.com/m-mizutani/goerr/v2"
)

type Option func(*Client)

type Client struct {
	svc            *tunnel.Service
	srcURL         string
	dstURL         string
	header         http.Header
	conn           *websocket.Conn
	connMu         sync.Mutex
	wsConnections  map[string]*LocalWebSocketConnection
	wsConnectionMu sync.RWMutex
}

func WithHeader(key, value string) Option {
	return func(x *Client) {
		x.header.Add(key, value)
	}
}

func New(svc *tunnel.Service, src string, dst string, opts ...Option) *Client {
	x := &Client{
		svc:           svc,
		srcURL:        src,
		dstURL:        dst,
		header:        http.Header{},
		wsConnections: make(map[string]*LocalWebSocketConnection),
	}
	for _, opt := range opts {
		opt(x)
	}
	return x
}

func (x *Client) Connect(ctx context.Context) error {
	logger := logging.Extract(ctx)

	wsURL, err := convertToWebSocketURL(x.srcURL)
	if err != nil {
		return goerr.Wrap(err, "failed to convert URL")
	}

	headers := x.header.Clone()
	headers.Add("Backstream-Client", "default")

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, headers)
	if err != nil {
		return goerr.Wrap(err, "failed to connect")
	}
	defer conn.Close()

	x.connMu.Lock()
	x.conn = conn
	x.connMu.Unlock()

	logger.Info("connected to server", "url", wsURL)

	// WebSocket message handling will be done through the connection directly

	errCh := make(chan error)

	// Handle incoming messages from server
	go func() {
		defer close(errCh)

		for {
			logger.Info("waiting for message")
			_, message, err := conn.ReadMessage()
			if err != nil {
				logging.Default().Error("failed to read message", "error", err)
				return
			}

			// Try to unmarshal as WebSocket message first
			var wsMsg model.WebSocketMessage
			if err := json.Unmarshal(message, &wsMsg); err == nil && wsMsg.Type != "" {
				// Handle WebSocket message (only if Type is not empty)
				x.handleWebSocketMessage(ctx, &wsMsg)
				continue
			}

			// Otherwise, handle as regular request
			// Debug: log raw message to see what we received
			logger.Info("received raw message from server", "message", string(message))
			
			var req model.Request
			if err := json.Unmarshal(message, &req); err != nil {
				errCh <- goerr.Wrap(err, "failed to unmarshal message")
				return
			}
			// Parse path and query string for logging
			parsedPath, query := parsePathAndQuery(req.Path)

			// Log at Info level with path and query string separated
			logger.Info("received request from server",
				"id", req.ID,
				"method", req.Method,
				"path", parsedPath,
				"query", query,
				"raw_path", req.Path)

			logger.Debug("request details", slog.Group("request",
				slog.Any("id", req.ID),
				slog.Any("path", parsedPath),
				slog.Any("query", query),
				slog.Any("method", req.Method),
				slog.Any("header", req.Header),
				slog.Any("body", string(req.Body)),
			))

			resp, err := x.svc.ToLocal(ctx, &req)
			if err != nil {
				errCh <- goerr.Wrap(err, "failed to handle local request")
				return
			}

			respBody, err := json.Marshal(resp)
			if err != nil {
				errCh <- goerr.Wrap(err, "failed to marshal response")
				return
			}

			// Log response with path and query string separated
			logger.Info("sending response to server",
				"id", resp.ID,
				"code", resp.Code,
				"path", parsedPath,
				"query", query,
				"method", req.Method)
			if err := conn.WriteMessage(websocket.TextMessage, respBody); err != nil {
				errCh <- goerr.Wrap(err, "failed to write response")
				return
			}
		}
	}()

	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)

	select {
	case <-interrupt:
		logger.Info("Quit signal received")

	case err := <-errCh:
		if err != nil {
			return goerr.Wrap(err, "failed to read message")
		}
	}

	return nil

	/*
		if err := conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "")); err != nil {
			return goerr.Wrap(err, "failed to write close message")
		}
		<-done
	*/
}

func convertToWebSocketURL(rawURL string) (string, error) {
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return "", goerr.Wrap(err, "failed to parse URL")
	}

	switch parsedURL.Scheme {
	case "http":
		parsedURL.Scheme = "ws"
	case "https":
		parsedURL.Scheme = "wss"
	default:
		return "", goerr.New("unsupported scheme", goerr.V("scheme", parsedURL.Scheme))
	}

	return parsedURL.String(), nil
}

// parsePathAndQuery splits a path string into path and query components
func parsePathAndQuery(fullPath string) (path string, query string) {
	if idx := strings.Index(fullPath, "?"); idx != -1 {
		return fullPath[:idx], fullPath[idx+1:]
	}
	return fullPath, ""
}

// handleWebSocketMessage handles WebSocket messages from the server
func (x *Client) handleWebSocketMessage(ctx context.Context, wsMsg *model.WebSocketMessage) {
	logger := logging.Extract(ctx)

	switch wsMsg.Type {
	case model.MessageTypeWebSocketUpgradeRequest:
		var req model.WebSocketUpgradeRequest
		if err := json.Unmarshal(wsMsg.Data, &req); err != nil {
			logger.Error("Failed to unmarshal WebSocket upgrade request", "error", err)
			return
		}
		x.handleWebSocketUpgradeRequest(ctx, &req)

	case model.MessageTypeWebSocketFrame:
		var frame model.WebSocketFrame
		if err := json.Unmarshal(wsMsg.Data, &frame); err != nil {
			logger.Error("Failed to unmarshal WebSocket frame", "error", err)
			return
		}
		x.handleWebSocketFrame(&frame)

	case model.MessageTypeWebSocketClose:
		var close model.WebSocketClose
		if err := json.Unmarshal(wsMsg.Data, &close); err != nil {
			logger.Error("Failed to unmarshal WebSocket close", "error", err)
			return
		}
		x.handleWebSocketClose(&close)

	default:
		logger.Warn("Unknown WebSocket message type", "type", wsMsg.Type)
	}
}
