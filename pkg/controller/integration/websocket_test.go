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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// startRealWebSocketServer starts a real WebSocket echo server for testing
func startRealWebSocketServer(t *testing.T) *httptest.Server {
	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Logf("Failed to upgrade: %v", err)
			return
		}
		defer conn.Close()

		// Echo server
		for {
			mt, message, err := conn.ReadMessage()
			if err != nil {
				return
			}

			if err := conn.WriteMessage(mt, message); err != nil {
				return
			}
		}
	})

	return httptest.NewServer(mux)
}

func TestWebSocketEndToEnd(t *testing.T) {
	// 1. Start local WebSocket server
	localWS := startRealWebSocketServer(t)
	defer localWS.Close()

	// 2. Start backstream server
	hubSvc := hub.New()
	serverHandler := server.New(hubSvc)
	backstreamServer := httptest.NewServer(serverHandler)
	defer backstreamServer.Close()

	// 3. Start backstream client in background
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	tunnelSvc := tunnel.New(localWS.URL)
	backstreamClient := client.New(tunnelSvc, backstreamServer.URL, localWS.URL)

	// Start monitoring for connection before starting the client
	connectionWait := waitForClientConnection(t, backstreamServer.URL)
	
	clientErr := make(chan error, 1)
	go func() {
		clientErr <- backstreamClient.Connect(ctx)
	}()

	// Wait for client to connect with timeout
	select {
	case <-connectionWait:
		t.Logf("Client connected successfully")
	case <-time.After(2 * time.Second):
		t.Fatal("Timeout waiting for client to connect")
	}

	// 4. Connect as end user to backstream server
	wsURL := "ws" + backstreamServer.URL[4:] + "/ws"
	dialer := websocket.Dialer{
		HandshakeTimeout: 5 * time.Second,
	}

	userConn, _, err := dialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer userConn.Close()

	// 5. Send message and verify echo
	testMsg := []byte("Hello WebSocket!")
	err = userConn.WriteMessage(websocket.TextMessage, testMsg)
	require.NoError(t, err)

	// 6. Read echo response with timeout
	err = userConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	require.NoError(t, err)
	mt, response, err := userConn.ReadMessage()
	require.NoError(t, err)
	assert.Equal(t, websocket.TextMessage, mt)
	assert.Equal(t, testMsg, response)

	// Clean shutdown
	cancel()
	select {
	case <-clientErr:
	case <-time.After(1 * time.Second):
	}
}

func TestHTTPAndWebSocketCoexistence(t *testing.T) {
	// Start HTTP server that also supports WebSocket
	mux := http.NewServeMux()

	// HTTP endpoint
	mux.HandleFunc("/api/test", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("HTTP response"))
	})

	// WebSocket endpoint
	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		// Echo
		for {
			mt, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if err := conn.WriteMessage(mt, msg); err != nil {
				return
			}
		}
	})

	localServer := httptest.NewServer(mux)
	defer localServer.Close()

	// Start backstream
	hubSvc := hub.New()
	serverHandler := server.New(hubSvc)
	backstreamServer := httptest.NewServer(serverHandler)
	defer backstreamServer.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	tunnelSvc := tunnel.New(localServer.URL)
	backstreamClient := client.New(tunnelSvc, backstreamServer.URL, localServer.URL)

	// Start monitoring for connection before starting the client
	connectionWait := waitForClientConnection(t, backstreamServer.URL)
	
	go func() {
		_ = backstreamClient.Connect(ctx)
	}()

	// Wait for client to connect with timeout
	select {
	case <-connectionWait:
		t.Logf("Client connected successfully")
	case <-time.After(2 * time.Second):
		t.Fatal("Timeout waiting for client to connect")
	}

	// Test HTTP request
	httpResp, err := http.Get(backstreamServer.URL + "/api/test")
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, httpResp.StatusCode)

	// Test WebSocket connection
	wsURL := "ws" + backstreamServer.URL[4:] + "/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer conn.Close()

	err = conn.WriteMessage(websocket.TextMessage, []byte("test"))
	require.NoError(t, err)

	err = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	require.NoError(t, err)
	_, msg, err := conn.ReadMessage()
	require.NoError(t, err)
	assert.Equal(t, []byte("test"), msg)
}
