package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"main/internal/dbschema"
	"net/url"
	"os"
	"path/filepath"

	_ "github.com/mattn/go-sqlite3"
)

func main() {
	var output string
	flag.StringVar(&output, "output", "", "new canonical main SQLite database (must not exist)")
	flag.Parse()
	if output == "" {
		fmt.Fprintln(os.Stderr, "-output is required")
		os.Exit(2)
	}
	abs, err := filepath.Abs(output)
	if err != nil {
		fatal(err)
	}
	if _, err = os.Lstat(abs); err == nil {
		fatal(fmt.Errorf("output already exists: %s", abs))
	} else if !os.IsNotExist(err) {
		fatal(err)
	}
	u := &url.URL{Scheme: "file", Path: abs}
	query := u.Query()
	query.Set("mode", "rwc")
	query.Set("_foreign_keys", "on")
	u.RawQuery = query.Encode()
	database, err := sql.Open("sqlite3", u.String())
	if err != nil {
		fatal(err)
	}
	if err = dbschema.Initialize(context.Background(), database); err == nil {
		err = dbschema.Validate(context.Background(), database)
	}
	if closeErr := database.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(abs)
		fatal(err)
	}
	file, err := os.Open(abs)
	if err != nil {
		fatal(err)
	}
	digest := sha256.New()
	_, err = io.Copy(digest, file)
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		fatal(err)
	}
	fmt.Printf("%s  %s\n", hex.EncodeToString(digest.Sum(nil)), abs)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
