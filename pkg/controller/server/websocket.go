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
	ID           string
	Conn         *websocket.Conn
	mu           sync.Mutex
	cancel       context.CancelFunc
	closed       bool
	pendingFrames []pendingFrame
}

type pendingFrame struct {
	messageType int
	data        []byte
}

// Send sends a message through the WebSocket connection
func (wc *WebSocketConnection) Send(messageType int, data []byte) error {
	wc.mu.Lock()
	defer wc.mu.Unlock()
	
	if wc.closed {
		return goerr.New("connection is closed")
	}
	
	if wc.Conn == nil {
		// Connection not established yet, queue the frame
		wc.pendingFrames = append(wc.pendingFrames, pendingFrame{
			messageType: messageType,
			data:        make([]byte, len(data)), // Copy data to avoid issues
		})
		copy(wc.pendingFrames[len(wc.pendingFrames)-1].data, data)
		return nil
	}
	
	err := wc.Conn.WriteMessage(messageType, data)
	if err != nil {
		// Mark as closed on write error to prevent future attempts
		wc.closed = true
		return err
	}
	return nil
}

// flushPendingFrames sends all queued frames after connection establishment
func (wc *WebSocketConnection) flushPendingFrames() error {
	// This should be called with mutex already held
	if wc.Conn == nil || len(wc.pendingFrames) == 0 {
		return nil
	}
	
	logger := logging.Default()
	logger.Debug("Flushing pending frames", "id", wc.ID, "count", len(wc.pendingFrames))
	
	for _, frame := range wc.pendingFrames {
		if err := wc.Conn.WriteMessage(frame.messageType, frame.data); err != nil {
			wc.closed = true
			return err
		}
	}
	
	// Clear pending frames
	wc.pendingFrames = nil
	return nil
}

// Close closes the WebSocket connection
func (wc *WebSocketConnection) Close() error {
	wc.mu.Lock()
	defer wc.mu.Unlock()
	
	if wc.closed {
		return nil
	}
	
	wc.closed = true
	
	// Clear pending frames
	wc.pendingFrames = nil
	
	if wc.cancel != nil {
		wc.cancel()
	}
	if wc.Conn != nil {
		return wc.Conn.Close()
	}
	return nil
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
		"headers", r.Header,
		"remoteAddr", r.RemoteAddr,
		"host", r.Host,
		"proto", r.Proto,
		"userAgent", r.UserAgent())

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

	// Create a temporary connection entry BEFORE sending request to avoid race condition
	// This ensures the connection is registered before client can send frames
	wsCtx, wsCancel := context.WithCancel(context.Background())
	tempConn := &WebSocketConnection{
		ID:     upgradeReq.ID,
		Conn:   nil, // Will be set after upgrade
		cancel: wsCancel,
	}
	x.addWebSocketConnection(upgradeReq.ID, tempConn)

	// Send to hub and wait for response
	respChan := make(chan *model.WebSocketUpgradeResponse, 1)
	x.registerUpgradeRequest(upgradeReq.ID, respChan)
	defer x.unregisterUpgradeRequest(upgradeReq.ID)

	if err := x.svc.Broadcast(msgData); err != nil {
		logger.Error("Failed to broadcast upgrade request", "error", err)
		x.removeWebSocketConnection(upgradeReq.ID) // Clean up on failure
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
			x.removeWebSocketConnection(upgradeReq.ID) // Clean up on rejection
			http.Error(w, "upgrade rejected", http.StatusBadGateway)
			return
		}

		// Prepare response headers from client
		var responseHeader http.Header
		if resp.Header != nil {
			responseHeader = http.Header(resp.Header)
		}
		
		// Log subprotocol negotiation details before upgrade
		requestedProtocols := r.Header.Get("Sec-WebSocket-Protocol")
		logger.Info("WebSocket subprotocol negotiation", 
			"id", upgradeReq.ID,
			"requestedProtocols", requestedProtocols,
			"responseHeaders", responseHeader)

		// Upgrade the connection with response headers
		// The upgrade function will handle subprotocol negotiation
		logger.Info("Attempting WebSocket upgrade", "id", upgradeReq.ID)
		conn, err := x.upgrade(w, r, responseHeader)
		if err != nil {
			logger.Error("Failed to upgrade WebSocket", "error", err, "id", upgradeReq.ID, "responseHeader", responseHeader)
			x.removeWebSocketConnection(upgradeReq.ID) // Clean up on failure
			return
		}
		logger.Info("WebSocket upgrade successful", "id", upgradeReq.ID)

		// Update the connection with the actual WebSocket
		tempConn.mu.Lock()
		tempConn.Conn = conn
		
		// Send any frames that were queued before connection establishment
		if err := tempConn.flushPendingFrames(); err != nil {
			tempConn.mu.Unlock()
			logger.Error("Failed to flush pending frames", "error", err, "id", upgradeReq.ID)
			x.removeWebSocketConnection(upgradeReq.ID)
			return
		}
		tempConn.mu.Unlock()

		// Log negotiated subprotocol after upgrade
		negotiatedProtocol := conn.Subprotocol()
		
		// Configure WebSocket connection for transparent proxying
		// Disable automatic ping/pong handling to allow transparent forwarding
		conn.SetPingHandler(func(appData string) error {
			logger.Info("Received ping from user, forwarding transparently", "id", upgradeReq.ID, "data", appData)
			// Forward ping frame to client instead of responding automatically
			frame := model.NewWebSocketFrame(upgradeReq.ID, websocket.PingMessage, []byte(appData))
			return x.broadcastWebSocketMessage(model.MessageTypeWebSocketFrame, frame)
		})
		
		conn.SetPongHandler(func(appData string) error {
			logger.Info("Received pong from user, forwarding transparently", "id", upgradeReq.ID, "data", appData)
			// Forward pong frame to client
			frame := model.NewWebSocketFrame(upgradeReq.ID, websocket.PongMessage, []byte(appData))
			return x.broadcastWebSocketMessage(model.MessageTypeWebSocketFrame, frame)
		})
		
		logger.Info("WebSocket connection established", 
			"id", upgradeReq.ID,
			"negotiatedProtocol", negotiatedProtocol,
			"requestedProtocols", requestedProtocols)

		// WebSocket connection is now ready for transparent proxying
		// No initial handshake needed - let the actual WebSocket communication flow through

		// Start relaying messages from the user to the backstream client
		// Handler must block to keep WebSocket connection alive
		x.relayUserToClient(wsCtx, tempConn)

	case <-ctx.Done():
		logger.Error("WebSocket upgrade timeout")
		x.removeWebSocketConnection(upgradeReq.ID) // Clean up on timeout
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
			// Check if connection is closed before attempting read
			wsConn.mu.Lock()
			if wsConn.closed || wsConn.Conn == nil {
				wsConn.mu.Unlock()
				logger.Debug("WebSocket connection already closed, terminating relay", "id", wsConn.ID)
				return
			}
			wsConn.mu.Unlock()
			
			// Set read deadline to avoid blocking forever
			wsConn.Conn.SetReadDeadline(time.Now().Add(1 * time.Second))
			messageType, data, err := wsConn.Conn.ReadMessage()
			
			// Check if this is a timeout error due to context cancellation
			if err != nil {
				// Mark connection as closed to prevent future read attempts
				wsConn.mu.Lock()
				wsConn.closed = true
				wsConn.mu.Unlock()
				
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

			logger.Info("Received frame from user WebSocket", 
				"id", wsConn.ID, 
				"type", messageType, 
				"size", len(data),
				"data", string(data))

			// Handle WebSocket control frames according to RFC 6455
			// For a WebSocket proxy, we should transparently forward control frames
			switch messageType {
			case websocket.CloseMessage:
				// Handle close frame according to RFC 6455
				logger.Debug("Received close frame from user", "id", wsConn.ID)
				// Forward close message to client and terminate connection
				frame := model.NewWebSocketFrame(wsConn.ID, messageType, data)
				if err := x.broadcastWebSocketMessage(model.MessageTypeWebSocketFrame, frame); err != nil {
					logger.Error("Failed to forward close frame to client", "error", err)
				}
				return
			
			case websocket.PingMessage, websocket.PongMessage:
				// Ping/pong frames are now handled by SetPingHandler/SetPongHandler
				// This should not be reached anymore
				logger.Warn("Unexpected ping/pong frame in ReadMessage loop", "id", wsConn.ID, "type", messageType)
			}

			// Handle application-level protocol control messages for text frames
			if messageType == websocket.TextMessage {
				if handled, err := x.handleProtocolControlMessage(wsConn, data); err != nil {
					logger.Error("Failed to handle protocol control message", "error", err, "id", wsConn.ID)
					return
				} else if handled {
					// Message was handled as a control message, don't forward to client
					continue
				}
			}

			// Forward data frame to client
			logger.Info("About to forward frame to client", "id", wsConn.ID, "type", messageType)
			frame := model.NewWebSocketFrame(wsConn.ID, messageType, data)
			if err := x.broadcastWebSocketMessage(model.MessageTypeWebSocketFrame, frame); err != nil {
				logger.Error("Failed to forward frame to client", "error", err)
				return
			}
			logger.Info("Forwarded frame to client", "id", wsConn.ID, "type", messageType)
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

	logger.Info("Found WebSocket connection, sending frame", "id", frame.ConnectionID)
	if err := conn.Send(frame.Type, frame.Data); err != nil {
		logger.Error("Failed to send frame to user WebSocket", "error", err)
		// Remove the connection if send fails to prevent further attempts
		x.removeWebSocketConnection(frame.ConnectionID)
	} else {
		logger.Info("Successfully sent frame to user WebSocket", "id", frame.ConnectionID)
	}
}


// handleProtocolControlMessage handles protocol-specific control messages
// Returns (handled, error) - handled=true means the message was processed and should not be forwarded
func (x *Server) handleProtocolControlMessage(wsConn *WebSocketConnection, data []byte) (bool, error) {
	logger := logging.Default()
	
	// Try to parse as JSON control message
	var message map[string]any
	if err := json.Unmarshal(data, &message); err != nil {
		// Not a JSON message, let it pass through
		return false, nil
	}
	
	msgType, ok := message["type"].(string)
	if !ok {
		// No type field, let it pass through
		return false, nil
	}
	
	// Handle common control message patterns
	switch msgType {
	case "ping":
		// Respond with pong for ping-pong keepalive pattern
		logger.Debug("Received ping, responding with pong", "id", wsConn.ID)
		pongMsg := map[string]any{"type": "pong"}
		pongData, _ := json.Marshal(pongMsg)
		
		if err := wsConn.Send(websocket.TextMessage, pongData); err != nil {
			return true, err
		}
		logger.Debug("Sent pong response", "id", wsConn.ID)
		return true, nil
		
	case "pong":
		// Acknowledge pong response (no action needed, just don't forward)
		logger.Debug("Received pong response", "id", wsConn.ID)
		return true, nil
		
	default:
		// Unknown control message type, let it pass through
		return false, nil
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
