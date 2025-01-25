package model

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"

	"github.com/google/uuid"
	"github.com/m-mizutani/goerr/v2"
)

type Request struct {
	ID     string              `json:"id"`
	Path   string              `json:"path"`
	Method string              `json:"method"`
	Body   []byte              `json:"body"`
	Header map[string][]string `json:"header"`
}

func (x *Request) NewHTTPRequest(ctx context.Context, dst string) (*http.Request, error) {
	baseURL, err := url.Parse(dst)
	if err != nil {
		return nil, goerr.Wrap(err, "failed to parse URL", goerr.V("dst", dst))
	}

	baseURL.Path = x.Path
	body := io.NopCloser(bytes.NewReader(x.Body))

	req, err := http.NewRequestWithContext(ctx, x.Method, baseURL.String(), body)
	if err != nil {
		return nil, goerr.Wrap(err, "failed to create http.Request")
	}

	req.Header = x.Header
	return req, nil
}

func NewRequest(r *http.Request) (*Request, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, goerr.Wrap(err, "Failed to read request body")
	}

	return &Request{
		ID:     uuid.New().String(),
		Path:   r.URL.Path,
		Method: r.Method,
		Body:   body,
		Header: r.Header,
	}, nil
}

type Response struct {
	ID     string              `json:"id"`
	Code   int                 `json:"code"`
	Body   []byte              `json:"body"`
	Header map[string][]string `json:"header"`
}

func (x *Request) NewResponse(r *http.Response) (*Response, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, goerr.Wrap(err, "Failed to read response body")
	}

	return &Response{
		ID:     x.ID,
		Code:   r.StatusCode,
		Body:   body,
		Header: r.Header,
	}, nil
}
