package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/deploypier/deploypier/internal/cli"
)

func main() {
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	if err := cli.Run(ctx, os.Args[1:], os.Stdout, os.Stderr, os.Environ(), cwd); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
