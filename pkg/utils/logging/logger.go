package logging

import (
	"log/slog"
	"sync"
)

var (
	defaultLogger = slog.Default()
	loggerMutex   sync.Mutex
)

func Default() *slog.Logger {
	loggerMutex.Lock()
	defer loggerMutex.Unlock()

	return defaultLogger
}

func SetDefault(logger *slog.Logger) {
	loggerMutex.Lock()
	defer loggerMutex.Unlock()

	defaultLogger = logger
}

func Disable() {
	logger := slog.New(slog.NewJSONHandler(&NopWriter{}, nil))
	SetDefault(logger)
}

type NopWriter struct{}

func (x *NopWriter) Write(p []byte) (n int, err error) {
	return len(p), nil
}
