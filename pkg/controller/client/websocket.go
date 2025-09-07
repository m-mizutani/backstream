package client

import (
	"context"
	"encoding/json"
	"net"
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
	ID     string
	Conn   *websocket.Conn
	mu     sync.Mutex
	cancel context.CancelFunc
}

// Send sends a message through the local WebSocket connection
func (lc *LocalWebSocketConnection) Send(messageType int, data []byte) error {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	return lc.Conn.WriteMessage(messageType, data)
}

// Close closes the local WebSocket connection
func (lc *LocalWebSocketConnection) Close() error {
	if lc.cancel != nil {
		lc.cancel()
	}
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
	localConn, respHeaders, err := x.connectToLocalWebSocket(ctx, req)

	var resp *model.WebSocketUpgradeResponse
	if err != nil {
		logger.Error("Failed to connect to local WebSocket", "error", err)
		resp = model.NewWebSocketUpgradeResponse(req.ID, false, nil, err.Error())
	} else {
		// Create a new context for WebSocket lifetime management
		wsCtx, wsCancel := context.WithCancel(context.Background())
		localConn.cancel = wsCancel

		// Store the connection
		x.addLocalWebSocketConnection(req.ID, localConn)

		// Start relay goroutines
		go x.relayLocalToServer(wsCtx, localConn)
		go x.relayServerToLocal(wsCtx, localConn)

		// Include response headers from local WebSocket in the response
		resp = model.NewWebSocketUpgradeResponse(req.ID, true, respHeaders, "")
		logger.Info("Local WebSocket connection established", "id", req.ID, "responseHeaders", respHeaders)
	}

	// Send response back to server
	if err := x.sendWebSocketMessage(model.MessageTypeWebSocketUpgradeResponse, resp); err != nil {
		logger.Error("Failed to send WebSocket upgrade response", "error", err)
	}
}

// connectToLocalWebSocket connects to the local WebSocket endpoint
// Returns the connection and response headers
func (x *Client) connectToLocalWebSocket(ctx context.Context, req *model.WebSocketUpgradeRequest) (*LocalWebSocketConnection, http.Header, error) {
	// Parse destination URL
	dstURL, err := url.Parse(x.dstURL)
	if err != nil {
		return nil, nil, goerr.Wrap(err, "failed to parse destination URL")
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
			return nil, nil, goerr.New("unsupported scheme", goerr.V("scheme", dstURL.Scheme))
		}
	}

	// Parse the path which may include query parameters
	if req.Path != "" {
		// Parse the path+query from the request
		parsedPath, err := url.Parse(req.Path)
		if err != nil {
			return nil, nil, goerr.Wrap(err, "failed to parse request path", goerr.V("path", req.Path))
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
		return nil, nil, goerr.Wrap(err, "failed to dial local WebSocket", goerr.V("url", dstURL.String()))
	}

	// Extract response headers for forwarding
	var responseHeaders http.Header
	if resp != nil {
		responseHeaders = resp.Header
	}

	return &LocalWebSocketConnection{
		ID:     req.ID,
		Conn:   conn,
		cancel: nil, // Will be set by caller
	}, responseHeaders, nil
}

// relayLocalToServer relays messages from local WebSocket to server
func (x *Client) relayLocalToServer(ctx context.Context, localConn *LocalWebSocketConnection) {
	logger := logging.Extract(ctx)
	defer x.removeLocalWebSocketConnection(localConn.ID)
	defer localConn.Close()

	logger.Info("Starting local to server relay", "id", localConn.ID)

	for {
		select {
		case <-ctx.Done():
			logger.Debug("Local WebSocket context cancelled, closing relay", "id", localConn.ID)
			return
		default:
			// Set read deadline to avoid blocking forever
			localConn.Conn.SetReadDeadline(time.Now().Add(1 * time.Second))
			messageType, data, err := localConn.Conn.ReadMessage()
			
			// Check if this is a timeout error due to context cancellation
			if err != nil {
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					// Check if context is done - if so, this is expected
					select {
					case <-ctx.Done():
						logger.Debug("Local WebSocket read timeout due to context cancellation", "id", localConn.ID)
						return
					default:
						// Real timeout, continue
						continue
					}
				}
				
				// Real error occurred
				if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
					logger.Error("Local WebSocket read error", "error", err)
				} else {
					logger.Debug("Local WebSocket closed normally", "error", err)
				}

				// Send close notification only for real errors
				closeMsg := model.NewWebSocketClose(localConn.ID, websocket.CloseAbnormalClosure, err.Error())
				if err := x.sendWebSocketMessage(model.MessageTypeWebSocketClose, closeMsg); err != nil {
					logger.Error("Failed to send close notification", "error", err)
				}
				return
			}
			
			// Clear read deadline for successful read
			localConn.Conn.SetReadDeadline(time.Time{})

			logger.Info("Received frame from local WebSocket", 
				"id", localConn.ID, 
				"type", messageType, 
				"size", len(data),
				"data", string(data))

			// Forward frame to server
			frame := model.NewWebSocketFrame(localConn.ID, messageType, data)
			if err := x.sendWebSocketMessage(model.MessageTypeWebSocketFrame, frame); err != nil {
				logger.Error("Failed to forward frame to server", "error", err)
				return
			}
			logger.Info("Forwarded frame to server", "id", localConn.ID)
		}
	}
}

// relayServerToLocal relays messages from server to local WebSocket
func (x *Client) relayServerToLocal(ctx context.Context, localConn *LocalWebSocketConnection) {
	logger := logging.Extract(ctx)

	// This function waits for frames from the server
	// The actual frame handling is done in handleWebSocketFrame
	logger.Info("Starting server to local relay", "id", localConn.ID)

	// Keep the goroutine alive
	<-ctx.Done()
}

// handleWebSocketFrame handles WebSocket frame from server
func (x *Client) handleWebSocketFrame(frame *model.WebSocketFrame) {
	logger := logging.Default()
	logger.Info("Handling WebSocket frame from server", 
		"id", frame.ConnectionID, 
		"type", frame.Type, 
		"size", len(frame.Data),
		"data", string(frame.Data))

	conn, ok := x.getLocalWebSocketConnection(frame.ConnectionID)
	if !ok {
		logger.Warn("No local WebSocket connection found", "id", frame.ConnectionID)
		return
	}

	if err := conn.Send(frame.Type, frame.Data); err != nil {
		logger.Error("Failed to send frame to local WebSocket", "error", err)
	} else {
		logger.Info("Sent frame to local WebSocket", "id", frame.ConnectionID)
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
