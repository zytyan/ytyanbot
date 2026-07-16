package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"main/internal/maindbmigrate"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	var cfg maindbmigrate.Config
	var timeout time.Duration
	flag.StringVar(&cfg.Source, "source", "", "read-only current main SQLite database")
	flag.StringVar(&cfg.Output, "output", "", "new migrated SQLite database (must not exist)")
	flag.StringVar(&cfg.ManifestPath, "manifest", "", "manifest path (default: <output>.manifest.json)")
	flag.DurationVar(&timeout, "timeout", 0, "optional migration upper limit")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	manifest, err := maindbmigrate.Run(ctx, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "main database migration failed: %v\n", err)
		os.Exit(1)
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err = encoder.Encode(manifest); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
