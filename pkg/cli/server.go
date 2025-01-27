package cli

import (
	"context"
	"net/http"
	"time"

	"github.com/m-mizutani/backstream/pkg/controller/server"
	"github.com/m-mizutani/backstream/pkg/service/hub"
	"github.com/m-mizutani/backstream/pkg/utils/logging"
	"github.com/m-mizutani/goerr/v2"
	"github.com/urfave/cli/v3"
)

func cmdServer() *cli.Command {
	var (
		addr string
	)

	cmd := &cli.Command{
		Name:    "server",
		Aliases: []string{"s", "serve"},
		Usage:   "Start backstream server",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:        "addr",
				Aliases:     []string{"a"},
				Value:       "localhost:8080",
				Usage:       "Listen address",
				Sources:     cli.EnvVars("BACKSTREAM_ADDR"),
				Destination: &addr,
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			svc := hub.New()
			s := server.New(svc)

			logging.Extract(ctx).Info("Start server", "addr", addr)

			server := &http.Server{
				Addr:         addr,
				Handler:      s,
				ReadTimeout:  10 * time.Second,
				WriteTimeout: 30 * time.Second,
			}

			if err := server.ListenAndServe(); err != nil {
				return goerr.Wrap(err, "failed to listen and serve")
			}

			return nil
		},
	}

	return cmd
}
