package cli

import (
	"context"

	"github.com/m-mizutani/backstream/pkg/cli/config"
	"github.com/m-mizutani/backstream/pkg/utils/logging"
	"github.com/m-mizutani/goerr/v2"
	"github.com/urfave/cli/v3"
)

func Run(ctx context.Context, args []string) error {
	var loggerCfg config.Logger
	flags := loggerCfg.Flags()
	app := cli.Command{
		Name:  "backstream",
		Flags: flags,
		Commands: []*cli.Command{
			cmdClient(),
			cmdServer(),
		},
		Before: func(ctx context.Context, c *cli.Command) (context.Context, error) {
			logger, closer, err := loggerCfg.New()
			if err != nil {
				return nil, goerr.Wrap(err, "failed to create logger")
			}
			defer closer()

			ctx = logging.Inject(ctx, logger)
			logging.SetDefault(logger)
			return ctx, nil
		},
	}

	if err := app.Run(ctx, args); err != nil {
		logging.Default().Error("failed to run app", "error", err)
		return goerr.Wrap(err, "failed to run app")
	}

	return nil
}
