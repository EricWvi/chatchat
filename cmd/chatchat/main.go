// Command chatchat is the command line client for taking part in chatchat
// chats. See docs/api.md for its commands.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"chatchat/internal/cli"
)

// Set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cli.Run(ctx, cli.Env{
		Args: os.Args[1:], Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr, Version: version,
	})
	stop()
	os.Exit(code)
}
