package model_test

import (
	"encoding/json"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/m-mizutani/backstream/pkg/model"
	"github.com/m-mizutani/gt"
)

func TestWebSocketUpgradeRequest(t *testing.T) {
	header := map[string]string{
		"Authorization": "Bearer test",
		"X-Custom":      "value",
	}

	req := model.NewWebSocketUpgradeRequest("/ws/test", header, "127.0.0.1:12345")

	gt.String(t, req.ID).IsNotEmpty()
	gt.Value(t, req.Path).Equal("/ws/test")
	gt.Value(t, req.Header["Authorization"]).Equal("Bearer test")
	gt.Value(t, req.Remote).Equal("127.0.0.1:12345")

	// Test JSON serialization
	data, err := json.Marshal(req)
	gt.NoError(t, err).Required()

	var decoded model.WebSocketUpgradeRequest
	err = json.Unmarshal(data, &decoded)
	gt.NoError(t, err).Required()

	gt.Value(t, decoded.ID).Equal(req.ID)
	gt.Value(t, decoded.Path).Equal(req.Path)
}

func TestWebSocketUpgradeResponse(t *testing.T) {
	// Test accepted response
	resp := model.NewWebSocketUpgradeResponse("test-id", true, nil, "")
	gt.Value(t, resp.ID).Equal("test-id")
	gt.Bool(t, resp.Accepted).True()
	gt.String(t, resp.Error).IsEmpty()

	// Test rejected response
	resp2 := model.NewWebSocketUpgradeResponse("test-id-2", false, nil, "connection failed")
	gt.Value(t, resp2.ID).Equal("test-id-2")
	gt.Bool(t, resp2.Accepted).False()
	gt.Value(t, resp2.Error).Equal("connection failed")
}

func TestWebSocketFrame(t *testing.T) {
	// Test text frame
	textFrame := model.NewWebSocketFrame("conn-1", websocket.TextMessage, []byte("hello"))
	gt.Value(t, textFrame.ConnectionID).Equal("conn-1")
	gt.Value(t, textFrame.Type).Equal(websocket.TextMessage)
	gt.Value(t, textFrame.Data).Equal([]byte("hello"))

	// Test binary frame
	binaryFrame := model.NewWebSocketFrame("conn-2", websocket.BinaryMessage, []byte{0x01, 0x02, 0x03})
	gt.Value(t, binaryFrame.ConnectionID).Equal("conn-2")
	gt.Value(t, binaryFrame.Type).Equal(websocket.BinaryMessage)
	gt.Value(t, binaryFrame.Data).Equal([]byte{0x01, 0x02, 0x03})
}

func TestWebSocketClose(t *testing.T) {
	closeMsg := model.NewWebSocketClose("conn-1", websocket.CloseNormalClosure, "goodbye")
	gt.Value(t, closeMsg.ConnectionID).Equal("conn-1")
	gt.Value(t, closeMsg.Code).Equal(websocket.CloseNormalClosure)
	gt.Value(t, closeMsg.Reason).Equal("goodbye")
}

func TestWebSocketMessage(t *testing.T) {
	frame := model.NewWebSocketFrame("conn-1", websocket.TextMessage, []byte("test"))
	msg, err := model.NewWebSocketMessage(model.MessageTypeWebSocketFrame, frame)
	gt.NoError(t, err).Required()

	gt.Value(t, msg.Type).Equal(model.MessageTypeWebSocketFrame)

	// Unmarshal the Data field to verify it contains the frame
	var unmarshaledFrame model.WebSocketFrame
	err = json.Unmarshal(msg.Data, &unmarshaledFrame)
	gt.NoError(t, err).Required()
	gt.Value(t, &unmarshaledFrame).Equal(frame)

	// Test JSON serialization
	data, err := json.Marshal(msg)
	gt.NoError(t, err).Required()

	var decoded model.WebSocketMessage
	err = json.Unmarshal(data, &decoded)
	gt.NoError(t, err).Required()

	gt.Value(t, decoded.Type).Equal(msg.Type)
}
