package server

import (
	"net/http"
	"time"

	"github.com/gorilla/websocket"
	"github.com/m-mizutani/backstream/pkg/utils/logging"
)

type Server struct {
	handler http.Handler
	upgrade websocket.Upgrader
}

func New() *Server {
	var upgrade = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return true
		},
	}

	return &Server{
		upgrade: upgrade,
	}
}

func (x *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	logger := logging.Extract(r.Context())
	ws, err := x.upgrade.Upgrade(w, r, nil)
	if err != nil {
		logger.Error("failed to upgrade", "error", err)
		return
	}
	defer ws.Close()

	for {
		message := []byte("Event!")
		if err := ws.WriteMessage(websocket.TextMessage, message); err != nil {
			logger.Error("failed to write message", "error", err)
			break
		}
		time.Sleep(1 * time.Second)
	}
}
