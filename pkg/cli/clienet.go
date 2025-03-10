package cli

import (
	"context"
	"net/http"
	"strings"

	"github.com/m-mizutani/backstream/pkg/controller/client"
	"github.com/m-mizutani/backstream/pkg/service/tunnel"
	"github.com/m-mizutani/backstream/pkg/utils/logging"
	"github.com/m-mizutani/goerr/v2"
	"github.com/m-mizutani/harlog"
	"github.com/urfave/cli/v3"
)

func cmdClient() *cli.Command {
	var (
		srcURL string
		dstURL string
		header []string
		output string
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
			&cli.StringSliceFlag{
				Name:        "header",
				Aliases:     []string{"H"},
				Usage:       "HTTP header, e.g. 'Authorization: Bearer <token>'",
				Sources:     cli.EnvVars("BACKSTREAM_HEADER"),
				Destination: &header,
			},
			&cli.StringFlag{
				Name:        "output",
				Aliases:     []string{"o"},
				Usage:       "Directory to save HAR files",
				Destination: &output,
			},
		},
		Usage: "Start backstream client",

		Action: func(ctx context.Context, cmd *cli.Command) error {
			var tunnelOptions []tunnel.Option
			if output != "" {
				logger := harlog.New(
					harlog.WithOutputDir(output),
					harlog.WithLogger(logging.Default()),
				)
				httpClient := &http.Client{
					Transport: logger,
				}
				tunnelOptions = append(tunnelOptions, tunnel.WithHTTPClient(httpClient))
			}
			svc := tunnel.New(dstURL, tunnelOptions...)

			var options []client.Option
			for _, h := range header {
				parts := strings.Split(h, ":")
				if len(parts) != 2 {
					return goerr.New("invalid header format", goerr.V("header", h))
				}
				options = append(options, client.WithHeader(
					strings.TrimSpace(parts[0]),
					strings.TrimSpace(parts[1]),
				))
			}

			c := client.New(svc, srcURL, options...)
			if err := c.Connect(ctx); err != nil {
				return goerr.Wrap(err, "failed to connect", goerr.V("src", srcURL), goerr.V("dst", dstURL))
			}

			return nil
		},
	}

	return cmd
}
