package logging

import (
	"context"
	"log/slog"
)

type ctxKey struct{}

func Inject(ctx context.Context, logger *slog.Logger) context.Context {
	return context.WithValue(ctx, ctxKey{}, logger)
}

func Extract(ctx context.Context) *slog.Logger {
	if logger, ok := ctx.Value(ctxKey{}).(*slog.Logger); ok {
		return logger
	}

	return Default()
}
