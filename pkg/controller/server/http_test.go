package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/m-mizutani/backstream/pkg/service/hub"
	"github.com/m-mizutani/opaq"
	"github.com/stretchr/testify/assert"
)

func TestServer_WebSocket_Auth(t *testing.T) {
	policy, err := opaq.New(opaq.Files("testdata/policy/auth_client.rego"))
	if err != nil {
		t.Fatalf("failed to create policy: %v", err)
	}

	server := New(hub.New(), WithPolicy(policy), WithUpgrade(func(w http.ResponseWriter, r *http.Request, responseHeader http.Header) (*websocket.Conn, error) {
		return nil, nil
	}))

	testCases := map[string]struct {
		req        func() *http.Request
		expectCode int
	}{
		"allow": {
			req: func() *http.Request {
				r := httptest.NewRequest("GET", "/", nil)
				r.Header.Add("Authorization", "Bearer valid_token")
				r.Header.Add("Backstream-Client", "test") // WebSocketハンドラを呼び出すために必要
				return r
			},
			expectCode: http.StatusInternalServerError, // auth passed, but upgrade failed
		},
		"deny": {
			req: func() *http.Request {
				r := httptest.NewRequest("GET", "/", nil)
				r.Header.Add("Authorization", "Bearer invalid_token")
				r.Header.Add("Backstream-Client", "test") // WebSocketハンドラを呼び出すために必要
				return r
			},
			expectCode: http.StatusForbidden,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			r := tc.req()
			w := httptest.NewRecorder()
			server.ServeHTTP(w, r) // ServeHTTPを使用して内部でhandleWebSocketを呼び出す

			resp := w.Result()
			assert.Equal(t, tc.expectCode, resp.StatusCode)
		})
	}
}

func TestWithNoClientCode(t *testing.T) {
	testCases := []struct {
		name         string
		noClientCode int64
		expectedCode int
		expectedBody string
	}{
		{
			name:         "Default 503 response",
			noClientCode: 503,
			expectedCode: 503,
			expectedBody: "no WebSocket client connected\n",
		},
		{
			name:         "Custom error code (400) response",
			noClientCode: 400,
			expectedCode: 400,
			expectedBody: "no WebSocket client connected\n",
		},
		{
			name:         "Success code (200) response",
			noClientCode: 200,
			expectedCode: 200,
			expectedBody: "no WebSocket client connected",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Setup server
			svc := hub.New()
			srv := New(svc, WithNoClientCode(tc.noClientCode))

			// Create test request
			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			w := httptest.NewRecorder()

			// Execute request
			srv.ServeHTTP(w, req)

			// Assertions
			assert.Equal(t, tc.expectedCode, w.Code)
			assert.Equal(t, tc.expectedBody, w.Body.String())
		})
	}
}
