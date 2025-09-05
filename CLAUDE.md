# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Backstream is a self-hosted reverse proxy written in Go that exposes local applications to the internet. It consists of a server component that runs on a public-facing server and a client component that connects from the local machine to tunnel HTTP traffic.

## Key Commands

### Build
```bash
go build ./...
```

### Run Tests
```bash
go test ./...
```

### Run Specific Test
```bash
go test -v -run TestName ./pkg/...
```

### Run Server
```bash
go run . server -a localhost:8080
# Or with policies
go run . server -a localhost:8080 -p path/to/policies
```

### Run Client
```bash
go run . client -s https://server-url -d http://localhost:8080
# Or with authentication header
go run . client -s https://server-url -d http://localhost:8080 -H "Authorization: Bearer token"
```

### Run Short Command (bs)
```bash
go run ./cmd/bs client -s https://server-url -d http://localhost:8080
```

## Architecture

### Core Components

1. **CLI Entry Points** (`main.go`, `cmd/bs/main.go`)
   - Both entry points call `pkg/cli.Run()` to start the application
   - Command structure defined in `pkg/cli/cli.go`

2. **Server Component** (`pkg/controller/server/`)
   - Handles WebSocket connections from clients
   - Routes HTTP requests to connected clients
   - Manages authentication via Rego policies

3. **Client Component** (`pkg/controller/client/`)
   - Establishes WebSocket connection to server
   - Tunnels HTTP requests to local destinations
   - Handles request/response forwarding

4. **Hub Service** (`pkg/service/hub/`)
   - Manages client connections on the server side
   - Routes requests to appropriate clients

5. **Tunnel Service** (`pkg/service/tunnel/`)
   - Handles HTTP request forwarding on client side
   - Makes actual HTTP requests to local services

### Authentication System

Backstream uses [Open Policy Agent (OPA)](https://www.openpolicyagent.org/) Rego policies for authentication:

- **`auth.client` package**: Validates WebSocket connections from backstream clients
- **`auth.server` package**: Validates HTTP requests from external services/browsers
- Policies loaded from directories/files specified with `-p` flag
- Default behavior is `allow = false` when policies are specified

### Message Protocol

Communication between client and server uses a custom protocol defined in `pkg/model/message.go`:
- Request/Response messages for HTTP tunneling
- WebSocket as transport layer
- JSON encoding for message serialization

## Key Dependencies

- `github.com/gorilla/websocket` - WebSocket implementation
- `github.com/m-mizutani/opaq` - OPA integration for Rego policies
- `github.com/urfave/cli/v3` - CLI framework
- `github.com/m-mizutani/clog` - Structured logging
- `github.com/m-mizutani/goerr/v2` - Error handling with stack traces

## Testing Approach

- Unit tests located alongside implementation files (`*_test.go`)
- Primary test coverage in `pkg/controller/server/http_test.go`
- Tests use standard Go testing package with `github.com/stretchr/testify` for assertions
- No separate test scripts or Makefile - use `go test` directly

## Environment Variables

Server mode:
- `BACKSTREAM_ADDR` - Listen address (default: localhost:8080)
- `BACKSTREAM_POLICY` - Policy paths
- `BACKSTREAM_NO_CLIENT_CODE` - HTTP status when no client connected (default: 503)
- `BACKSTREAM_LOG_FORMAT` - Log format (json/text)
- `BACKSTREAM_LOG_LEVEL` - Log level

Client mode:
- `BACKSTREAM_SRC_URL` - Server URL
- `BACKSTREAM_DST_URL` - Local destination URL
- `BACKSTREAM_HEADER` - Authentication headers