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
)

type Server struct {
	svc     *hub.Service
	upgrade websocket.Upgrader
}

func New(svc *hub.Service) *Server {
	var upgrade = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return true
		},
	}

	return &Server{
		svc:     svc,
		upgrade: upgrade,
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

func (x *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	logger := logging.Extract(r.Context())
	ws, err := x.upgrade.Upgrade(w, r, nil)
	if err != nil {
		logger.Error("failed to upgrade", "error", err)
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
