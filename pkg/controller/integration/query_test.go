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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHTTPProxyWithQueryString(t *testing.T) {
	// 1. Start local HTTP server that checks query parameters
	mux := http.NewServeMux()
	mux.HandleFunc("/search", func(w http.ResponseWriter, r *http.Request) {
		t.Logf("Local server received request: %s %s", r.Method, r.URL.String())

		// Check query parameters
		query := r.URL.Query()
		q := query.Get("q")
		page := query.Get("page")

		t.Logf("Query params: q=%s, page=%s", q, page)

		if q == "test" && page == "1" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("Query params received correctly"))
		} else {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte("Query params missing or incorrect"))
		}
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

	// 4. Make HTTP request with query string through backstream
	testURL := backstreamServer.URL + "/search?q=test&page=1"
	t.Logf("Making HTTP request to: %s", testURL)

	httpClient := &http.Client{
		Timeout: 5 * time.Second,
	}
	resp, err := httpClient.Get(testURL)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode, "Query parameters should be forwarded correctly")

	// Clean shutdown
	cancel()
	select {
	case <-clientDone:
		t.Logf("Client shutdown cleanly")
	case <-time.After(2 * time.Second):
		t.Logf("Client shutdown timeout")
	}
}
