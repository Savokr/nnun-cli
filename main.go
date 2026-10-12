package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
)

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := newCommand().ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "nnun:", err)
		os.Exit(1)
	}
}
