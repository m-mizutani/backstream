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
	ID     string            `json:"id"`
	Path   string            `json:"path"`
	Method string            `json:"method"`
	Body   []byte            `json:"body"`
	Remote string            `json:"remote"`
	Header map[string]string `json:"header"`
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

	for k, v := range x.Header {
		req.Header.Add(k, v)
	}

	return req, nil
}

func NewRequest(r *http.Request) (*Request, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, goerr.Wrap(err, "Failed to read request body")
	}

	header := make(map[string]string)
	for k, v := range r.Header {
		header[k] = v[0]
	}

	return &Request{
		ID:     uuid.New().String(),
		Path:   r.URL.Path,
		Method: r.Method,
		Body:   body,
		Remote: r.RemoteAddr,
		Header: header,
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
