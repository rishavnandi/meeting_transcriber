package summarize

import (
	"io"
	"log/slog"
)

// discardLogger returns a logger that throws everything away, for tests.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
