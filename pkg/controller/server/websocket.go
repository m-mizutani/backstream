package server

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/m-mizutani/backstream/pkg/model"
	"github.com/m-mizutani/backstream/pkg/utils/logging"
	"github.com/m-mizutani/goerr/v2"
)

// WebSocketConnection represents a WebSocket connection
type WebSocketConnection struct {
	ID     string
	Conn   *websocket.Conn
	mu     sync.Mutex
	cancel context.CancelFunc
}

// Send sends a message through the WebSocket connection
func (wc *WebSocketConnection) Send(messageType int, data []byte) error {
	wc.mu.Lock()
	defer wc.mu.Unlock()
	return wc.Conn.WriteMessage(messageType, data)
}

// Close closes the WebSocket connection
func (wc *WebSocketConnection) Close() error {
	if wc.cancel != nil {
		wc.cancel()
	}
	return wc.Conn.Close()
}

// isWebSocketUpgrade checks if the request is a WebSocket upgrade request
func isWebSocketUpgrade(r *http.Request) bool {
	connection := r.Header.Get("Connection")
	upgrade := r.Header.Get("Upgrade")

	// Check for "upgrade" in Connection header (can be part of a list)
	hasUpgrade := false
	for _, v := range strings.Split(strings.ToLower(connection), ",") {
		if strings.TrimSpace(v) == "upgrade" {
			hasUpgrade = true
			break
		}
	}

	return hasUpgrade && strings.ToLower(upgrade) == "websocket"
}

// handleUserWebSocket handles WebSocket connections from end users
func (x *Server) handleUserWebSocket(w http.ResponseWriter, r *http.Request) {
	logger := logging.Extract(r.Context())
	logger.Info("Received WebSocket upgrade request", 
		"path", r.URL.Path,
		"rawQuery", r.URL.RawQuery,
		"headers", r.Header)

	// Check auth policy for WebSocket connections
	if x.policy != nil {
		if err := checkAuthPolicy(r.Context(), x.policy, r, "data.auth.server"); err != nil {
			logger.Error("WebSocket auth policy failed", "error", err)
			http.Error(w, "auth policy denied", http.StatusForbidden)
			return
		}
	}

	// Create WebSocket upgrade request
	header := make(map[string]string)
	for k, v := range r.Header {
		if len(v) > 0 {
			header[k] = v[0]
		}
	}

	// Include query string in the path for WebSocket upgrade
	path := r.URL.Path
	if r.URL.RawQuery != "" {
		path = path + "?" + r.URL.RawQuery
	}
	upgradeReq := model.NewWebSocketUpgradeRequest(path, header, r.RemoteAddr)
	logger.Debug("Sending WebSocket upgrade request to client", "id", upgradeReq.ID)

	// Send upgrade request to client and wait for response
	wsMsg, err := model.NewWebSocketMessage(model.MessageTypeWebSocketUpgradeRequest, upgradeReq)
	if err != nil {
		logger.Error("Failed to create WebSocket message", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	msgData, err := json.Marshal(wsMsg)
	if err != nil {
		logger.Error("Failed to marshal WebSocket upgrade request", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Send to hub and wait for response
	respChan := make(chan *model.WebSocketUpgradeResponse, 1)
	x.registerUpgradeRequest(upgradeReq.ID, respChan)
	defer x.unregisterUpgradeRequest(upgradeReq.ID)

	if err := x.svc.Broadcast(msgData); err != nil {
		logger.Error("Failed to broadcast upgrade request", "error", err)
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
		return
	}

	// Wait for response with timeout
	ctx, cancel := context.WithTimeout(r.Context(), upgradeTimeout)
	defer cancel()

	select {
	case resp := <-respChan:
		if !resp.Accepted {
			logger.Error("WebSocket upgrade rejected by client", "error", resp.Error)
			http.Error(w, "upgrade rejected", http.StatusBadGateway)
			return
		}

		// Prepare response headers from client
		var responseHeader http.Header
		if resp.Header != nil {
			responseHeader = http.Header(resp.Header)
		}
		
		// Create a temporary connection entry before upgrade to avoid race condition
		wsCtx, wsCancel := context.WithCancel(context.Background())
		tempConn := &WebSocketConnection{
			ID:     upgradeReq.ID,
			Conn:   nil, // Will be set after upgrade
			cancel: wsCancel,
		}
		x.addWebSocketConnection(upgradeReq.ID, tempConn)
		
		// Upgrade the connection with response headers
		conn, err := x.upgrade(w, r, responseHeader)
		if err != nil {
			logger.Error("Failed to upgrade WebSocket", "error", err)
			x.removeWebSocketConnection(upgradeReq.ID) // Clean up on failure
			return
		}

		// Update the connection with the actual WebSocket
		tempConn.Conn = conn

		logger.Info("WebSocket connection established", "id", upgradeReq.ID)

		// Start relaying messages from the user to the backstream client
		// Run synchronously to keep the HTTP handler alive until WebSocket is closed
		x.relayUserToClient(wsCtx, tempConn)

	case <-ctx.Done():
		logger.Error("WebSocket upgrade timeout")
		http.Error(w, "upgrade timeout", http.StatusGatewayTimeout)
		return
	}
}

// relayUserToClient relays messages from user to client
func (x *Server) relayUserToClient(ctx context.Context, wsConn *WebSocketConnection) {
	logger := logging.Extract(ctx)
	defer wsConn.Close()
	defer x.removeWebSocketConnection(wsConn.ID)

	logger.Debug("Starting user to client relay", "id", wsConn.ID)

	// Add panic recovery to handle gorilla/websocket panics
	defer func() {
		if r := recover(); r != nil {
			logger.Error("Panic in relayUserToClient", "error", r, "id", wsConn.ID)
		}
	}()

	for {
		select {
		case <-ctx.Done():
			logger.Debug("WebSocket context cancelled, closing relay", "id", wsConn.ID)
			return
		default:
			// Set read deadline to avoid blocking forever
			wsConn.Conn.SetReadDeadline(time.Now().Add(1 * time.Second))
			messageType, data, err := wsConn.Conn.ReadMessage()
			
			// Check if this is a timeout error due to context cancellation
			if err != nil {
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					// Check if context is done - if so, this is expected
					select {
					case <-ctx.Done():
						logger.Debug("WebSocket read timeout due to context cancellation", "id", wsConn.ID)
						return
					default:
						// Real timeout, continue
						continue
					}
				}
				
				// Any read error should terminate the relay to avoid panic
				if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
					logger.Error("WebSocket read error", "error", err)
				} else {
					logger.Debug("User WebSocket closed normally", "error", err)
				}

				// Send close notification only for real errors
				closeMsg := model.NewWebSocketClose(wsConn.ID, websocket.CloseAbnormalClosure, err.Error())
				if err := x.broadcastWebSocketMessage(model.MessageTypeWebSocketClose, closeMsg); err != nil {
					logger.Error("Failed to broadcast close notification", "error", err)
				}
				return
			}
			
			// Clear read deadline for successful read
			wsConn.Conn.SetReadDeadline(time.Time{})

			logger.Debug("Received frame from user WebSocket", 
				"id", wsConn.ID, 
				"type", messageType, 
				"size", len(data),
				"data", string(data))

			// Forward frame to client
			frame := model.NewWebSocketFrame(wsConn.ID, messageType, data)
			if err := x.broadcastWebSocketMessage(model.MessageTypeWebSocketFrame, frame); err != nil {
				logger.Error("Failed to forward frame to client", "error", err)
				return
			}
			logger.Debug("Forwarded frame to client", "id", wsConn.ID)
		}
	}
}

// broadcastWebSocketMessage broadcasts a WebSocket message through the hub
func (x *Server) broadcastWebSocketMessage(msgType string, data interface{}) error {
	wsMsg, err := model.NewWebSocketMessage(msgType, data)
	if err != nil {
		return goerr.Wrap(err, "failed to create WebSocket message")
	}

	msgData, err := json.Marshal(wsMsg)
	if err != nil {
		return goerr.Wrap(err, "failed to marshal WebSocket message")
	}

	return x.svc.Broadcast(msgData)
}

// addWebSocketConnection adds a WebSocket connection to the map
func (x *Server) addWebSocketConnection(id string, conn *WebSocketConnection) {
	x.wsConnectionMu.Lock()
	defer x.wsConnectionMu.Unlock()
	x.wsConnections[id] = conn
}

// removeWebSocketConnection removes a WebSocket connection from the map
func (x *Server) removeWebSocketConnection(id string) {
	x.wsConnectionMu.Lock()
	defer x.wsConnectionMu.Unlock()
	delete(x.wsConnections, id)
}

// getWebSocketConnection gets a WebSocket connection by ID
func (x *Server) getWebSocketConnection(id string) (*WebSocketConnection, bool) {
	x.wsConnectionMu.RLock()
	defer x.wsConnectionMu.RUnlock()
	conn, ok := x.wsConnections[id]
	return conn, ok
}

// registerUpgradeRequest registers an upgrade request waiting for response
func (x *Server) registerUpgradeRequest(id string, ch chan *model.WebSocketUpgradeResponse) {
	x.upgradeRequestMu.Lock()
	defer x.upgradeRequestMu.Unlock()
	x.upgradeRequests[id] = ch
}

// unregisterUpgradeRequest unregisters an upgrade request
func (x *Server) unregisterUpgradeRequest(id string) {
	x.upgradeRequestMu.Lock()
	defer x.upgradeRequestMu.Unlock()
	delete(x.upgradeRequests, id)
}

// handleWebSocketUpgradeResponse handles WebSocket upgrade response from client
func (x *Server) handleWebSocketUpgradeResponse(resp *model.WebSocketUpgradeResponse) {
	x.upgradeRequestMu.RLock()
	ch, ok := x.upgradeRequests[resp.ID]
	x.upgradeRequestMu.RUnlock()

	if ok {
		select {
		case ch <- resp:
		default:
		}
	}
}

// handleWebSocketFrame handles WebSocket frame from client
func (x *Server) handleWebSocketFrame(frame *model.WebSocketFrame) {
	logger := logging.Default()
	logger.Info("Handling WebSocket frame from client", 
		"id", frame.ConnectionID, 
		"type", frame.Type, 
		"size", len(frame.Data),
		"data", string(frame.Data))

	// Debug: Log all available connections
	x.wsConnectionMu.RLock()
	connIDs := make([]string, 0, len(x.wsConnections))
	for id := range x.wsConnections {
		connIDs = append(connIDs, id)
	}
	x.wsConnectionMu.RUnlock()
	logger.Info("Available WebSocket connections", "connectionIDs", connIDs, "lookingFor", frame.ConnectionID)

	conn, ok := x.getWebSocketConnection(frame.ConnectionID)
	if !ok {
		logger.Error("No user WebSocket connection found", 
			"id", frame.ConnectionID, 
			"availableConnections", connIDs)
		return
	}

	// Check if connection is still being established
	if conn.Conn == nil {
		logger.Warn("WebSocket connection not yet fully established, dropping frame", "id", frame.ConnectionID)
		return
	}

	logger.Info("Found WebSocket connection, sending frame", "id", frame.ConnectionID)
	if err := conn.Send(frame.Type, frame.Data); err != nil {
		logger.Error("Failed to send frame to user WebSocket", "error", err)
	} else {
		logger.Info("Successfully sent frame to user WebSocket", "id", frame.ConnectionID)
	}
}

// handleWebSocketClose handles WebSocket close from client
func (x *Server) handleWebSocketClose(close *model.WebSocketClose) {
	conn, ok := x.getWebSocketConnection(close.ConnectionID)
	if !ok {
		return
	}

	conn.Close()
	x.removeWebSocketConnection(close.ConnectionID)
}
