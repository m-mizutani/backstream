package cli

import (
	"context"

	"github.com/m-mizutani/backstream/pkg/controller/client"
	"github.com/m-mizutani/goerr/v2"
	"github.com/urfave/cli/v3"
)

func cmdClient() *cli.Command {
	cmd := &cli.Command{
		Name:    "client",
		Aliases: []string{"c"},
		Usage:   "Start backstream client",

		Action: func(ctx context.Context, cmd *cli.Command) error {
			if cmd.Args().Len() != 1 {
				return goerr.New("URL is required")
			}

			url := cmd.Args().Get(0)
			c := client.New(url)

			if err := c.Connect(ctx); err != nil {
				return goerr.Wrap(err, "failed to connect", goerr.V("url", url))
			}

			return nil
		},
	}

	return cmd
}
