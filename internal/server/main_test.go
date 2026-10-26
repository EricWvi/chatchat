package server

import (
	"io"
	"log/slog"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	// Request logs drown test output; the leak test installs its own.
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	os.Exit(m.Run())
}
