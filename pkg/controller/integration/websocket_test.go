package integration_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
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

// startViteHMRServer simulates a Vite dev server WebSocket endpoint
func startViteHMRServer(t *testing.T) *httptest.Server {
	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
		// Accept vite-hmr protocol
		Subprotocols: []string{"vite-hmr"},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		t.Logf("Vite HMR server received request: %s %s", r.Method, r.URL.Path)
		t.Logf("Headers: %v", r.Header)

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Logf("Failed to upgrade: %v", err)
			return
		}
		defer conn.Close()

		t.Logf("Vite HMR connection established, subprotocol: %s", conn.Subprotocol())

		// Send initial connected message like Vite does
		err = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"connected"}`))
		if err != nil {
			t.Logf("Failed to send connected message: %v", err)
			return
		}

		// Keep connection alive and echo messages
		for {
			mt, message, err := conn.ReadMessage()
			if err != nil {
				t.Logf("Read error: %v", err)
				return
			}
			t.Logf("Vite HMR received: %s", string(message))

			// Echo back
			if err := conn.WriteMessage(mt, message); err != nil {
				t.Logf("Write error: %v", err)
				return
			}
		}
	})

	return httptest.NewServer(mux)
}

func TestViteHMRProtocolE2E(t *testing.T) {
	// 1. Start mock Vite HMR server
	viteServer := startViteHMRServer(t)
	defer viteServer.Close()
	t.Logf("Vite HMR server started at: %s", viteServer.URL)

	// 2. Start backstream server
	hubSvc := hub.New()
	serverHandler := server.New(hubSvc)
	backstreamServer := httptest.NewServer(serverHandler)
	defer backstreamServer.Close()
	t.Logf("Backstream server started at: %s", backstreamServer.URL)

	// 3. Start backstream client connecting to Vite server
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tunnelSvc := tunnel.New(viteServer.URL)
	backstreamClient := client.New(tunnelSvc, backstreamServer.URL, viteServer.URL)

	clientErr := make(chan error, 1)
	go func() {
		err := backstreamClient.Connect(ctx)
		if err != nil {
			t.Logf("Client error: %v", err)
		}
		clientErr <- err
	}()

	// Wait a bit for client to connect
	time.Sleep(500 * time.Millisecond)

	// 4. Connect as browser with vite-hmr protocol
	wsURL := "ws" + backstreamServer.URL[4:] + "/"
	dialer := websocket.Dialer{
		HandshakeTimeout: 5 * time.Second,
		Subprotocols:     []string{"vite-hmr"}, // Request vite-hmr protocol
	}

	t.Logf("Connecting to backstream as browser with vite-hmr protocol...")
	browserConn, resp, err := dialer.Dial(wsURL, nil)
	if err != nil {
		t.Logf("Dial error: %v", err)
		if resp != nil {
			t.Logf("Response status: %s", resp.Status)
			t.Logf("Response headers: %v", resp.Header)
		}
	}
	require.NoError(t, err)
	defer browserConn.Close()

	// 5. Verify subprotocol was negotiated
	assert.Equal(t, "vite-hmr", browserConn.Subprotocol(), "Subprotocol should be vite-hmr")
	t.Logf("Successfully negotiated subprotocol: %s", browserConn.Subprotocol())

	// 6. Read initial connected message
	err = browserConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	require.NoError(t, err)

	mt, msg, err := browserConn.ReadMessage()
	require.NoError(t, err)
	assert.Equal(t, websocket.TextMessage, mt)
	assert.Equal(t, `{"type":"connected"}`, string(msg))
	t.Logf("Received initial message: %s", string(msg))

	// 7. Send a ping and verify pong response
	pingMsg := []byte(`{"type":"ping"}`)
	err = browserConn.WriteMessage(websocket.TextMessage, pingMsg)
	require.NoError(t, err)
	t.Logf("Sent ping message")

	// 8. Read pong response
	err = browserConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	require.NoError(t, err)

	mt, msg, err = browserConn.ReadMessage()
	require.NoError(t, err)
	assert.Equal(t, websocket.TextMessage, mt)
	expectedPong := []byte(`{"type":"pong"}`)
	assert.Equal(t, expectedPong, msg)
	t.Logf("Received pong response: %s", string(msg))

	// 9. Keep connection alive for a bit to ensure it doesn't close
	time.Sleep(100 * time.Millisecond)

	// Try another regular message to verify connection is still alive
	testMsg2 := []byte(`{"type":"test"}`)
	err = browserConn.WriteMessage(websocket.TextMessage, testMsg2)
	require.NoError(t, err)

	err = browserConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	require.NoError(t, err)

	_, msg, err = browserConn.ReadMessage()
	require.NoError(t, err)
	assert.Equal(t, testMsg2, msg)
	t.Logf("Connection still alive, received: %s", string(msg))

	// Clean shutdown
	cancel()
	select {
	case err := <-clientErr:
		if err != nil && err != context.Canceled {
			t.Errorf("Client error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Log("Client shutdown timeout")
	}
}

func TestWebSocketPingPongHandling(t *testing.T) {
	// Test ping/pong control message handling specifically
	viteServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{
			Subprotocols: []string{"vite-hmr"},
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			// Don't use t.Errorf in goroutines after test completion
			return
		}
		defer conn.Close()

		// Read first message (ping)
		_, msg, err := conn.ReadMessage()
		if err != nil {
			// Don't use t.Errorf in goroutines after test completion
			return
		}

		// Verify it's a ping message
		assert.Equal(t, `{"type":"ping"}`, string(msg))

		// Send confirmation that we received ping but don't respond
		// The server should handle ping internally and respond with pong
	}))
	defer viteServer.Close()

	// Create backstream server
	svc := hub.New()
	backstreamServer := server.New(svc)
	testBackstreamServer := httptest.NewServer(backstreamServer)
	defer testBackstreamServer.Close()

	// Start backstream client
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tunnelSvc := tunnel.New(viteServer.URL)
	clientInstance := client.New(tunnelSvc, testBackstreamServer.URL, viteServer.URL)
	clientErr := make(chan error, 1)
	go func() {
		err := clientInstance.Connect(ctx)
		if err != nil {
			t.Logf("Client error: %v", err)
		}
		clientErr <- err
	}()

	// Wait for client connection
	time.Sleep(100 * time.Millisecond)

	// Connect as browser
	dialer := websocket.Dialer{
		Subprotocols: []string{"vite-hmr"},
	}
	
	wsURL := strings.Replace(testBackstreamServer.URL, "http", "ws", 1) + "/"
	browserConn, resp, err := dialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer browserConn.Close()

	assert.Equal(t, "vite-hmr", resp.Header.Get("Sec-Websocket-Protocol"))

	// Send ping message
	err = browserConn.WriteMessage(websocket.TextMessage, []byte(`{"type":"ping"}`))
	require.NoError(t, err)

	// Should receive pong response
	err = browserConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	require.NoError(t, err)

	_, msg, err := browserConn.ReadMessage()
	require.NoError(t, err)
	assert.Equal(t, `{"type":"pong"}`, string(msg))

	// Test multiple ping/pong cycles
	for i := 0; i < 3; i++ {
		err = browserConn.WriteMessage(websocket.TextMessage, []byte(`{"type":"ping"}`))
		require.NoError(t, err)

		_, msg, err = browserConn.ReadMessage()
		require.NoError(t, err)
		assert.Equal(t, `{"type":"pong"}`, string(msg))
	}

	// Clean shutdown
	cancel()
}
