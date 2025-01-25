package cli

import (
	"context"

	"github.com/m-mizutani/backstream/pkg/controller/client"
	"github.com/m-mizutani/backstream/pkg/service/tunnel"
	"github.com/m-mizutani/goerr/v2"
	"github.com/urfave/cli/v3"
)

func cmdClient() *cli.Command {
	var (
		srcURL string
		dstURL string
	)

	cmd := &cli.Command{
		Name:    "client",
		Aliases: []string{"c"},
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:        "src",
				Aliases:     []string{"s"},
				Usage:       "Source URL",
				Sources:     cli.EnvVars("BACKSTREAM_SRC_URL"),
				Required:    true,
				Destination: &srcURL,
			},
			&cli.StringFlag{
				Name:        "dst",
				Aliases:     []string{"d"},
				Usage:       "Destination URL",
				Sources:     cli.EnvVars("BACKSTREAM_DST_URL"),
				Required:    true,
				Destination: &dstURL,
			},
		},
		Usage: "Start backstream client",

		Action: func(ctx context.Context, cmd *cli.Command) error {
			svc := tunnel.New(dstURL)

			c := client.New(svc, srcURL)

			if err := c.Connect(ctx); err != nil {
				return goerr.Wrap(err, "failed to connect", goerr.V("src", srcURL), goerr.V("dst", dstURL))
			}

			return nil
		},
	}

	return cmd
}
