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

func TestHTTPProxy(t *testing.T) {
	// 1. Start local HTTP server
	mux := http.NewServeMux()
	mux.HandleFunc("/test", func(w http.ResponseWriter, r *http.Request) {
		t.Logf("Local server received request: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Hello from local server"))
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
	
	clientDone := make(chan error, 1)
	go func() {
		t.Logf("Starting backstream client...")
		err := backstreamClient.Connect(ctx)
		t.Logf("Backstream client finished with error: %v", err)
		clientDone <- err
	}()
	
	// Wait for client to connect
	time.Sleep(500 * time.Millisecond)
	
	// 4. Make HTTP request through backstream
	t.Logf("Making HTTP request to: %s/test", backstreamServer.URL)
	
	httpClient := &http.Client{
		Timeout: 5 * time.Second,
	}
	resp, err := httpClient.Get(backstreamServer.URL + "/test")
	require.NoError(t, err)
	defer resp.Body.Close()
	
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	
	// Clean shutdown
	cancel()
	select {
	case <-clientDone:
		t.Logf("Client shutdown cleanly")
	case <-time.After(2 * time.Second):
		t.Logf("Client shutdown timeout")
	}
}