package main

import (
	"context"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/CountableNewt/my-context-protocol/apps/context/internal/cli"
)

var version = "dev"

func main() {
	env := make(map[string]string)
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			env[key] = value
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(cli.Run(ctx, os.Args[1:], env, os.Stdin, os.Stdout, os.Stderr, version))
}
