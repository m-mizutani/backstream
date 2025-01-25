package client

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/signal"

	"github.com/gorilla/websocket"
	"github.com/m-mizutani/backstream/pkg/utils/logging"
	"github.com/m-mizutani/goerr/v2"
)

type Client struct {
	url string
}

func New(url string) *Client {
	return &Client{
		url: url,
	}
}

func (x *Client) Connect(ctx context.Context) error {
	wsURL, err := convertToWebSocketURL(x.url)
	if err != nil {
		return goerr.Wrap(err, "failed to convert URL")
	}

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		return goerr.Wrap(err, "failed to connect")
	}
	defer conn.Close()

	done := make(chan struct{})

	go func() {
		defer close(done)
		for {
			_, message, err := conn.ReadMessage()
			if err != nil {
				logging.Default().Error("failed to read message", "error", err)
				return
			}
			fmt.Printf("Recv: %s\n", message)
		}
	}()

	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)

	<-interrupt

	fmt.Println("Quit signal received")
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
		return "", err
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
