package integration_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/m-mizutani/backstream/pkg/controller/client"
	"github.com/m-mizutani/backstream/pkg/controller/server"
	"github.com/m-mizutani/backstream/pkg/service/hub"
	"github.com/m-mizutani/backstream/pkg/service/tunnel"
	"github.com/stretchr/testify/require"
)

// TestWebSocketPingDebug - debug specific ping/pong issue
func TestWebSocketPingDebug(t *testing.T) {
	// Create simple echo server
	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}
	
	localServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Logf("Upgrade failed: %v", err)
			return
		}
		defer conn.Close()
		
		t.Logf("Local server: WebSocket connection established")
		
		for {
			messageType, data, err := conn.ReadMessage()
			if err != nil {
				t.Logf("Local server: Read error: %v", err)
				return
			}
			
			t.Logf("Local server: Received frame type=%d, data=%q", messageType, string(data))
			
			switch messageType {
			case websocket.PingMessage:
				t.Logf("Local server: Received ping, sending pong")
				if err := conn.WriteMessage(websocket.PongMessage, data); err != nil {
					t.Logf("Local server: Failed to send pong: %v", err)
					return
				}
				t.Logf("Local server: Sent pong")
			case websocket.PongMessage:
				t.Logf("Local server: Received pong")
			case websocket.TextMessage:
				t.Logf("Local server: Echoing text message")
				conn.WriteMessage(websocket.TextMessage, data)
			case websocket.CloseMessage:
				t.Logf("Local server: Received close message")
				return
			}
		}
	}))
	defer localServer.Close()
	
	// Setup backstream
	hubSvc := hub.New()
	serverHandler := server.New(hubSvc)
	backstreamServer := httptest.NewServer(serverHandler)
	defer backstreamServer.Close()

	tunnelSvc := tunnel.New(localServer.URL)
	backstreamClient := client.New(tunnelSvc, backstreamServer.URL, localServer.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Start client
	go func() {
		err := backstreamClient.Connect(ctx)
		if err != nil && err != context.Canceled {
			t.Logf("Client error: %v", err)
		}
	}()

	// Wait for connection
	time.Sleep(500 * time.Millisecond)

	// Connect as user
	wsURL := "ws" + backstreamServer.URL[4:] + "/"
	dialer := websocket.Dialer{
		HandshakeTimeout: 5 * time.Second,
	}

	userConn, _, err := dialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer userConn.Close()

	t.Logf("User: Connected to backstream")
	
	// Set pong handler to receive pong frames
	pongReceived := make(chan string, 1)
	userConn.SetPongHandler(func(appData string) error {
		t.Logf("User: Received pong via handler: %q", appData)
		pongReceived <- appData
		return nil
	})

	// Test 1: Send normal text message first
	t.Logf("User: Sending text message")
	err = userConn.WriteMessage(websocket.TextMessage, []byte("hello"))
	require.NoError(t, err)

	// Read response
	_, response, err := userConn.ReadMessage()
	require.NoError(t, err)
	t.Logf("User: Received response: %q", string(response))

	// Test 2: Send ping frame
	t.Logf("User: Sending ping frame")
	pingData := []byte("test-ping")
	err = userConn.WriteMessage(websocket.PingMessage, pingData)
	require.NoError(t, err)

	// Wait for pong response via handler
	select {
	case receivedData := <-pongReceived:
		t.Logf("User: Received pong data: %q", receivedData)
		if receivedData != string(pingData) {
			t.Errorf("Expected pong data %q, got %q", string(pingData), receivedData)
		}
		t.Logf("User: Ping/pong test successful!")
	case <-time.After(3 * time.Second):
		t.Fatalf("User: Timeout waiting for pong response")
	}
	
	// Close connection properly to avoid connection errors
	userConn.Close()
	
	// Wait a bit before ending test to allow cleanup
	time.Sleep(100 * time.Millisecond)
}