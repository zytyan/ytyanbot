package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"main/internal/aidbmigrate"
	"os"
	"os/signal"
	"os/user"
	"strconv"
	"syscall"
	"time"
)

func resolveOwner(userName, groupName string) (int, int, error) {
	uid, gid := -1, -1
	if userName != "" {
		account, err := user.Lookup(userName)
		if err != nil {
			return uid, gid, err
		}
		uid, err = strconv.Atoi(account.Uid)
		if err != nil {
			return -1, -1, err
		}
	}
	if groupName != "" {
		group, err := user.LookupGroup(groupName)
		if err != nil {
			return uid, gid, err
		}
		gid, err = strconv.Atoi(group.Gid)
		if err != nil {
			return -1, -1, err
		}
	}
	return uid, gid, nil
}

func main() {
	var cfg aidbmigrate.Config
	var mediaUser, mediaGroup string
	var timeout time.Duration
	flag.StringVar(&cfg.Source, "source", "", "read-only legacy SQLite database")
	flag.StringVar(&cfg.Output, "output", "", "new V2 SQLite database (must not exist)")
	flag.StringVar(&cfg.MediaPath, "media", "", "new content-addressed media directory (must not exist)")
	flag.StringVar(&cfg.ManifestPath, "manifest", "", "migration manifest path (default: <output>.manifest.json)")
	flag.StringVar(&cfg.DefaultProvider, "default-provider", "gemini", "provider for sessions without legacy metadata")
	flag.StringVar(&cfg.DefaultModel, "default-model", "gemini-3-flash-preview", "model for sessions without legacy metadata")
	flag.StringVar(&mediaUser, "media-user", "tgbotapi", "owner user for the media tree; empty keeps current owner")
	flag.StringVar(&mediaGroup, "media-group", "tgbots", "owner group for the media tree; empty keeps current group")
	flag.DurationVar(&timeout, "timeout", 0, "optional migration upper limit; zero uses only signal cancellation")
	flag.Parse()

	uid, gid, err := resolveOwner(mediaUser, mediaGroup)
	if err != nil {
		fmt.Fprintf(os.Stderr, "resolve media owner: %v\n", err)
		os.Exit(2)
	}
	cfg.SetMediaOwner = mediaUser != "" || mediaGroup != ""
	cfg.MediaUID, cfg.MediaGID = uid, gid
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	manifest, err := aidbmigrate.Run(ctx, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "AI V2 migration failed: %v\n", err)
		os.Exit(1)
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err = encoder.Encode(manifest); err != nil {
		fmt.Fprintf(os.Stderr, "write migration result: %v\n", err)
		os.Exit(1)
	}
}
