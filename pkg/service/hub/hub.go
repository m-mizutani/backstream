package hub

import (
	"errors"
	"sync"

	"github.com/m-mizutani/backstream/pkg/model"
	"github.com/m-mizutani/backstream/pkg/utils/logging"
)

type Service struct {
	reqCh      map[string]chan *model.Request
	reqChMutex sync.Mutex

	respCh      map[string]chan *model.Response
	respChMutex sync.Mutex
}

func New() *Service {
	return &Service{
		reqCh:  make(map[string]chan *model.Request),
		respCh: make(map[string]chan *model.Response),
	}
}

// Join waits for a request from HTTP server.
// This function should be called by WebSocket server.
func (x *Service) Join(clientID string) chan *model.Request {
	x.reqChMutex.Lock()
	defer x.reqChMutex.Unlock()

	ch := make(chan *model.Request)
	x.reqCh[clientID] = ch
	return ch
}

// Leave removes a request channel.
// This function should be called by WebSocket server.
func (x *Service) Leave(clientID string) {
	x.reqChMutex.Lock()
	defer x.reqChMutex.Unlock()

	if ch, ok := x.reqCh[clientID]; ok {
		close(ch)
		delete(x.reqCh, clientID)
	}
}

// PutResponse puts a response to the response channel.
// This function should be called by WebSocket server.
func (x *Service) PutResponse(resp *model.Response) {
	x.respChMutex.Lock()
	defer x.respChMutex.Unlock()

	if ch, ok := x.respCh[resp.ID]; ok {
		ch <- resp
		close(ch)
		delete(x.respCh, resp.ID)
	}
}

// EmitAndWait emits a request and wait for the response.
// This function should be called by HTTP server.
func (x *Service) EmitAndWait(req *model.Request) *model.Response {
	respCh := x.joinRespCh(req.ID)

	if err := x.broadcast(req); err != nil {
		return nil
	}

	return <-respCh
}

var errNoClient = errors.New("no client")

func (x *Service) broadcast(req *model.Request) error {
	x.reqChMutex.Lock()
	defer x.reqChMutex.Unlock()

	if len(x.reqCh) == 0 {
		return errNoClient
	}

	for _, ch := range x.reqCh {
		ch <- req
	}
	logging.Default().Debug("broadcasted request", "id", req.ID, "count", len(x.reqCh))
	return nil
}

func (x *Service) joinRespCh(id string) chan *model.Response {
	x.respChMutex.Lock()
	defer x.respChMutex.Unlock()

	ch := make(chan *model.Response)
	x.respCh[id] = ch

	logging.Default().Debug("joined response channel", "id", id)

	return ch
}
