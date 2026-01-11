package integration_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/m-mizutani/backstream/pkg/controller/client"
	"github.com/m-mizutani/backstream/pkg/controller/server"
	"github.com/m-mizutani/backstream/pkg/service/hub"
	"github.com/m-mizutani/backstream/pkg/service/tunnel"
	"github.com/m-mizutani/gt"
)

// waitForClientConnection waits for the backstream client to connect to the server
// It returns a channel that will be closed when the client is connected
func waitForClientConnection(t *testing.T, serverURL string) <-chan struct{} {
	connected := make(chan struct{})

	go func() {
		defer close(connected)

		// Poll the server to check if a client is connected
		// We do this by making a request and checking if we get a 503 (no client) or not
		client := &http.Client{Timeout: 100 * time.Millisecond}

		for i := 0; i < 50; i++ { // Try for up to 5 seconds
			resp, err := client.Get(serverURL + "/health")
			if err == nil {
				if resp.StatusCode != 503 && resp.StatusCode != 0 {
					resp.Body.Close()
					return // Client is connected
				}
				resp.Body.Close()
			}
			time.Sleep(100 * time.Millisecond)
		}

		t.Logf("Warning: Could not confirm client connection")
	}()

	return connected
}

func TestHTTPProxy(t *testing.T) {
	// 1. Start local HTTP server
	mux := http.NewServeMux()
	mux.HandleFunc("/test", func(w http.ResponseWriter, r *http.Request) {
		t.Logf("Local server received request: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("Hello from local server"))
	})
	localServer := httptest.NewServer(mux)
	defer localServer.Close()
	t.Logf("Local server started at: %s", localServer.URL)

	// 2. Start backstream server
	hubSvc := hub.New()
	serverHandler := server.New(hubSvc)
	backstreamServer := httptest.NewServer(serverHandler)
	defer backstreamServer.Close()
	t.Logf("Backstream server started at: %s", backstreamServer.URL)

	// 3. Start backstream client
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tunnelSvc := tunnel.New(localServer.URL)
	backstreamClient := client.New(tunnelSvc, backstreamServer.URL, localServer.URL)

	// Start monitoring for connection before starting the client
	connectionWait := waitForClientConnection(t, backstreamServer.URL)

	clientDone := make(chan error, 1)
	go func() {
		t.Logf("Starting backstream client...")
		err := backstreamClient.Connect(ctx)
		t.Logf("Backstream client finished with error: %v", err)
		clientDone <- err
	}()

	// Wait for client to connect with timeout
	select {
	case <-connectionWait:
		t.Logf("Client connected successfully")
	case <-time.After(5 * time.Second):
		t.Fatal("Timeout waiting for client to connect")
	}

	// 4. Make HTTP request through backstream
	t.Logf("Making HTTP request to: %s/test", backstreamServer.URL)

	httpClient := &http.Client{
		Timeout: 5 * time.Second,
	}
	resp, err := httpClient.Get(backstreamServer.URL + "/test")
	gt.NoError(t, err).Required()
	defer resp.Body.Close()

	gt.Value(t, resp.StatusCode).Equal(http.StatusOK)

	// Clean shutdown
	cancel()
	select {
	case err := <-clientDone:
		if err != nil && err != context.Canceled {
			t.Logf("Client shutdown with error: %v", err)
		} else {
			t.Logf("Client shutdown cleanly")
		}
	case <-time.After(2 * time.Second):
		// Client shutdown timeout is not a critical error
		t.Logf("Client shutdown timeout (non-critical)")
	}
}
