package model_test

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/m-mizutani/backstream/pkg/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewRequestWithQueryString(t *testing.T) {
	// Create an HTTP request with query parameters
	req := httptest.NewRequest("GET", "/search?q=test&page=1", nil)

	// Create a model.Request from the HTTP request
	modelReq, err := model.NewRequest(req)
	require.NoError(t, err)

	// The path should include the query string
	assert.Equal(t, "/search?q=test&page=1", modelReq.Path)
}

func TestNewRequestWithoutQueryString(t *testing.T) {
	// Create an HTTP request without query parameters
	req := httptest.NewRequest("GET", "/users", nil)

	// Create a model.Request from the HTTP request
	modelReq, err := model.NewRequest(req)
	require.NoError(t, err)

	// The path should be just the path
	assert.Equal(t, "/users", modelReq.Path)
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
	require.NoError(t, err)

	// Check the full URL includes query parameters
	assert.Equal(t, "http://localhost:8080/search?q=test&page=1", httpReq.URL.String())
	assert.Equal(t, "/search", httpReq.URL.Path)
	assert.Equal(t, "q=test&page=1", httpReq.URL.RawQuery)

	// Check individual query parameters
	assert.Equal(t, "test", httpReq.URL.Query().Get("q"))
	assert.Equal(t, "1", httpReq.URL.Query().Get("page"))
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
	require.NoError(t, err)

	// Check the full URL includes all query parameters
	assert.Equal(t, "https://api.example.com/api/items?category=books&sort=price&order=asc&filter=new", httpReq.URL.String())

	// Check individual query parameters
	query := httpReq.URL.Query()
	assert.Equal(t, "books", query.Get("category"))
	assert.Equal(t, "price", query.Get("sort"))
	assert.Equal(t, "asc", query.Get("order"))
	assert.Equal(t, "new", query.Get("filter"))
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
	require.NoError(t, err)

	// Check the query parameters are properly decoded
	query := httpReq.URL.Query()
	assert.Equal(t, "hello world", query.Get("q"))
	assert.Equal(t, "foo&bar", query.Get("tag"))
}
