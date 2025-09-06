package model

import (
	"bytes"
	"context"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"

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

	// Parse the path which may include query parameters
	if x.Path != "" {
		// Parse the path+query from the request
		parsedPath, err := url.Parse(x.Path)
		if err != nil {
			return nil, goerr.Wrap(err, "failed to parse request path", goerr.V("path", x.Path))
		}

		// Set path and query separately
		baseURL.Path = parsedPath.Path
		baseURL.RawQuery = parsedPath.RawQuery
	}

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
	// Debug logging for production issue
	// TODO: Remove after fixing query string issue
	if r.URL.RawQuery != "" || strings.Contains(r.URL.Path, "auth") {
		log.Printf("[DEBUG] NewRequest: URL.Path=%s, URL.RawQuery=%s, URL.String=%s, RequestURI=%s",
			r.URL.Path, r.URL.RawQuery, r.URL.String(), r.RequestURI)
	}
	
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, goerr.Wrap(err, "Failed to read request body")
	}

	header := make(map[string]string)
	for k, v := range r.Header {
		header[k] = v[0]
	}

	// Include both path and query string
	path := r.URL.Path
	if r.URL.RawQuery != "" {
		path = path + "?" + r.URL.RawQuery
	}

	return &Request{
		ID:     uuid.New().String(),
		Path:   path,
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
