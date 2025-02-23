package server_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/m-mizutani/backstream/pkg/controller/server"
	"github.com/m-mizutani/backstream/pkg/service/hub"
	"github.com/m-mizutani/gt"
	"github.com/m-mizutani/opaq"
)

func TestServer_HandleWebSocket_Auth(t *testing.T) {
	policy, err := opaq.New(opaq.Files("testdata/policy/auth_client.rego"))
	if err != nil {
		t.Fatalf("failed to create policy: %v", err)
	}

	server := server.New(hub.New(), server.WithPolicy(policy), server.WithUpgrade(func(w http.ResponseWriter, r *http.Request, responseHeader http.Header) (*websocket.Conn, error) {
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
				return r
			},
			expectCode: http.StatusInternalServerError, // auth passed, but upgrade failed
		},
		"deny": {
			req: func() *http.Request {
				r := httptest.NewRequest("GET", "/", nil)
				r.Header.Add("Authorization", "Bearer invalid_token")
				return r
			},
			expectCode: http.StatusForbidden,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			r := tc.req()
			w := httptest.NewRecorder()
			server.HandleWebsocket(w, r)

			resp := w.Result()
			gt.Equal(t, resp.StatusCode, tc.expectCode)
		})
	}
}
