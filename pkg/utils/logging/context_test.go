package logging_test

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/m-mizutani/backstream/pkg/utils/logging"
	"github.com/m-mizutani/gt"
)

func TestInjectAndExtract(t *testing.T) {
	ctx := context.Background()
	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(buf, nil))

	// Test Inject
	ctx = logging.Inject(ctx, logger)
	extractedLogger := logging.Extract(ctx)
	extractedLogger.Info("Test Inject")
	gt.S(t, buf.String()).Contains("Test Inject")

	// Test Extract with no logger in context
	ctx = context.Background()
	extractedLogger = logging.Extract(ctx)
	extractedLogger.Info("Test Extract with no logger in context")
	gt.S(t, buf.String()).NotContains("Test Extract with no logger in context")
}
