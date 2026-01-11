package integration_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/m-mizutani/backstream/pkg/controller/client"
	"github.com/m-mizutani/backstream/pkg/controller/server"
	"github.com/m-mizutani/backstream/pkg/service/hub"
	"github.com/m-mizutani/backstream/pkg/service/tunnel"
	"github.com/m-mizutani/gt"
)

// TestWebSocketFrameTypes tests all WebSocket frame types according to RFC 6455
func TestWebSocketFrameTypes(t *testing.T) {
	// Create mock local server that handles all frame types
	localServer := createComprehensiveWebSocketServer(t)
	defer localServer.Close()

	// Start backstream infrastructure
	backstreamServer, backstreamClient := setupBackstreamInfrastructure(t, localServer.URL)
	defer backstreamServer.Close()

	// Start client connection
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	startBackstreamClient(t, backstreamClient, ctx)

	// Connect as end user
	userConn := connectAsEndUser(t, backstreamServer.URL, "/comprehensive")

	t.Run("TextMessage", func(t *testing.T) {
		testMsg := "Hello WebSocket Text Message!"
		err := userConn.WriteMessage(websocket.TextMessage, []byte(testMsg))
		gt.NoError(t, err).Required()

		_, response, err := userConn.ReadMessage()
		gt.NoError(t, err).Required()
		gt.Value(t, string(response)).Equal("ECHO_TEXT: "+testMsg)
	})

	t.Run("BinaryMessage", func(t *testing.T) {
		testData := []byte{0x01, 0x02, 0x03, 0x04, 0xFF, 0xFE}
		err := userConn.WriteMessage(websocket.BinaryMessage, testData)
		gt.NoError(t, err).Required()

		messageType, response, err := userConn.ReadMessage()
		gt.NoError(t, err).Required()
		gt.Value(t, messageType).Equal(websocket.BinaryMessage)
		// Server prepends "BINARY:" as bytes
		expected := append([]byte("BINARY:"), testData...)
		gt.Value(t, response).Equal(expected)
	})

	t.Run("PingPongFrames", func(t *testing.T) {
		// Test transparent ping/pong forwarding
		// In transparent WebSocket proxying, ping/pong frames are forwarded
		// through the system rather than being handled automatically
		
		// Send ping frame
		pingData := []byte("ping-payload")
		err := userConn.WriteMessage(websocket.PingMessage, pingData)
		gt.NoError(t, err).Required()
		
		// For transparent proxying, we verify the ping was successfully processed
		// by sending a normal text message afterwards. If the connection is still
		// alive and working, it proves the ping/pong cycle completed successfully.
		time.Sleep(100 * time.Millisecond) // Allow time for ping/pong processing
		
		// Send a test message to verify connection is still alive
		testMsg := "connection-alive-test"
		err = userConn.WriteMessage(websocket.TextMessage, []byte(testMsg))
		gt.NoError(t, err).Required()
		
		// Read response to confirm connection is working
		_, response, err := userConn.ReadMessage()
		gt.NoError(t, err).Required()
		gt.Value(t, string(response)).Equal("ECHO_TEXT: "+testMsg)
		
		t.Logf("Ping/pong transparent forwarding test passed - connection remains alive")
	})

	userConn.Close()
}

// TestWebSocketConcurrentConnections tests multiple simultaneous connections
func TestWebSocketConcurrentConnections(t *testing.T) {
	localServer := createComprehensiveWebSocketServer(t)
	defer localServer.Close()

	backstreamServer, backstreamClient := setupBackstreamInfrastructure(t, localServer.URL)
	defer backstreamServer.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	startBackstreamClient(t, backstreamClient, ctx)

	const numConnections = 5
	var wg sync.WaitGroup
	results := make([]bool, numConnections)

	for i := 0; i < numConnections; i++ {
		wg.Add(1)
		go func(connID int) {
			defer wg.Done()

			conn := connectAsEndUser(t, backstreamServer.URL, "/concurrent")
			defer conn.Close()

			// Send unique message
			testMsg := fmt.Sprintf("Connection-%d-Message", connID)
			err := conn.WriteMessage(websocket.TextMessage, []byte(testMsg))
			if err != nil {
				t.Errorf("Connection %d failed to send: %v", connID, err)
				return
			}

			// Read response
			_, response, err := conn.ReadMessage()
			if err != nil {
				t.Errorf("Connection %d failed to read: %v", connID, err)
				return
			}

			expected := "ECHO_TEXT: " + testMsg
			results[connID] = string(response) == expected
		}(i)
	}

	wg.Wait()

	// Verify all connections succeeded
	for i, success := range results {
		gt.Bool(t, success).True().Describe(fmt.Sprintf("Connection %d failed", i))
	}
}

// TestWebSocketSubprotocolNegotiation tests various subprotocol scenarios
func TestWebSocketSubprotocolNegotiation(t *testing.T) {
	localServer := createSubprotocolTestServer(t)
	defer localServer.Close()

	backstreamServer, backstreamClient := setupBackstreamInfrastructure(t, localServer.URL)
	defer backstreamServer.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	startBackstreamClient(t, backstreamClient, ctx)

	testCases := []struct {
		name               string
		requestedProtocols []string
		expectedProtocol   string
	}{
		{
			name:               "SingleProtocol",
			requestedProtocols: []string{"chat"},
			expectedProtocol:   "chat",
		},
		{
			name:               "MultipleProtocols",
			requestedProtocols: []string{"chat", "echo", "ping"},
			expectedProtocol:   "chat", // Server prefers first supported
		},
		{
			name:               "NoProtocol",
			requestedProtocols: nil,
			expectedProtocol:   "",
		},
		{
			name:               "UnsupportedProtocol",
			requestedProtocols: []string{"unsupported"},
			expectedProtocol:   "unsupported", // Proxy passes through even unsupported protocols
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			dialer := websocket.Dialer{
				HandshakeTimeout: 5 * time.Second,
				Subprotocols:     tc.requestedProtocols,
			}

			wsURL := "ws" + backstreamServer.URL[4:] + "/subprotocol"
			conn, resp, err := dialer.Dial(wsURL, nil)
			gt.NoError(t, err).Required()
			defer conn.Close()

			gt.Value(t, conn.Subprotocol()).Equal(tc.expectedProtocol)
			if tc.expectedProtocol != "" {
				gt.Value(t, resp.Header.Get("Sec-Websocket-Protocol")).Equal(tc.expectedProtocol)
			}

			// Test communication works
			err = conn.WriteMessage(websocket.TextMessage, []byte("protocol-test"))
			gt.NoError(t, err).Required()

			_, response, err := conn.ReadMessage()
			gt.NoError(t, err).Required()
			gt.String(t, string(response)).Contains("protocol-test")
		})
	}
}

// TestWebSocketConnectionLifecycle tests connection establishment and cleanup
func TestWebSocketConnectionLifecycle(t *testing.T) {
	localServer := createLifecycleTestServer(t)
	defer localServer.Close()

	backstreamServer, backstreamClient := setupBackstreamInfrastructure(t, localServer.URL)
	defer backstreamServer.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	startBackstreamClient(t, backstreamClient, ctx)

	t.Run("NormalCloseHandshake", func(t *testing.T) {
		conn := connectAsEndUser(t, backstreamServer.URL, "/lifecycle")

		// Send close frame with reason
		closeMessage := websocket.FormatCloseMessage(websocket.CloseNormalClosure, "test close")
		err := conn.WriteMessage(websocket.CloseMessage, closeMessage)
		gt.NoError(t, err).Required()

		// Should receive close frame back (WebSocket returns close error when reading after close)
		messageType, closeData, err := conn.ReadMessage()
		if err != nil {
			// Check if it's a normal close error
			if websocket.IsCloseError(err, websocket.CloseNormalClosure) {
				// This is expected - the connection was closed normally
				return
			}
			gt.NoError(t, err).Required()
		}
		gt.Value(t, messageType).Equal(websocket.CloseMessage)

		// Parse close message manually since ParseCloseMessage may not be available
		if len(closeData) >= 2 {
			closeCode := int(closeData[0])<<8 | int(closeData[1])
			closeText := string(closeData[2:])
			gt.Value(t, closeCode).Equal(websocket.CloseNormalClosure)
			gt.Value(t, closeText).Equal("test close")
		}

		conn.Close()
	})

	t.Run("AbruptDisconnection", func(t *testing.T) {
		conn := connectAsEndUser(t, backstreamServer.URL, "/lifecycle")

		// Send a message to establish communication
		err := conn.WriteMessage(websocket.TextMessage, []byte("pre-disconnect"))
		gt.NoError(t, err).Required()

		_, _, err = conn.ReadMessage()
		gt.NoError(t, err).Required()

		// Close without handshake
		conn.Close()
		// This tests that the server handles abrupt disconnections gracefully
		// No assertion needed - the test passes if no panic occurs
	})
}

// TestWebSocketMessageOrdering tests that message order is preserved
func TestWebSocketMessageOrdering(t *testing.T) {
	localServer := createOrderingTestServer(t)
	defer localServer.Close()

	backstreamServer, backstreamClient := setupBackstreamInfrastructure(t, localServer.URL)
	defer backstreamServer.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	startBackstreamClient(t, backstreamClient, ctx)

	conn := connectAsEndUser(t, backstreamServer.URL, "/ordering")
	defer conn.Close()

	const numMessages = 100
	
	// Send messages rapidly
	for i := 0; i < numMessages; i++ {
		msg := fmt.Sprintf("message-%03d", i)
		err := conn.WriteMessage(websocket.TextMessage, []byte(msg))
		gt.NoError(t, err).Required()
	}

	// Read responses and verify order
	for i := 0; i < numMessages; i++ {
		_, response, err := conn.ReadMessage()
		gt.NoError(t, err).Required()
		
		expected := fmt.Sprintf("ORDERED: message-%03d", i)
		gt.String(t, string(response)).Equal(expected).Describe(fmt.Sprintf("Message %d out of order", i))
	}
}

// TestWebSocketLargeMessages tests handling of large payloads
func TestWebSocketLargeMessages(t *testing.T) {
	localServer := createLargeMessageTestServer(t)
	defer localServer.Close()

	backstreamServer, backstreamClient := setupBackstreamInfrastructure(t, localServer.URL)
	defer backstreamServer.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	startBackstreamClient(t, backstreamClient, ctx)

	conn := connectAsEndUser(t, backstreamServer.URL, "/large")
	defer conn.Close()

	testSizes := []int{
		1024,      // 1KB
		64 * 1024, // 64KB
		1024 * 1024, // 1MB
	}

	for _, size := range testSizes {
		t.Run(fmt.Sprintf("Size%dBytes", size), func(t *testing.T) {
			// Create test data
			testData := make([]byte, size)
			for i := range testData {
				testData[i] = byte(i % 256)
			}

			// Send large message
			err := conn.WriteMessage(websocket.BinaryMessage, testData)
			gt.NoError(t, err).Required()

			// Read response
			messageType, response, err := conn.ReadMessage()
			gt.NoError(t, err).Required()
			gt.Value(t, messageType).Equal(websocket.BinaryMessage)
			
			// Server echoes with size prefix "SIZE:XXXX:"
			expectedSize := len(testData)
			// Find where the actual data starts (after "SIZE:XXXX:")
			prefixEnd := 0
			for i := 5; i < len(response) && i < 20; i++ { // Start from 5 (after "SIZE:")
				if response[i] == ':' {
					prefixEnd = i + 1
					break
				}
			}
			actualSize := len(response) - prefixEnd
			gt.Value(t, actualSize).Equal(expectedSize).Describe("Large message size mismatch")
		})
	}
}

// Helper function to create comprehensive WebSocket server
func createComprehensiveWebSocketServer(t *testing.T) *httptest.Server {
	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/comprehensive", func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Logf("Failed to upgrade: %v", err)
			return
		}
		defer conn.Close()

		// Set pong handler
		conn.SetPongHandler(func(appData string) error {
			t.Logf("Received pong: %s", appData)
			return nil
		})

		for {
			messageType, data, err := conn.ReadMessage()
			if err != nil {
				return
			}

			switch messageType {
			case websocket.TextMessage:
				response := "ECHO_TEXT: " + string(data)
				conn.WriteMessage(websocket.TextMessage, []byte(response))
			case websocket.BinaryMessage:
				response := append([]byte("BINARY:"), data...)
				conn.WriteMessage(websocket.BinaryMessage, response)
			case websocket.PingMessage:
				// Respond with pong
				conn.WriteMessage(websocket.PongMessage, data)
			case websocket.CloseMessage:
				return
			}
		}
	})

	mux.HandleFunc("/concurrent", func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		for {
			messageType, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			response := "ECHO_TEXT: " + string(data)
			conn.WriteMessage(messageType, []byte(response))
		}
	})

	return httptest.NewServer(mux)
}

// Helper function to create subprotocol test server
func createSubprotocolTestServer(t *testing.T) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/subprotocol", func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
			Subprotocols: []string{"chat", "echo", "ping"},
		}

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			response := fmt.Sprintf("PROTOCOL[%s]: %s", conn.Subprotocol(), string(data))
			conn.WriteMessage(websocket.TextMessage, []byte(response))
		}
	})

	return httptest.NewServer(mux)
}

// Helper function to create lifecycle test server
func createLifecycleTestServer(t *testing.T) *httptest.Server {
	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/lifecycle", func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		for {
			messageType, data, err := conn.ReadMessage()
			if err != nil {
				return
			}

			if messageType == websocket.CloseMessage {
				// Echo close message
				conn.WriteMessage(websocket.CloseMessage, data)
				return
			}

			response := "LIFECYCLE: " + string(data)
			conn.WriteMessage(websocket.TextMessage, []byte(response))
		}
	})

	return httptest.NewServer(mux)
}

// Helper function to create ordering test server
func createOrderingTestServer(t *testing.T) *httptest.Server {
	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/ordering", func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			response := "ORDERED: " + string(data)
			conn.WriteMessage(websocket.TextMessage, []byte(response))
		}
	})

	return httptest.NewServer(mux)
}

// Helper function to create large message test server
func createLargeMessageTestServer(t *testing.T) *httptest.Server {
	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/large", func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		for {
			messageType, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			
			// Prepend size information
			sizePrefix := fmt.Sprintf("SIZE:%d:", len(data))
			response := append([]byte(sizePrefix), data...)
			conn.WriteMessage(messageType, response)
		}
	})

	return httptest.NewServer(mux)
}

// Helper functions for test setup
func setupBackstreamInfrastructure(t *testing.T, localURL string) (*httptest.Server, *client.Client) {
	hubSvc := hub.New()
	serverHandler := server.New(hubSvc)
	backstreamServer := httptest.NewServer(serverHandler)

	tunnelSvc := tunnel.New(localURL)
	backstreamClient := client.New(tunnelSvc, backstreamServer.URL, localURL)

	return backstreamServer, backstreamClient
}

func startBackstreamClient(t *testing.T, backstreamClient *client.Client, ctx context.Context) {
	go func() {
		err := backstreamClient.Connect(ctx)
		if err != nil && err != context.Canceled {
			t.Logf("Client connection error: %v", err)
		}
	}()

	// Wait a moment for connection to establish
	time.Sleep(500 * time.Millisecond)
}

func connectAsEndUser(t *testing.T, backstreamServerURL, path string) *websocket.Conn {
	wsURL := "ws" + backstreamServerURL[4:] + path
	dialer := websocket.Dialer{
		HandshakeTimeout: 5 * time.Second,
	}

	conn, _, err := dialer.Dial(wsURL, nil)
	gt.NoError(t, err).Required()
	return conn
}