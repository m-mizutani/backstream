package config

import (
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/fatih/color"
	"github.com/m-mizutani/backstream/pkg/utils/logging"
	"github.com/m-mizutani/clog"
	"github.com/m-mizutani/goerr/v2"
	"github.com/m-mizutani/masq"
	"github.com/urfave/cli/v3"
)

type Logger struct {
	level  string
	format string
	output string
	stack  bool
}

func (x *Logger) Flags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Name:        "log-level",
			Aliases:     []string{"l"},
			Category:    "Log",
			Usage:       "Log level (debug, info, warn, error)",
			Value:       "info",
			Sources:     cli.EnvVars("BACKSTREAM_LOG_LEVEL"),
			Destination: &x.level,
		},
		&cli.StringFlag{
			Name:        "log-format",
			Aliases:     []string{"f"},
			Category:    "Log",
			Usage:       "Log format (json, text)",
			Value:       "text",
			Sources:     cli.EnvVars("BACKSTREAM_LOG_FORMAT"),
			Destination: &x.format,
		},
		&cli.StringFlag{
			Name:        "log-output",
			Aliases:     []string{"o"},
			Category:    "Log",
			Usage:       "Log output (stdout, stderr, file)",
			Value:       "stdout",
			Sources:     cli.EnvVars("BACKSTREAM_LOG_OUTPUT"),
			Destination: &x.output,
		},
		&cli.BoolFlag{
			Name:        "log-stack",
			Aliases:     []string{"s"},
			Category:    "Log",
			Usage:       "Show log stack trace (The option is for console only)",
			Value:       false,
			Destination: &x.stack,
		},
	}
}

func (x Logger) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("level", x.level),
		slog.String("format", x.format),
		slog.String("output", x.output),
		slog.Bool("stack", x.stack),
	)
}

func (x Logger) New() (*slog.Logger, func(), error) {
	closer := func() {}

	// Log level
	var level slog.Level
	levelMap := map[string]slog.Level{
		"debug": slog.LevelDebug,
		"info":  slog.LevelInfo,
		"warn":  slog.LevelWarn,
		"error": slog.LevelError,
	}

	if v, ok := levelMap[x.level]; ok {
		level = v
	} else {
		return nil, nil, goerr.New("Invalid log level", goerr.V("level", x.level))
	}

	// Log output
	var w io.Writer
	switch x.output {
	case "stdout", "-":
		w = os.Stdout
	case "stderr":
		w = os.Stderr
	default:
		file, err := os.OpenFile(x.output, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			return nil, nil, goerr.Wrap(err, "Failed to open log file", goerr.V("file", x.output))
		}
		w = file
		closer = func() {
			if err := file.Close(); err != nil {
				logging.Default().Error("failed to close log file", "error", err)
			}
		}
	}

	filter := masq.New(masq.WithFieldName("Authorization"))

	// Log format
	var handler slog.Handler

	goerrHook := func(_ []string, attr slog.Attr) *clog.HandleAttr {
		var attrs []any
		if err, ok := attr.Value.Any().(error); ok {
			attrs = append(attrs, slog.Any("msg", fmt.Sprintf("%v", err)))

			if goErr, ok := attr.Value.Any().(*goerr.Error); ok {
				for k, v := range goErr.Values() {
					attrs = append(attrs, slog.Any(k, v))
				}

			}

			newAttr := slog.Group(attr.Key, attrs...)
			return &clog.HandleAttr{
				NewAttr: &newAttr,
			}
		}

		return nil
	}
	if x.stack {
		goerrHook = clog.GoerrHook
	}

	switch x.format {
	case "json":
		handler = slog.NewJSONHandler(w, &slog.HandlerOptions{
			AddSource:   true,
			Level:       level,
			ReplaceAttr: filter,
		})

	case "text":
		options := []clog.Option{
			clog.WithWriter(w),
			clog.WithLevel(level),
			clog.WithReplaceAttr(filter),
			clog.WithAttrHook(goerrHook),
			clog.WithColorMap(&clog.ColorMap{
				Level: map[slog.Level]*color.Color{
					slog.LevelDebug: color.New(color.FgGreen, color.Bold),
					slog.LevelInfo:  color.New(color.FgCyan, color.Bold),
					slog.LevelWarn:  color.New(color.FgYellow, color.Bold),
					slog.LevelError: color.New(color.FgRed, color.Bold),
				},
				LevelDefault: color.New(color.FgBlue, color.Bold),
				Time:         color.New(color.FgWhite),
				Message:      color.New(color.FgHiWhite),
				AttrKey:      color.New(color.FgHiCyan),
				AttrValue:    color.New(color.FgHiWhite),
			}),
			clog.WithAttrHook(clog.GoerrHook),
		}

		if level <= slog.LevelDebug {
			options = append(options, clog.WithSource(true))
		}

		handler = clog.New(options...)

	default:
		return nil, nil, goerr.New("Invalid log format", goerr.V("format", x.format))
	}

	return slog.New(handler), closer, nil
}
