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
	ID           string
	Conn         *websocket.Conn
	mu           sync.Mutex
	cancel       context.CancelFunc
	closed       bool
	pendingFrames []pendingFrame
	// Channels for pump goroutines
	sendChan     chan pendingFrame
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
	
	// Send through the write pump channel instead of direct write
	frame := pendingFrame{
		messageType: messageType,
		data:        make([]byte, len(data)),
	}
	copy(frame.data, data)
	
	select {
	case wc.sendChan <- frame:
		return nil
	default:
		// Channel is full, this is an error condition
		return goerr.New("send channel is full")
	}
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
	
	// Close the send channel to signal write pump to stop
	if wc.sendChan != nil {
		close(wc.sendChan)
	}
	
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
		ID:       upgradeReq.ID,
		Conn:     nil, // Will be set after upgrade
		cancel:   wsCancel,
		sendChan: make(chan pendingFrame, 256), // Buffered channel for queued messages
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
		
		// Start the write pump goroutine BEFORE flushing pending frames
		// This ensures all pending frames are processed through the pump
		go x.writePump(wsCtx, tempConn)
		
		// Send any frames that were queued before connection establishment
		// Now they go through the write pump instead of direct write
		for _, frame := range tempConn.pendingFrames {
			select {
			case tempConn.sendChan <- frame:
				logger.Debug("Queued pending frame to write pump", "id", upgradeReq.ID)
			default:
				logger.Error("Write pump channel full, dropping frame", "id", upgradeReq.ID)
			}
		}
		tempConn.pendingFrames = nil // Clear pending frames after queueing
		tempConn.mu.Unlock()

		// Log negotiated subprotocol after upgrade
		negotiatedProtocol := conn.Subprotocol()
		
		// Configure WebSocket connection for transparent proxying
		// Disable automatic ping/pong handling to allow transparent forwarding
		conn.SetPingHandler(func(appData string) error {
			logger.Debug("Received ping from user, forwarding transparently", "id", upgradeReq.ID, "data", appData)
			// Forward ping frame to client instead of responding automatically
			frame := model.NewWebSocketFrame(upgradeReq.ID, websocket.PingMessage, []byte(appData))
			if err := x.broadcastWebSocketMessage(model.MessageTypeWebSocketFrame, frame); err != nil {
				logger.Error("Failed to forward ping frame", "error", err, "id", upgradeReq.ID)
			}
			// Always return nil to prevent connection closure
			return nil
		})
		
		conn.SetPongHandler(func(appData string) error {
			logger.Debug("Received pong from user, forwarding transparently", "id", upgradeReq.ID, "data", appData)
			// Forward pong frame to client
			frame := model.NewWebSocketFrame(upgradeReq.ID, websocket.PongMessage, []byte(appData))
			if err := x.broadcastWebSocketMessage(model.MessageTypeWebSocketFrame, frame); err != nil {
				logger.Error("Failed to forward pong frame", "error", err, "id", upgradeReq.ID)
			}
			// Always return nil to prevent connection closure
			return nil
		})
		
		logger.Info("WebSocket connection established", 
			"id", upgradeReq.ID,
			"negotiatedProtocol", negotiatedProtocol,
			"requestedProtocols", requestedProtocols)

		// WebSocket connection is now ready for transparent proxying
		// No initial handshake needed - let the actual WebSocket communication flow through

		// Start the read pump in a goroutine
		// The handler blocks here waiting for context cancellation
		go x.readPump(wsCtx, tempConn)
		
		// Block until context is cancelled
		<-wsCtx.Done()
		logger.Debug("WebSocket context cancelled", "id", upgradeReq.ID)
		
		// Clean up
		tempConn.Close()
		x.removeWebSocketConnection(upgradeReq.ID)

	case <-ctx.Done():
		logger.Error("WebSocket upgrade timeout")
		x.removeWebSocketConnection(upgradeReq.ID) // Clean up on timeout
		http.Error(w, "upgrade timeout", http.StatusGatewayTimeout)
		return
	}
}

// readPump reads messages from the WebSocket connection and forwards them to the client
func (x *Server) readPump(ctx context.Context, wsConn *WebSocketConnection) {
	logger := logging.Extract(ctx)
	defer func() {
		if wsConn.cancel != nil {
			wsConn.cancel() // Cancel context to stop write pump
		}
	}()

	logger.Debug("Starting read pump", "id", wsConn.ID)

	// Add panic recovery to handle gorilla/websocket panics
	defer func() {
		if r := recover(); r != nil {
			logger.Error("Panic in readPump", "error", r, "id", wsConn.ID)
		}
	}()

	for {
		// Check if context is cancelled
		select {
		case <-ctx.Done():
			logger.Debug("Read pump context cancelled", "id", wsConn.ID)
			return
		default:
		}

		// Read message from WebSocket
		messageType, data, err := wsConn.Conn.ReadMessage()
		
		if err != nil {
			// Mark connection as closed
			wsConn.mu.Lock()
			wsConn.closed = true
			wsConn.mu.Unlock()
			
			// Check if this is a normal close
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				logger.Debug("User WebSocket closed normally", "id", wsConn.ID)
				// Forward normal close to client
				closeMsg := model.NewWebSocketClose(wsConn.ID, websocket.CloseNormalClosure, "connection closed")
				if err := x.broadcastWebSocketMessage(model.MessageTypeWebSocketClose, closeMsg); err != nil {
					logger.Error("Failed to broadcast close notification", "error", err)
				}
				return
			}
			
			// Log errors
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				logger.Error("WebSocket unexpected read error", "error", err, "id", wsConn.ID)
			} else {
				logger.Debug("WebSocket read error", "error", err, "id", wsConn.ID)
			}

			// Send close notification
			closeMsg := model.NewWebSocketClose(wsConn.ID, websocket.CloseAbnormalClosure, err.Error())
			if err := x.broadcastWebSocketMessage(model.MessageTypeWebSocketClose, closeMsg); err != nil {
				logger.Error("Failed to broadcast close notification", "error", err)
			}
			return
		}

		logger.Debug("Received frame from user WebSocket", 
			"id", wsConn.ID, 
			"type", messageType, 
			"size", len(data),
			"data", string(data))

		// Handle WebSocket control frames
		switch messageType {
		case websocket.CloseMessage:
			logger.Debug("Received close frame from user", "id", wsConn.ID)
			// Forward close message to client and terminate
			frame := model.NewWebSocketFrame(wsConn.ID, messageType, data)
			if err := x.broadcastWebSocketMessage(model.MessageTypeWebSocketFrame, frame); err != nil {
				logger.Error("Failed to forward close frame to client", "error", err)
			}
			return
		
		case websocket.PingMessage, websocket.PongMessage:
			// These should be handled by SetPingHandler/SetPongHandler
			logger.Warn("Unexpected ping/pong frame in ReadMessage loop", "id", wsConn.ID, "type", messageType)
		}

		// For transparent WebSocket proxy, forward all application messages without interpretation
		// This allows the actual WebSocket protocol (like Vite HMR) to be handled by the real server

		// Forward data frame to client
		logger.Debug("Forwarding frame to client", "id", wsConn.ID, "type", messageType)
		frame := model.NewWebSocketFrame(wsConn.ID, messageType, data)
		if err := x.broadcastWebSocketMessage(model.MessageTypeWebSocketFrame, frame); err != nil {
			logger.Error("Failed to forward frame to client", "error", err)
			return
		}
	}
}

// writePump writes messages from the send channel to the WebSocket connection
func (x *Server) writePump(ctx context.Context, wsConn *WebSocketConnection) {
	logger := logging.Extract(ctx)
	defer func() {
		wsConn.Close()
	}()

	logger.Debug("Starting write pump", "id", wsConn.ID)

	// Add panic recovery
	defer func() {
		if r := recover(); r != nil {
			logger.Error("Panic in writePump", "error", r, "id", wsConn.ID)
		}
	}()

	for {
		select {
		case <-ctx.Done():
			logger.Debug("Write pump context cancelled", "id", wsConn.ID)
			return
			
		case frame := <-wsConn.sendChan:
			// Write message to WebSocket
			if err := wsConn.Conn.WriteMessage(frame.messageType, frame.data); err != nil {
				logger.Error("Failed to write message", "error", err, "id", wsConn.ID)
				return
			}
			logger.Debug("Wrote frame to WebSocket", "id", wsConn.ID, "type", frame.messageType)
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
	logger.Debug("Handling WebSocket frame from client", 
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
	logger.Debug("Available WebSocket connections", "connectionIDs", connIDs, "lookingFor", frame.ConnectionID)

	conn, ok := x.getWebSocketConnection(frame.ConnectionID)
	if !ok {
		logger.Error("No user WebSocket connection found", 
			"id", frame.ConnectionID, 
			"availableConnections", connIDs)
		return
	}

	logger.Debug("Found WebSocket connection, sending frame", "id", frame.ConnectionID)
	if err := conn.Send(frame.Type, frame.Data); err != nil {
		logger.Error("Failed to send frame to user WebSocket", "error", err)
		// Remove the connection if send fails to prevent further attempts
		x.removeWebSocketConnection(frame.ConnectionID)
	} else {
		logger.Debug("Successfully sent frame to user WebSocket", "id", frame.ConnectionID)
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
