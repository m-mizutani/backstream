package model

import (
	"github.com/google/uuid"
)

// WebSocket message types
const (
	MessageTypeWebSocketUpgradeRequest  = "websocket_upgrade_request"
	MessageTypeWebSocketUpgradeResponse = "websocket_upgrade_response"
	MessageTypeWebSocketFrame           = "websocket_frame"
	MessageTypeWebSocketClose           = "websocket_close"
)

// WebSocketUpgradeRequest represents a request to upgrade to WebSocket
type WebSocketUpgradeRequest struct {
	ID     string            `json:"id"`
	Path   string            `json:"path"`
	Header map[string]string `json:"header"`
	Remote string            `json:"remote"`
}

// NewWebSocketUpgradeRequest creates a new WebSocket upgrade request
func NewWebSocketUpgradeRequest(path string, header map[string]string, remote string) *WebSocketUpgradeRequest {
	return &WebSocketUpgradeRequest{
		ID:     uuid.New().String(),
		Path:   path,
		Header: header,
		Remote: remote,
	}
}

// WebSocketUpgradeResponse represents a response to WebSocket upgrade request
type WebSocketUpgradeResponse struct {
	ID       string              `json:"id"`
	Accepted bool                `json:"accepted"`
	Header   map[string][]string `json:"header,omitempty"`
	Error    string              `json:"error,omitempty"`
}

// NewWebSocketUpgradeResponse creates a new WebSocket upgrade response
func NewWebSocketUpgradeResponse(id string, accepted bool, header map[string][]string, errMsg string) *WebSocketUpgradeResponse {
	return &WebSocketUpgradeResponse{
		ID:       id,
		Accepted: accepted,
		Header:   header,
		Error:    errMsg,
	}
}

// WebSocketFrame represents a WebSocket frame data
type WebSocketFrame struct {
	ConnectionID string `json:"connection_id"`
	Type         int    `json:"type"` // websocket.TextMessage or websocket.BinaryMessage
	Data         []byte `json:"data"`
}

// NewWebSocketFrame creates a new WebSocket frame
func NewWebSocketFrame(connectionID string, frameType int, data []byte) *WebSocketFrame {
	return &WebSocketFrame{
		ConnectionID: connectionID,
		Type:         frameType,
		Data:         data,
	}
}

// WebSocketClose represents a WebSocket connection close notification
type WebSocketClose struct {
	ConnectionID string `json:"connection_id"`
	Code         int    `json:"code"`
	Reason       string `json:"reason"`
}

// NewWebSocketClose creates a new WebSocket close notification
func NewWebSocketClose(connectionID string, code int, reason string) *WebSocketClose {
	return &WebSocketClose{
		ConnectionID: connectionID,
		Code:         code,
		Reason:       reason,
	}
}

// Message wrapper for WebSocket messages
type WebSocketMessage struct {
	Type string      `json:"type"`
	Data interface{} `json:"data"`
}

// NewWebSocketMessage creates a new WebSocket message wrapper
func NewWebSocketMessage(msgType string, data interface{}) *WebSocketMessage {
	return &WebSocketMessage{
		Type: msgType,
		Data: data,
	}
}