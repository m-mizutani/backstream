package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/m-mizutani/backstream/pkg/model"
	"github.com/m-mizutani/backstream/pkg/utils/logging"
	"github.com/m-mizutani/goerr/v2"
)

// WebSocketConnection represents a WebSocket connection
type WebSocketConnection struct {
	ID   string
	Conn *websocket.Conn
	mu   sync.Mutex
}

// Send sends a message through the WebSocket connection
func (wc *WebSocketConnection) Send(messageType int, data []byte) error {
	wc.mu.Lock()
	defer wc.mu.Unlock()
	return wc.Conn.WriteMessage(messageType, data)
}

// Close closes the WebSocket connection
func (wc *WebSocketConnection) Close() error {
	return wc.Conn.Close()
}

// isWebSocketUpgrade checks if the request is a WebSocket upgrade request
func isWebSocketUpgrade(r *http.Request) bool {
	connection := r.Header.Get("Connection")
	upgrade := r.Header.Get("Upgrade")

	return strings.ToLower(connection) == "upgrade" &&
		strings.ToLower(upgrade) == "websocket"
}

// handleUserWebSocket handles WebSocket connections from end users
func (x *Server) handleUserWebSocket(w http.ResponseWriter, r *http.Request) {
	logger := logging.Extract(r.Context())

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

	upgradeReq := model.NewWebSocketUpgradeRequest(r.URL.Path, header, r.RemoteAddr)
	logger.Debug("Sending WebSocket upgrade request to client", "id", upgradeReq.ID)

	// Send upgrade request to client and wait for response
	wsMsg := model.NewWebSocketMessage(model.MessageTypeWebSocketUpgradeRequest, upgradeReq)
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

		// Upgrade the connection
		conn, err := x.upgrade(w, r, nil)
		if err != nil {
			logger.Error("Failed to upgrade WebSocket", "error", err)
			return
		}

		// Store the connection
		wsConn := &WebSocketConnection{
			ID:   upgradeReq.ID,
			Conn: conn,
		}
		x.addWebSocketConnection(upgradeReq.ID, wsConn)
		defer x.removeWebSocketConnection(upgradeReq.ID)

		logger.Info("WebSocket connection established", "id", upgradeReq.ID)

		// Start relay goroutines
		go x.relayUserToClient(r.Context(), wsConn)
		x.relayClientToUser(r.Context(), wsConn)

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

	for {
		messageType, data, err := wsConn.Conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				logger.Error("WebSocket read error", "error", err)
			}

			// Send close notification
			closeMsg := model.NewWebSocketClose(wsConn.ID, websocket.CloseAbnormalClosure, err.Error())
			if err := x.broadcastWebSocketMessage(model.MessageTypeWebSocketClose, closeMsg); err != nil {
				logger.Error("Failed to broadcast close notification", "error", err)
			}
			return
		}

		// Forward frame to client
		frame := model.NewWebSocketFrame(wsConn.ID, messageType, data)
		if err := x.broadcastWebSocketMessage(model.MessageTypeWebSocketFrame, frame); err != nil {
			logger.Error("Failed to forward frame to client", "error", err)
			return
		}
	}
}

// relayClientToUser relays messages from client to user
func (x *Server) relayClientToUser(ctx context.Context, wsConn *WebSocketConnection) {
	logger := logging.Extract(ctx)
	logger.Debug("Starting client to user relay", "id", wsConn.ID)

	// Join WebSocket message channel
	wsMsgCh := x.svc.JoinWebSocket(wsConn.ID)
	defer x.svc.LeaveWebSocket(wsConn.ID)

	for {
		select {
		case msg := <-wsMsgCh:
			// Parse the message to determine type
			var wsMsg model.WebSocketMessage
			if err := json.Unmarshal(msg, &wsMsg); err != nil {
				logger.Error("Failed to unmarshal WebSocket message", "error", err)
				continue
			}

			switch wsMsg.Type {
			case model.MessageTypeWebSocketFrame:
				// Handle frame from client
				data, _ := json.Marshal(wsMsg.Data)
				var frame model.WebSocketFrame
				if err := json.Unmarshal(data, &frame); err != nil {
					logger.Error("Failed to unmarshal frame", "error", err)
					continue
				}

				// Forward to user if it's for this connection
				if frame.ConnectionID == wsConn.ID {
					if err := wsConn.Send(frame.Type, frame.Data); err != nil {
						logger.Error("Failed to send frame to user", "error", err)
						return
					}
				}

			case model.MessageTypeWebSocketClose:
				// Handle close from client
				data, _ := json.Marshal(wsMsg.Data)
				var close model.WebSocketClose
				if err := json.Unmarshal(data, &close); err != nil {
					logger.Error("Failed to unmarshal close", "error", err)
					continue
				}

				if close.ConnectionID == wsConn.ID {
					logger.Info("Closing WebSocket connection", "id", wsConn.ID)
					return
				}
			}

		case <-ctx.Done():
			return
		}
	}
}

// broadcastWebSocketMessage broadcasts a WebSocket message through the hub
func (x *Server) broadcastWebSocketMessage(msgType string, data interface{}) error {
	wsMsg := model.NewWebSocketMessage(msgType, data)
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
	conn, ok := x.getWebSocketConnection(frame.ConnectionID)
	if !ok {
		return
	}

	if err := conn.Send(frame.Type, frame.Data); err != nil {
		logger := logging.Default()
		logger.Error("Failed to send frame to user WebSocket", "error", err)
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
