package server

import "net/http"

func (x *Server) HandleWebsocket(w http.ResponseWriter, r *http.Request) {
	x.handleWebSocket(w, r)
}
