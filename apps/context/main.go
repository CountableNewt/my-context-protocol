package main

import (
	"os"
	"strings"

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
	os.Exit(cli.Run(os.Args[1:], env, os.Stdin, os.Stdout, os.Stderr, version))
}
