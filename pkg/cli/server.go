package cli

import (
	"context"
	"net/http"
	"time"

	"github.com/m-mizutani/backstream/pkg/controller/server"
	"github.com/m-mizutani/backstream/pkg/service/hub"
	"github.com/m-mizutani/backstream/pkg/utils/logging"
	"github.com/m-mizutani/goerr/v2"
	"github.com/m-mizutani/opaq"
	"github.com/urfave/cli/v3"
)

func cmdServer() *cli.Command {
	var (
		addr       string
		policyPath string
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
			&cli.StringFlag{
				Name:        "policy",
				Aliases:     []string{"p"},
				Value:       "./policies",
				Usage:       "Directory or file path of auth policy in Rego",
				Destination: &policyPath,
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			var serverOptions []server.Option
			if policyPath != "" {
				policy, err := opaq.New(opaq.Files(policyPath))
				if err != nil {
					return goerr.Wrap(err, "failed to create policy", goerr.V("policy_path", policyPath))
				}
				serverOptions = append(serverOptions, server.WithPolicy(policy))
			}

			svc := hub.New()
			s := server.New(svc, serverOptions...)

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
