package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/m-mizutani/backstream/pkg/model"
	"github.com/m-mizutani/backstream/pkg/utils/logging"
	"github.com/m-mizutani/goerr/v2"
)

// LocalWebSocketConnection represents a connection to local WebSocket endpoint
type LocalWebSocketConnection struct {
	ID   string
	Conn *websocket.Conn
	mu   sync.Mutex
}

// Send sends a message through the local WebSocket connection
func (lc *LocalWebSocketConnection) Send(messageType int, data []byte) error {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	return lc.Conn.WriteMessage(messageType, data)
}

// Close closes the local WebSocket connection
func (lc *LocalWebSocketConnection) Close() error {
	return lc.Conn.Close()
}

// handleWebSocketUpgradeRequest handles WebSocket upgrade request from server
func (x *Client) handleWebSocketUpgradeRequest(ctx context.Context, req *model.WebSocketUpgradeRequest) {
	logger := logging.Extract(ctx)
	logger.Info("Received WebSocket upgrade request", 
		"id", req.ID, 
		"path", req.Path,
		"dstURL", x.dstURL,
		"headers", req.Header)

	// Attempt to connect to local WebSocket endpoint
	localConn, err := x.connectToLocalWebSocket(ctx, req)

	var resp *model.WebSocketUpgradeResponse
	if err != nil {
		logger.Error("Failed to connect to local WebSocket", "error", err)
		resp = model.NewWebSocketUpgradeResponse(req.ID, false, nil, err.Error())
	} else {
		// Store the connection
		x.addLocalWebSocketConnection(req.ID, localConn)

		// Start relay goroutines
		go x.relayLocalToServer(ctx, localConn)
		go x.relayServerToLocal(ctx, localConn)

		resp = model.NewWebSocketUpgradeResponse(req.ID, true, nil, "")
		logger.Info("Local WebSocket connection established", "id", req.ID)
	}

	// Send response back to server
	if err := x.sendWebSocketMessage(model.MessageTypeWebSocketUpgradeResponse, resp); err != nil {
		logger.Error("Failed to send WebSocket upgrade response", "error", err)
	}
}

// connectToLocalWebSocket connects to the local WebSocket endpoint
func (x *Client) connectToLocalWebSocket(ctx context.Context, req *model.WebSocketUpgradeRequest) (*LocalWebSocketConnection, error) {
	// Parse destination URL
	dstURL, err := url.Parse(x.dstURL)
	if err != nil {
		return nil, goerr.Wrap(err, "failed to parse destination URL")
	}

	// Convert HTTP to WebSocket scheme
	switch dstURL.Scheme {
	case "http":
		dstURL.Scheme = "ws"
	case "https":
		dstURL.Scheme = "wss"
	default:
		// Already WebSocket scheme or unsupported
		if dstURL.Scheme != "ws" && dstURL.Scheme != "wss" {
			return nil, goerr.New("unsupported scheme", goerr.V("scheme", dstURL.Scheme))
		}
	}

	// Parse the path which may include query parameters
	if req.Path != "" {
		// Parse the path+query from the request
		parsedPath, err := url.Parse(req.Path)
		if err != nil {
			return nil, goerr.Wrap(err, "failed to parse request path", goerr.V("path", req.Path))
		}

		// Set path and query separately
		dstURL.Path = parsedPath.Path
		dstURL.RawQuery = parsedPath.RawQuery
	}

	// Prepare headers - forward most headers from the original request
	header := make(http.Header)
	var subprotocols []string
	
	for k, v := range req.Header {
		lowerKey := strings.ToLower(k)
		switch lowerKey {
		case "upgrade", "connection", "sec-websocket-version", "sec-websocket-key":
			// These are mandatory WebSocket headers that gorilla sets
			continue
		case "sec-websocket-protocol":
			// Extract subprotocols for the dialer
			if v != "" {
				subprotocols = strings.Split(v, ",")
				for i := range subprotocols {
					subprotocols[i] = strings.TrimSpace(subprotocols[i])
				}
			}
			continue
		case "sec-websocket-extensions":
			// Skip extensions - gorilla will handle this
			continue
		default:
			// Forward all other headers
			header.Set(k, v)
		}
	}

	// Connect to local WebSocket
	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
		Subprotocols:      subprotocols,
	}

	logger := logging.Extract(ctx)
	logger.Info("Connecting to local WebSocket", 
		"url", dstURL.String(),
		"subprotocols", subprotocols,
		"headers", header)
	
	conn, resp, err := dialer.DialContext(ctx, dstURL.String(), header)
	if err != nil {
		if resp != nil {
			logger.Error("WebSocket dial failed with response", "status", resp.StatusCode, "url", dstURL.String())
		}
		return nil, goerr.Wrap(err, "failed to dial local WebSocket", goerr.V("url", dstURL.String()))
	}

	return &LocalWebSocketConnection{
		ID:   req.ID,
		Conn: conn,
	}, nil
}

// relayLocalToServer relays messages from local WebSocket to server
func (x *Client) relayLocalToServer(ctx context.Context, localConn *LocalWebSocketConnection) {
	logger := logging.Extract(ctx)
	defer x.removeLocalWebSocketConnection(localConn.ID)
	defer localConn.Close()

	for {
		messageType, data, err := localConn.Conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				logger.Error("Local WebSocket read error", "error", err)
			}

			// Send close notification
			closeMsg := model.NewWebSocketClose(localConn.ID, websocket.CloseAbnormalClosure, err.Error())
			if err := x.sendWebSocketMessage(model.MessageTypeWebSocketClose, closeMsg); err != nil {
				logger.Error("Failed to send close notification", "error", err)
			}
			return
		}

		// Forward frame to server
		frame := model.NewWebSocketFrame(localConn.ID, messageType, data)
		if err := x.sendWebSocketMessage(model.MessageTypeWebSocketFrame, frame); err != nil {
			logger.Error("Failed to forward frame to server", "error", err)
			return
		}
	}
}

// relayServerToLocal relays messages from server to local WebSocket
func (x *Client) relayServerToLocal(ctx context.Context, localConn *LocalWebSocketConnection) {
	logger := logging.Extract(ctx)

	// This function waits for frames from the server
	// The actual frame handling is done in handleWebSocketFrame
	logger.Debug("Starting server to local relay", "id", localConn.ID)

	// Keep the goroutine alive
	<-ctx.Done()
}

// handleWebSocketFrame handles WebSocket frame from server
func (x *Client) handleWebSocketFrame(frame *model.WebSocketFrame) {
	conn, ok := x.getLocalWebSocketConnection(frame.ConnectionID)
	if !ok {
		return
	}

	if err := conn.Send(frame.Type, frame.Data); err != nil {
		logger := logging.Default()
		logger.Error("Failed to send frame to local WebSocket", "error", err)
	}
}

// handleWebSocketClose handles WebSocket close from server
func (x *Client) handleWebSocketClose(close *model.WebSocketClose) {
	conn, ok := x.getLocalWebSocketConnection(close.ConnectionID)
	if !ok {
		return
	}

	if err := conn.Close(); err != nil {
		logger := logging.Default()
		logger.Error("Failed to close local WebSocket", "error", err)
	}
	x.removeLocalWebSocketConnection(close.ConnectionID)
}

// sendWebSocketMessage sends a WebSocket message to the server
func (x *Client) sendWebSocketMessage(msgType string, data interface{}) error {
	wsMsg, err := model.NewWebSocketMessage(msgType, data)
	if err != nil {
		return goerr.Wrap(err, "failed to create WebSocket message")
	}

	msgData, err := json.Marshal(wsMsg)
	if err != nil {
		return goerr.Wrap(err, "failed to marshal WebSocket message")
	}

	x.connMu.Lock()
	defer x.connMu.Unlock()

	if x.conn == nil {
		return goerr.New("connection not established")
	}

	return x.conn.WriteMessage(websocket.TextMessage, msgData)
}

// addLocalWebSocketConnection adds a local WebSocket connection to the map
func (x *Client) addLocalWebSocketConnection(id string, conn *LocalWebSocketConnection) {
	x.wsConnectionMu.Lock()
	defer x.wsConnectionMu.Unlock()
	x.wsConnections[id] = conn
}

// removeLocalWebSocketConnection removes a local WebSocket connection from the map
func (x *Client) removeLocalWebSocketConnection(id string) {
	x.wsConnectionMu.Lock()
	defer x.wsConnectionMu.Unlock()
	delete(x.wsConnections, id)
}

// getLocalWebSocketConnection gets a local WebSocket connection by ID
func (x *Client) getLocalWebSocketConnection(id string) (*LocalWebSocketConnection, bool) {
	x.wsConnectionMu.RLock()
	defer x.wsConnectionMu.RUnlock()
	conn, ok := x.wsConnections[id]
	return conn, ok
}
