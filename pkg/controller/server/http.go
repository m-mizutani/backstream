package server

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/m-mizutani/backstream/pkg/model"
	"github.com/m-mizutani/backstream/pkg/service/hub"
	"github.com/m-mizutani/backstream/pkg/utils/logging"
	"github.com/m-mizutani/goerr/v2"
	"github.com/m-mizutani/opaq"
)

type Upgrade func(w http.ResponseWriter, r *http.Request, responseHeader http.Header) (*websocket.Conn, error)

type Server struct {
	svc     *hub.Service
	upgrade Upgrade
	policy  *opaq.Client
}

func New(svc *hub.Service, opts ...Option) *Server {
	var upgrade = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return true
		},
	}

	x := &Server{
		svc:     svc,
		upgrade: upgrade.Upgrade,
	}

	for _, opt := range opts {
		opt(x)
	}

	return x
}

type Option func(*Server)

func WithPolicy(policy *opaq.Client) Option {
	return func(x *Server) {
		x.policy = policy
	}
}

func WithUpgrade(upgrade Upgrade) Option {
	return func(x *Server) {
		x.upgrade = upgrade
	}
}

func (x *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Backstream-Client") != "" {
		x.handleWebSocket(w, r)
	} else {
		x.handleHTTP(w, r)
	}
}

func (x *Server) handleHTTP(w http.ResponseWriter, r *http.Request) {
	logger := logging.Extract(r.Context())

	if x.policy != nil {
		if err := checkAuthPolicy(x.policy, r, "data.auth.server"); err != nil {
			logger.Error("auth policy failed", "error", err)
			http.Error(w, "auth policy denied", http.StatusForbidden)
		}
	}

	req, err := model.NewRequest(r)
	if err != nil {
		logging.Extract(r.Context()).Error("failed to create request", "error", err)
		http.Error(w, "failed to create request", http.StatusBadRequest)
		return
	}

	logger.Debug("received HTTP request", "request", req)

	resp := x.svc.EmitAndWait(req)
	if resp == nil {
		logging.Extract(r.Context()).Error("failed to get response")
		http.Error(w, "failed to get response", http.StatusInternalServerError)
		return
	}

	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(resp.Code)
	_, _ = w.Write(resp.Body)

	logger.Info("sent HTTP response", "id", resp.ID, "method", r.Method, "url", r.URL, "code", resp.Code)
}

type AuthPolicyInput struct {
	Method string            `json:"method"`
	Path   string            `json:"path"`
	Header map[string]string `json:"header"`
	Remote string            `json:"remote"`
}

type AuthPolicyOutput struct {
	Allow bool `json:"allow"`
}

func checkAuthPolicy(policy *opaq.Client, r *http.Request, query string) error {
	input := AuthPolicyInput{
		Method: r.Method,
		Path:   r.URL.Path,
		Header: make(map[string]string),
		Remote: r.RemoteAddr,
	}
	for k, v := range r.Header {
		input.Header[k] = v[0]
	}

	var output AuthPolicyOutput
	if err := policy.Query(r.Context(), query, input, &output); err != nil {
		return err
	}

	if !output.Allow {
		return goerr.New("auth denied", goerr.T(model.ErrAuthDenied))
	}

	return nil
}

func (x *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	logger := logging.Extract(r.Context())

	if x.policy != nil {
		if err := checkAuthPolicy(x.policy, r, "data.auth.client"); err != nil {
			logger.Error("auth policy failed", "error", err)
			if goerr.HasTag(err, model.ErrAuthDenied) {
				http.Error(w, "auth policy denied", http.StatusForbidden)
			} else {
				http.Error(w, "failed auth policy evaluation", http.StatusInternalServerError)
			}
			return
		}
	}

	ws, err := x.upgrade(w, r, nil)
	if err != nil {
		logger.Error("failed to upgrade", "error", err)
		http.Error(w, "failed to upgrade: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if ws == nil {
		logger.Error("failed to upgrade")
		http.Error(w, "failed to upgrade", http.StatusInternalServerError)
		return
	}
	defer ws.Close()
	logger.Info("connected to WebSocket server", "remote", ws.RemoteAddr())

	clientID := uuid.New().String()
	reqCh := x.svc.Join(clientID)
	defer x.svc.Leave(clientID)

	respCh := make(chan *model.Response)
	errCh := make(chan error)
	go func() {
		defer close(respCh)
		defer close(errCh)

		for {
			_, message, err := ws.ReadMessage()
			if err != nil {
				errCh <- err
				return
			}

			var resp model.Response
			if err := json.Unmarshal(message, &resp); err != nil {
				errCh <- err
				return
			}

			logger.Debug("received message", slog.Group("request",
				slog.Any("id", resp.ID),
				slog.Any("code", resp.Code),
				slog.Any("header", resp.Header),
				slog.Any("body", string(resp.Body)),
			))

			x.svc.PutResponse(&resp)
		}
	}()

	for {
		select {
		case req := <-reqCh:
			message, err := json.Marshal(req)
			if err != nil {
				logger.Error("failed to marshal message", "error", err)
				return
			}

			if err := ws.WriteMessage(websocket.TextMessage, message); err != nil {
				logger.Error("failed to write message", "error", err)
				return
			}
			logger.Info("sent message", "id", req.ID, "method", req.Method, "path", req.Path)

		case resp := <-respCh:
			x.svc.PutResponse(resp)

		case err := <-errCh:
			logger.Error("failed to read message", "error", err)
			return
		}
	}
}
