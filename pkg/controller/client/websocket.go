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
	ID       string
	Conn     *websocket.Conn
	mu       sync.Mutex
	cancel   context.CancelFunc
	closed   bool
	sendChan chan sendFrame
}

type sendFrame struct {
	messageType int
	data        []byte
}

// Send sends a message through the local WebSocket connection
func (lc *LocalWebSocketConnection) Send(messageType int, data []byte) error {
	lc.mu.Lock()
	if lc.closed || lc.Conn == nil {
		lc.mu.Unlock()
		return goerr.New("connection is closed")
	}
	lc.mu.Unlock()
	
	// Send through the write pump channel
	frame := sendFrame{
		messageType: messageType,
		data:        make([]byte, len(data)),
	}
	copy(frame.data, data)
	
	select {
	case lc.sendChan <- frame:
		return nil
	default:
		return goerr.New("send channel is full")
	}
}

// Close closes the local WebSocket connection
func (lc *LocalWebSocketConnection) Close() error {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	
	if lc.closed {
		return nil
	}
	
	lc.closed = true
	
	if lc.sendChan != nil {
		close(lc.sendChan)
	}
	
	if lc.cancel != nil {
		lc.cancel()
	}
	
	if lc.Conn != nil {
		err := lc.Conn.Close()
		lc.Conn = nil // Set to nil after closing to prevent multiple close attempts
		return err
	}
	
	return nil
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

		// Start pump goroutines
		go x.readPumpLocal(wsCtx, localConn)
		go x.writePumpLocal(wsCtx, localConn)

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

	// Configure WebSocket connection for transparent proxying
	// Set ping/pong handlers to forward frames transparently instead of auto-responding
	conn.SetPingHandler(func(appData string) error {
		logger.Info("Received ping from local WebSocket, forwarding to server", "id", req.ID, "data", appData)
		// Forward ping frame to server
		frame := model.NewWebSocketFrame(req.ID, websocket.PingMessage, []byte(appData))
		return x.sendWebSocketMessage(model.MessageTypeWebSocketFrame, frame)
	})
	
	conn.SetPongHandler(func(appData string) error {
		logger.Info("Received pong from local WebSocket, forwarding to server", "id", req.ID, "data", appData)
		// Forward pong frame to server
		frame := model.NewWebSocketFrame(req.ID, websocket.PongMessage, []byte(appData))
		return x.sendWebSocketMessage(model.MessageTypeWebSocketFrame, frame)
	})

	// Extract response headers for forwarding
	var responseHeaders http.Header
	if resp != nil {
		responseHeaders = resp.Header
	}

	return &LocalWebSocketConnection{
		ID:       req.ID,
		Conn:     conn,
		cancel:   nil, // Will be set by caller
		sendChan: make(chan sendFrame, 256), // Buffered channel for writes
	}, responseHeaders, nil
}

// readPumpLocal reads messages from local WebSocket and forwards to server
func (x *Client) readPumpLocal(ctx context.Context, localConn *LocalWebSocketConnection) {
	logger := logging.Extract(ctx)
	defer x.removeLocalWebSocketConnection(localConn.ID)
	defer localConn.Close()

	logger.Info("Starting local read pump", "id", localConn.ID)

	// Add panic recovery to handle gorilla/websocket panics
	defer func() {
		if r := recover(); r != nil {
			logger.Error("Panic in readPumpLocal", "error", r, "id", localConn.ID)
		}
	}()

	for {
		select {
		case <-ctx.Done():
			logger.Debug("Local read pump context cancelled", "id", localConn.ID)
			return
		default:
		}
		
		// Read message from local WebSocket
		messageType, data, err := localConn.Conn.ReadMessage()
		
		if err != nil {
			// Mark connection as closed
			localConn.mu.Lock()
			localConn.closed = true
			localConn.mu.Unlock()
			
			// Check if this is a normal close
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				logger.Debug("Local WebSocket closed normally", "id", localConn.ID)
			} else if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				logger.Error("Local WebSocket read error", "error", err)
			} else {
				logger.Debug("Local WebSocket closed", "error", err)
			}

			// Send close notification
			closeMsg := model.NewWebSocketClose(localConn.ID, websocket.CloseAbnormalClosure, err.Error())
			if err := x.sendWebSocketMessage(model.MessageTypeWebSocketClose, closeMsg); err != nil {
				logger.Error("Failed to send close notification", "error", err)
			}
			return
		}

		logger.Debug("Received frame from local WebSocket", 
			"id", localConn.ID, 
			"type", messageType, 
			"size", len(data),
			"data", string(data))

		// Handle WebSocket control frames
		switch messageType {
		case websocket.CloseMessage:
			logger.Debug("Received close frame from local WebSocket", "id", localConn.ID)
			// Forward close message to server and terminate
			frame := model.NewWebSocketFrame(localConn.ID, messageType, data)
			if err := x.sendWebSocketMessage(model.MessageTypeWebSocketFrame, frame); err != nil {
				logger.Error("Failed to forward close frame to server", "error", err)
			}
			return
		
		case websocket.PingMessage, websocket.PongMessage:
			// These should be handled by SetPingHandler/SetPongHandler
			logger.Warn("Unexpected ping/pong frame in ReadMessage loop", "id", localConn.ID, "type", messageType)
		}

		// Forward data frame to server
		frame := model.NewWebSocketFrame(localConn.ID, messageType, data)
		if err := x.sendWebSocketMessage(model.MessageTypeWebSocketFrame, frame); err != nil {
			logger.Error("Failed to forward frame to server", "error", err)
			return
		}
	}
}

// writePumpLocal writes messages from the send channel to local WebSocket
func (x *Client) writePumpLocal(ctx context.Context, localConn *LocalWebSocketConnection) {
	logger := logging.Extract(ctx)
	defer localConn.Close()

	logger.Info("Starting local write pump", "id", localConn.ID)

	defer func() {
		if r := recover(); r != nil {
			logger.Error("Panic in writePumpLocal", "error", r, "id", localConn.ID)
		}
	}()

	for {
		select {
		case <-ctx.Done():
			logger.Debug("Local write pump context cancelled", "id", localConn.ID)
			return
			
		case frame, ok := <-localConn.sendChan:
			if !ok {
				// Channel closed
				return
			}
			
			// Write message to local WebSocket
			if err := localConn.Conn.WriteMessage(frame.messageType, frame.data); err != nil {
				logger.Error("Failed to write to local WebSocket", "error", err, "id", localConn.ID)
				return
			}
			logger.Debug("Wrote frame to local WebSocket", "id", localConn.ID, "type", frame.messageType)
		}
	}
}

// handleWebSocketFrame handles WebSocket frame from server
func (x *Client) handleWebSocketFrame(frame *model.WebSocketFrame) {
	logger := logging.Default()
	logger.Debug("Handling WebSocket frame from server", 
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
		logger.Debug("Sent frame to local WebSocket", "id", frame.ConnectionID)
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
