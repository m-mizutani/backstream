package model_test

import (
	"encoding/json"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/m-mizutani/backstream/pkg/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWebSocketUpgradeRequest(t *testing.T) {
	header := map[string]string{
		"Authorization": "Bearer test",
		"X-Custom":      "value",
	}

	req := model.NewWebSocketUpgradeRequest("/ws/test", header, "127.0.0.1:12345")

	assert.NotEmpty(t, req.ID)
	assert.Equal(t, "/ws/test", req.Path)
	assert.Equal(t, "Bearer test", req.Header["Authorization"])
	assert.Equal(t, "127.0.0.1:12345", req.Remote)

	// Test JSON serialization
	data, err := json.Marshal(req)
	require.NoError(t, err)

	var decoded model.WebSocketUpgradeRequest
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, req.ID, decoded.ID)
	assert.Equal(t, req.Path, decoded.Path)
}

func TestWebSocketUpgradeResponse(t *testing.T) {
	// Test accepted response
	resp := model.NewWebSocketUpgradeResponse("test-id", true, nil, "")
	assert.Equal(t, "test-id", resp.ID)
	assert.True(t, resp.Accepted)
	assert.Empty(t, resp.Error)

	// Test rejected response
	resp2 := model.NewWebSocketUpgradeResponse("test-id-2", false, nil, "connection failed")
	assert.Equal(t, "test-id-2", resp2.ID)
	assert.False(t, resp2.Accepted)
	assert.Equal(t, "connection failed", resp2.Error)
}

func TestWebSocketFrame(t *testing.T) {
	// Test text frame
	textFrame := model.NewWebSocketFrame("conn-1", websocket.TextMessage, []byte("hello"))
	assert.Equal(t, "conn-1", textFrame.ConnectionID)
	assert.Equal(t, websocket.TextMessage, textFrame.Type)
	assert.Equal(t, []byte("hello"), textFrame.Data)

	// Test binary frame
	binaryFrame := model.NewWebSocketFrame("conn-2", websocket.BinaryMessage, []byte{0x01, 0x02, 0x03})
	assert.Equal(t, "conn-2", binaryFrame.ConnectionID)
	assert.Equal(t, websocket.BinaryMessage, binaryFrame.Type)
	assert.Equal(t, []byte{0x01, 0x02, 0x03}, binaryFrame.Data)
}

func TestWebSocketClose(t *testing.T) {
	closeMsg := model.NewWebSocketClose("conn-1", websocket.CloseNormalClosure, "goodbye")
	assert.Equal(t, "conn-1", closeMsg.ConnectionID)
	assert.Equal(t, websocket.CloseNormalClosure, closeMsg.Code)
	assert.Equal(t, "goodbye", closeMsg.Reason)
}

func TestWebSocketMessage(t *testing.T) {
	frame := model.NewWebSocketFrame("conn-1", websocket.TextMessage, []byte("test"))
	msg, err := model.NewWebSocketMessage(model.MessageTypeWebSocketFrame, frame)
	require.NoError(t, err)

	assert.Equal(t, model.MessageTypeWebSocketFrame, msg.Type)
	
	// Unmarshal the Data field to verify it contains the frame
	var unmarshaledFrame model.WebSocketFrame
	err = json.Unmarshal(msg.Data, &unmarshaledFrame)
	require.NoError(t, err)
	assert.Equal(t, frame, &unmarshaledFrame)

	// Test JSON serialization
	data, err := json.Marshal(msg)
	require.NoError(t, err)

	var decoded model.WebSocketMessage
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, msg.Type, decoded.Type)
}
