package model_test

import (
	"context"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/m-mizutani/backstream/pkg/model"
	"github.com/m-mizutani/gt"
)

func TestNewRequestWithQueryString(t *testing.T) {
	// Create an HTTP request with query parameters
	req := httptest.NewRequest("GET", "/search?q=test&page=1", nil)

	// Create a model.Request from the HTTP request
	modelReq, err := model.NewRequest(req)
	gt.NoError(t, err).Required()

	// Parse the path to verify query parameters are preserved
	parsedURL, err := url.Parse(modelReq.Path)
	gt.NoError(t, err).Required()
	
	gt.Value(t, parsedURL.Path).Equal("/search")
	gt.Value(t, parsedURL.Query().Get("q")).Equal("test")
	gt.Value(t, parsedURL.Query().Get("page")).Equal("1")
}

func TestNewRequestWithoutQueryString(t *testing.T) {
	// Create an HTTP request without query parameters
	req := httptest.NewRequest("GET", "/users", nil)

	// Create a model.Request from the HTTP request
	modelReq, err := model.NewRequest(req)
	gt.NoError(t, err).Required()

	// The path should be just the path
	gt.Value(t, modelReq.Path).Equal("/users")
}

func TestNewHTTPRequestWithQueryString(t *testing.T) {
	// Create a model.Request with query parameters in the path
	modelReq := &model.Request{
		ID:     "test-123",
		Path:   "/search?q=test&page=1",
		Method: "GET",
		Header: make(map[string]string),
	}

	// Convert to HTTP request
	httpReq, err := modelReq.NewHTTPRequest(context.Background(), "http://localhost:8080")
	gt.NoError(t, err).Required()

	// Check the full URL includes query parameters
	gt.Value(t, httpReq.URL.String()).Equal("http://localhost:8080/search?q=test&page=1")
	gt.Value(t, httpReq.URL.Path).Equal("/search")
	gt.Value(t, httpReq.URL.RawQuery).Equal("q=test&page=1")

	// Check individual query parameters
	gt.Value(t, httpReq.URL.Query().Get("q")).Equal("test")
	gt.Value(t, httpReq.URL.Query().Get("page")).Equal("1")
}

func TestNewHTTPRequestWithComplexQueryString(t *testing.T) {
	// Create a model.Request with complex query parameters
	modelReq := &model.Request{
		ID:     "test-456",
		Path:   "/api/items?category=books&sort=price&order=asc&filter=new",
		Method: "GET",
		Header: make(map[string]string),
	}

	// Convert to HTTP request
	httpReq, err := modelReq.NewHTTPRequest(context.Background(), "https://api.example.com")
	gt.NoError(t, err).Required()

	// Check the full URL includes all query parameters
	gt.Value(t, httpReq.URL.String()).Equal("https://api.example.com/api/items?category=books&sort=price&order=asc&filter=new")

	// Check individual query parameters
	query := httpReq.URL.Query()
	gt.Value(t, query.Get("category")).Equal("books")
	gt.Value(t, query.Get("sort")).Equal("price")
	gt.Value(t, query.Get("order")).Equal("asc")
	gt.Value(t, query.Get("filter")).Equal("new")
}

func TestNewHTTPRequestWithEncodedQueryString(t *testing.T) {
	// Create a model.Request with URL-encoded query parameters
	modelReq := &model.Request{
		ID:     "test-789",
		Path:   "/search?q=hello%20world&tag=foo%26bar",
		Method: "GET",
		Header: make(map[string]string),
	}

	// Convert to HTTP request
	httpReq, err := modelReq.NewHTTPRequest(context.Background(), "http://localhost:3000")
	gt.NoError(t, err).Required()

	// Check the query parameters are properly decoded
	query := httpReq.URL.Query()
	gt.Value(t, query.Get("q")).Equal("hello world")
	gt.Value(t, query.Get("tag")).Equal("foo&bar")
}
