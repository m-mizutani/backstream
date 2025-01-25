package tunnel

import (
	"context"
	"net/http"

	"github.com/m-mizutani/backstream/pkg/interfaces"
	"github.com/m-mizutani/backstream/pkg/model"
)

type Service struct {
	dst        string
	httpClient interfaces.HTTPClient
}

type Option func(*Service)

func WithHTTPClient(httpClient interfaces.HTTPClient) Option {
	return func(x *Service) {
		x.httpClient = httpClient
	}
}

func New(dst string, opts ...Option) *Service {
	x := &Service{
		dst:        dst,
		httpClient: http.DefaultClient,
	}

	for _, opt := range opts {
		opt(x)
	}

	return x
}

func (x *Service) ToLocal(ctx context.Context, req *model.Request) (*model.Response, error) {
	httpReq, err := req.NewHTTPRequest(ctx, x.dst)
	if err != nil {
		return nil, err
	}

	httpResp, err := x.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}

	return req.NewResponse(httpResp)
}
