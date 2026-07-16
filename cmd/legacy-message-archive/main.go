package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"main/internal/legacyarchive"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	var cfg legacyarchive.Config
	flag.StringVar(&cfg.MessageDB, "message-db", "", "live legacy message SQLite database")
	flag.StringVar(&cfg.WALDB, "wal-db", "", "live Meilisearch WAL SQLite database")
	flag.StringVar(&cfg.MeiliDump, "meili-dump", "", "completed Meilisearch dump file")
	flag.StringVar(&cfg.MeiliDumpUnavailableReason, "meili-dump-unavailable-reason", "", "explicit reason no Meilisearch dump exists")
	flag.StringVar(&cfg.OutputDir, "output-dir", "", "new archive directory (must not exist)")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	manifest, err := legacyarchive.Run(ctx, cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err = encoder.Encode(manifest); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
