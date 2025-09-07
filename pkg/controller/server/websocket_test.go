package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/m-mizutani/backstream/pkg/service/hub"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWebSocketSubprotocolNegotiation(t *testing.T) {
	// Create a hub service
	hubSvc := hub.New()

	// Create server
	srv := New(hubSvc)

	// Create test server
	ts := httptest.NewServer(srv)
	defer ts.Close()

	// Connect as a backstream client first
	clientURL := strings.Replace(ts.URL, "http", "ws", 1)
	clientHeader := http.Header{
		"Backstream-Client": []string{"test"},
	}
	clientConn, _, err := websocket.DefaultDialer.Dial(clientURL, clientHeader)
	require.NoError(t, err)
	defer clientConn.Close()

	// Start a goroutine to handle client messages
	go func() {
		for {
			_, message, err := clientConn.ReadMessage()
			if err != nil {
				return
			}
			// Parse message and respond to WebSocket upgrade requests
			if strings.Contains(string(message), "websocket_upgrade_request") {
				// Send acceptance response
				response := `{"type":"websocket_upgrade_response","data":{"id":"","accepted":true,"header":{}}}`
				// Extract ID from request and update response
				if idx := strings.Index(string(message), `"id":"`); idx > 0 {
					idStart := idx + 6
					idEnd := strings.Index(string(message)[idStart:], `"`)
					if idEnd > 0 {
						id := string(message)[idStart : idStart+idEnd]
						response = `{"type":"websocket_upgrade_response","data":{"id":"` + id + `","accepted":true,"header":{}}}`
					}
				}
				clientConn.WriteMessage(websocket.TextMessage, []byte(response))
			}
		}
	}()

	// Give client time to connect
	time.Sleep(100 * time.Millisecond)

	// Now test user WebSocket connection with subprotocol
	userHeader := http.Header{
		"Sec-WebSocket-Protocol": []string{"vite-hmr"},
	}
	
	t.Log("Attempting to connect with vite-hmr protocol")
	
	dialer := websocket.Dialer{
		HandshakeTimeout: 5 * time.Second,
	}
	
	userConn, resp, err := dialer.Dial(clientURL, userHeader)
	if err != nil {
		t.Logf("Connection error: %v", err)
		if resp != nil {
			t.Logf("Response status: %s", resp.Status)
			t.Logf("Response headers: %v", resp.Header)
		}
	}
	require.NoError(t, err)
	defer userConn.Close()

	// Check that the server accepted the subprotocol
	assert.Equal(t, "vite-hmr", userConn.Subprotocol())
	
	// Try to send a message
	err = userConn.WriteMessage(websocket.TextMessage, []byte(`{"type":"ping"}`))
	assert.NoError(t, err)
	
	// Keep connection alive for a bit
	time.Sleep(100 * time.Millisecond)
	
	// Check connection is still alive
	err = userConn.WriteMessage(websocket.TextMessage, []byte(`{"type":"ping2"}`))
	assert.NoError(t, err)
}