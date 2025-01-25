package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"os/signal"

	"github.com/gorilla/websocket"
	"github.com/m-mizutani/backstream/pkg/model"
	"github.com/m-mizutani/backstream/pkg/service/tunnel"
	"github.com/m-mizutani/backstream/pkg/utils/logging"
	"github.com/m-mizutani/goerr/v2"
)

type Client struct {
	svc    *tunnel.Service
	srcURL string
}

func New(svc *tunnel.Service, src string) *Client {
	return &Client{
		svc:    svc,
		srcURL: src,
	}
}

func (x *Client) Connect(ctx context.Context) error {
	logger := logging.Extract(ctx)

	wsURL, err := convertToWebSocketURL(x.srcURL)
	if err != nil {
		return goerr.Wrap(err, "failed to convert URL")
	}

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, http.Header{
		"Backstream-Client": []string{"default"},
	})

	if err != nil {
		return goerr.Wrap(err, "failed to connect")
	}
	defer conn.Close()

	logger.Info("connected to server", "url", wsURL)

	errCh := make(chan error)
	go func() {
		defer close(errCh)

		for {
			logger.Info("waiting for message")
			_, message, err := conn.ReadMessage()
			if err != nil {
				logging.Default().Error("failed to read message", "error", err)
				return
			}

			var req model.Request
			if err := json.Unmarshal(message, &req); err != nil {
				errCh <- goerr.Wrap(err, "failed to unmarshal message")
				return
			}
			logger.Info("received message", "id", req.ID, "path", req.Path, "method", req.Method)

			resp, err := x.svc.ToLocal(ctx, &req)
			if err != nil {
				errCh <- goerr.Wrap(err, "failed to handle local request")
				return
			}

			respBody, err := json.Marshal(resp)
			if err != nil {
				errCh <- goerr.Wrap(err, "failed to marshal response")
				return
			}

			logger.Info("sending response", "id", resp.ID, "code", resp.Code)
			if err := conn.WriteMessage(websocket.TextMessage, respBody); err != nil {
				errCh <- goerr.Wrap(err, "failed to write response")
				return
			}
		}
	}()

	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)

	select {
	case <-interrupt:
		logger.Info("Quit signal received")

	case err := <-errCh:
		if err != nil {
			return goerr.Wrap(err, "failed to read message")
		}
	}

	return nil

	/*
		if err := conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "")); err != nil {
			return goerr.Wrap(err, "failed to write close message")
		}
		<-done
	*/
}

func convertToWebSocketURL(rawURL string) (string, error) {
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return "", goerr.Wrap(err, "failed to parse URL")
	}

	switch parsedURL.Scheme {
	case "http":
		parsedURL.Scheme = "ws"
	case "https":
		parsedURL.Scheme = "wss"
	default:
		return "", goerr.New("unsupported scheme", goerr.V("scheme", parsedURL.Scheme))
	}

	return parsedURL.String(), nil
}
