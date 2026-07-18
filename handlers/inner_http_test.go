package handlers

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	g "main/globalcfg"
	"main/globalcfg/aiq"
	"main/helpers/aimedia"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

func newTestServer() *httptest.Server {
	logger := g.GetLogger("inner-http-test", slog.LevelInfo)
	logger.Info("inner http test server start")
	return httptest.NewServer(buildHandler(logger))
}

func tarZstdEntries(t *testing.T, data []byte) map[string][]byte {
	t.Helper()
	decoder, err := zstd.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("open zstd: %v", err)
	}
	defer decoder.Close()
	tarReader := tar.NewReader(decoder)
	entries := make(map[string][]byte)
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("iterate tar: %v", err)
		}
		content, err := io.ReadAll(tarReader)
		if err != nil {
			t.Fatalf("read tar file %s: %v", header.Name, err)
		}
		entries[header.Name] = content
	}
	return entries
}

func parseManifest(t *testing.T, data []byte) backupManifest {
	t.Helper()
	var manifest backupManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	return manifest
}

func TestMarsCounterSuccess(t *testing.T) {
	server := newTestServer()
	defer server.Close()

	groupID := time.Now().UnixNano()
	resp, err := http.Post(server.URL+"/mars-counter", "application/json", strings.NewReader(`{"group_id":`+strconv.FormatInt(groupID, 10)+`,"mars_count":2}`))
	if err != nil {
		t.Fatalf("post mars-counter: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status: %d", resp.StatusCode)
	}

	stat := g.Q.ChatStatNow(groupID)
	if stat == nil {
		t.Fatalf("Q.ChatStatNow(groupID) is nil")
	}
	if stat.MarsCount != 1 {
		t.Fatalf("expected MarsCount=1 got %d", stat.MarsCount)
	}
	if stat.MaxMarsCount != 2 {
		t.Fatalf("expected MaxMarsCount=2 got %d", stat.MaxMarsCount)
	}
}

func TestMarsCounterBadJSON(t *testing.T) {
	server := newTestServer()
	defer server.Close()

	resp, err := http.Post(server.URL+"/mars-counter", "application/json", strings.NewReader("{invalid"))
	if err != nil {
		t.Fatalf("post mars-counter: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 got %d", resp.StatusCode)
	}
}

func TestDioBanActions(t *testing.T) {
	server := newTestServer()
	defer server.Close()

	groupID := time.Now().UnixNano()
	body := `{"user_id":1,"group_id":` + strconv.FormatInt(groupID, 10) + `,"action":0}`
	resp, err := http.Post(server.URL+"/dio-ban", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("post dio-ban add: %v", err)
	}
	resp.Body.Close()

	body = `{"user_id":1,"group_id":` + strconv.FormatInt(groupID, 10) + `,"action":2}`
	resp, err = http.Post(server.URL+"/dio-ban", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("post dio-ban ban: %v", err)
	}
	resp.Body.Close()

	stat := g.Q.ChatStatNow(groupID)
	if stat == nil {
		t.Fatalf("expected stat != nil, got nil")
	}
	if stat.DioAddUserCount != 1 {
		t.Fatalf("expected DioAddUserCount=1 got %d", stat.DioAddUserCount)
	}
	if stat.DioBanUserCount != 1 {
		t.Fatalf("expected DioBanUserCount=1 got %d", stat.DioBanUserCount)
	}
}

func TestSetLoggerLevelNotFound(t *testing.T) {
	server := newTestServer()
	defer server.Close()

	req, err := http.NewRequest(http.MethodPut, server.URL+"/loggers/not-exist/1", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "logger not-exist not found") {
		t.Fatalf("unexpected body: %s", string(body))
	}
}

func TestPprofIndexAvailable(t *testing.T) {
	server := newTestServer()
	defer server.Close()

	resp, err := http.Get(server.URL + "/debug/pprof/")
	if err != nil {
		t.Fatalf("get pprof: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 got %d", resp.StatusCode)
	}
}

func TestInnerHTTPTokenProtectsEveryOperationalRoute(t *testing.T) {
	t.Setenv(backupTokenEnvKey, "all-routes-secret")
	server := newTestServer()
	defer server.Close()

	paths := []string{"/", "/loggers", "/debug/pprof/", "/mars-counter", "/dio-ban", "/backupdb"}
	for _, route := range paths {
		resp, err := http.Get(server.URL + route)
		if err != nil {
			t.Fatalf("get %s: %v", route, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("GET %s status = %d, want 401", route, resp.StatusCode)
		}
	}

	req, err := http.NewRequest(http.MethodGet, server.URL+"/debug/pprof/", nil)
	if err != nil {
		t.Fatalf("new authorized request: %v", err)
	}
	req.Header.Set("X-Backup-Token", "all-routes-secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("authorized pprof request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("authorized pprof status = %d, want 200", resp.StatusCode)
	}
}

func TestResolveInnerHTTPAddr(t *testing.T) {
	addr, enabled := resolveInnerHTTPAddr("")
	if !enabled || addr != innerHTTPDefaultAddr {
		t.Fatalf("expected default addr, got %s enabled=%v", addr, enabled)
	}

	addr, enabled = resolveInnerHTTPAddr("OFF")
	if enabled || addr != "" {
		t.Fatalf("expected disabled on OFF, got addr=%s enabled=%v", addr, enabled)
	}

	addr, enabled = resolveInnerHTTPAddr("127.0.0.1:12345")
	if !enabled || addr != "127.0.0.1:12345" {
		t.Fatalf("unexpected addr parsing, got %s enabled=%v", addr, enabled)
	}
}

func TestInnerHTTPAddrRequiresToken(t *testing.T) {
	tests := []struct {
		addr string
		want bool
	}{
		{addr: "127.0.0.1:4019"},
		{addr: "[::1]:4019"},
		{addr: "localhost:4019"},
		{addr: "0.0.0.0:4019", want: true},
		{addr: ":4019", want: true},
		{addr: "192.0.2.1:4019", want: true},
	}
	for _, test := range tests {
		if got := innerHTTPAddrRequiresToken(test.addr); got != test.want {
			t.Errorf("innerHTTPAddrRequiresToken(%q) = %v, want %v", test.addr, got, test.want)
		}
	}
}

func TestResolveInnerHTTPAddrPanicsOnInvalid(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic for invalid addr")
		}
	}()
	resolveInnerHTTPAddr("invalid-addr")
}

func TestRootListsRoutesForAnyMethod(t *testing.T) {
	server := newTestServer()
	defer server.Close()

	req, err := http.NewRequest(http.MethodPost, server.URL+"/", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "GET /loggers") {
		t.Fatalf("unexpected body: %s", string(body))
	}
}

func TestBackupDBSuccessMainByDefault(t *testing.T) {
	server := newTestServer()
	defer server.Close()

	resp, err := http.Get(server.URL + "/backupdb")
	if err != nil {
		t.Fatalf("get backupdb: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/zstd" {
		t.Fatalf("expected content-type application/zstd, got %s", ct)
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, ".tar.zst") {
		t.Fatalf("expected .tar.zst filename, got %s", cd)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	entries := tarZstdEntries(t, body)
	if _, ok := entries["main.db"]; !ok {
		t.Fatalf("expected main.db in zip")
	}
	if _, ok := entries["msg.db"]; ok {
		t.Fatalf("retired msg.db must not be in the archive")
	}
	manifestData, ok := entries["manifest.json"]
	if !ok {
		t.Fatalf("manifest missing")
	}
	manifest := parseManifest(t, manifestData)
	if len(manifest.Databases) != 1 {
		t.Fatalf("expected 1 database, got %d", len(manifest.Databases))
	}
	if manifest.Options["db"] != "main" {
		t.Fatalf("expected manifest option db=main, got %s", manifest.Options["db"])
	}
	if manifest.Media.Included || manifest.CompleteAIDataset {
		t.Fatalf("database-only backup must be marked as an incomplete AI dataset")
	}
}

func createMediaBackupFixture(t *testing.T) (*sql.DB, string, *aimedia.Store, aimedia.Object) {
	t.Helper()
	databasePath := filepath.Join(t.TempDir(), "source.db")
	database, err := sql.Open("sqlite3", databasePath)
	if err != nil {
		t.Fatalf("open source database: %v", err)
	}
	_, err = database.Exec(`
CREATE TABLE ai_messages (
  chat_id INTEGER NOT NULL, msg_id INTEGER NOT NULL, sent_at INTEGER NOT NULL,
  user_id INTEGER NOT NULL, username TEXT NOT NULL, atable_username TEXT,
  msg_type TEXT NOT NULL, text TEXT, reply_to_msg_id INTEGER,
  PRIMARY KEY(chat_id, msg_id)
) WITHOUT ROWID, STRICT;
CREATE TABLE media_objects (
  sha256 TEXT PRIMARY KEY, relative_path TEXT NOT NULL UNIQUE,
  byte_size INTEGER NOT NULL, mime_type TEXT NOT NULL, created_at INTEGER NOT NULL
) WITHOUT ROWID, STRICT;
CREATE TABLE ai_message_media (
  chat_id INTEGER NOT NULL, msg_id INTEGER NOT NULL, ordinal INTEGER NOT NULL,
  media_sha256 TEXT NOT NULL, media_kind TEXT NOT NULL,
  PRIMARY KEY(chat_id, msg_id, ordinal)
) WITHOUT ROWID, STRICT;`)
	if err != nil {
		database.Close()
		t.Fatalf("create source schema: %v", err)
	}
	store, err := aimedia.NewStore(filepath.Join(t.TempDir(), "media"))
	if err != nil {
		database.Close()
		t.Fatalf("create media store: %v", err)
	}
	object, err := store.Put([]byte("complete-media-backup"))
	if err != nil {
		database.Close()
		t.Fatalf("store media: %v", err)
	}
	queries := aiq.New(database)
	err = queries.InsertAIMessage(context.Background(), aiq.InsertAIMessageParams{
		ChatID: -1, MsgID: 10, SentAt: 100, UserID: 7, Username: "user", MsgType: "photo",
	})
	if err == nil {
		err = queries.InsertMediaObject(context.Background(), aiq.InsertMediaObjectParams{
			Sha256: object.SHA256, RelativePath: object.RelativePath, ByteSize: object.Size,
			MimeType: "image/jpeg", CreatedAt: 100,
		})
	}
	if err == nil {
		err = queries.AddAIMessageMedia(context.Background(), aiq.AddAIMessageMediaParams{
			ChatID: -1, MsgID: 10, Ordinal: 0, MediaSha256: object.SHA256, MediaKind: "photo",
		})
	}
	if err != nil {
		database.Close()
		t.Fatalf("insert media references: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database, databasePath, store, object
}

func TestCompleteMediaBackupCanBeRestored(t *testing.T) {
	database, _, store, object := createMediaBackupFixture(t)
	ctx := context.Background()
	snapshotPath := filepath.Join(t.TempDir(), "main.db")
	if err := backupSQLiteDB(ctx, database, snapshotPath); err != nil {
		t.Fatalf("snapshot database: %v", err)
	}
	objects, mediaManifest, err := loadBackupMedia(ctx, snapshotPath, store.Root())
	if err != nil {
		t.Fatalf("prepare media backup: %v", err)
	}
	if !mediaManifest.Included || mediaManifest.ObjectCount != 1 || mediaManifest.TotalBytes != object.Size {
		t.Fatalf("unexpected media manifest: %+v", mediaManifest)
	}
	if len(mediaManifest.ListSHA256) != 64 {
		t.Fatalf("unexpected media list checksum: %q", mediaManifest.ListSHA256)
	}

	var archive bytes.Buffer
	encoder, err := zstd.NewWriter(&archive)
	if err != nil {
		t.Fatalf("create encoder: %v", err)
	}
	tarWriter := tar.NewWriter(encoder)
	if err = addFileToTar(tarWriter, "main.db", snapshotPath); err == nil {
		err = addBytesToTar(tarWriter, mediaManifest.ListPath, backupMediaList(objects), 0600)
	}
	if err == nil {
		for _, item := range objects {
			err = addMediaToTar(ctx, tarWriter, store, item)
			if err != nil {
				break
			}
		}
	}
	manifest := backupManifest{
		Timestamp: time.Now().UTC(), Databases: []backupManifestDB{{Name: "main", Size: 1}},
		Media: mediaManifest, CompleteAIDataset: true,
		Options: map[string]string{"db": "main", "media": "true"},
	}
	if err == nil {
		err = writeManifest(tarWriter, manifest)
	}
	if closeErr := tarWriter.Close(); err == nil {
		err = closeErr
	}
	if closeErr := encoder.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatalf("create complete archive: %v", err)
	}

	entries := tarZstdEntries(t, archive.Bytes())
	listData, ok := entries[mediaManifest.ListPath]
	if !ok {
		t.Fatalf("media manifest list missing")
	}
	if actual := fmt.Sprintf("%x", sha256.Sum256(listData)); actual != mediaManifest.ListSHA256 {
		t.Fatalf("media list checksum mismatch: got %s want %s", actual, mediaManifest.ListSHA256)
	}
	restoreRoot := t.TempDir()
	restoredDB := filepath.Join(restoreRoot, "main.db")
	if err = os.WriteFile(restoredDB, entries["main.db"], 0600); err != nil {
		t.Fatalf("restore database: %v", err)
	}
	for name, data := range entries {
		if !strings.HasPrefix(name, "ai-media/") {
			continue
		}
		destination := filepath.Join(restoreRoot, filepath.FromSlash(name))
		if err = os.MkdirAll(filepath.Dir(destination), 0750); err == nil {
			err = os.WriteFile(destination, data, 0640)
		}
		if err != nil {
			t.Fatalf("restore media %s: %v", name, err)
		}
	}
	restoredStore, err := aimedia.NewStore(filepath.Join(restoreRoot, "ai-media"))
	if err != nil {
		t.Fatalf("open restored store: %v", err)
	}
	if err = restoredStore.Verify(object.SHA256, object.Size); err != nil {
		t.Fatalf("verify restored object: %v", err)
	}
	restored, err := sql.Open("sqlite3", restoredDB)
	if err != nil {
		t.Fatalf("open restored database: %v", err)
	}
	defer restored.Close()
	hashes, err := aiq.New(restored).ListReferencedMediaHashes(ctx)
	if err != nil || len(hashes) != 1 || hashes[0] != object.SHA256 {
		t.Fatalf("unexpected restored media references: %v err=%v", hashes, err)
	}
	restoredManifest := parseManifest(t, entries["manifest.json"])
	if !restoredManifest.CompleteAIDataset || !restoredManifest.Media.Included {
		t.Fatalf("restored manifest does not describe a complete AI dataset")
	}
}

func TestCompleteMediaBackupRejectsCorruptOrMissingObject(t *testing.T) {
	database, _, store, object := createMediaBackupFixture(t)
	snapshotPath := filepath.Join(t.TempDir(), "main.db")
	if err := backupSQLiteDB(context.Background(), database, snapshotPath); err != nil {
		t.Fatalf("snapshot database: %v", err)
	}
	objectPath := filepath.Join(store.Root(), filepath.FromSlash(object.RelativePath))
	if err := os.WriteFile(objectPath, []byte("corrupt"), 0640); err != nil {
		t.Fatalf("corrupt object: %v", err)
	}
	if _, _, err := loadBackupMedia(context.Background(), snapshotPath, store.Root()); err == nil {
		t.Fatalf("expected corrupt media backup to fail")
	}
	if err := os.Remove(objectPath); err != nil {
		t.Fatalf("remove object: %v", err)
	}
	if _, _, err := loadBackupMedia(context.Background(), snapshotPath, store.Root()); err == nil {
		t.Fatalf("expected missing media backup to fail")
	}
}

func TestBackupMediaOptionsAndTimeout(t *testing.T) {
	for _, value := range []string{"", "0", "false"} {
		included, err := parseBackupMedia(value)
		if err != nil || included {
			t.Fatalf("expected media %q disabled: included=%v err=%v", value, included, err)
		}
	}
	for _, value := range []string{"1", "true"} {
		included, err := parseBackupMedia(value)
		if err != nil || !included {
			t.Fatalf("expected media %q enabled: included=%v err=%v", value, included, err)
		}
	}
	if _, err := parseBackupMedia("invalid"); err == nil {
		t.Fatalf("expected invalid media option to fail")
	}
	t.Setenv(backupMaxDurationEnv, "2m")
	req := httptest.NewRequest(http.MethodGet, "/backupdb?media=1", nil)
	ctx, cancel, err := backupRequestContext(req, true)
	if err != nil {
		t.Fatalf("create media backup context: %v", err)
	}
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) < time.Minute {
		t.Fatalf("media backup did not use configurable upper limit")
	}
}

func TestBackupDBScopeMain(t *testing.T) {
	server := newTestServer()
	defer server.Close()

	resp, err := http.Get(server.URL + "/backupdb?db=main")
	if err != nil {
		t.Fatalf("get backupdb: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 got %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	entries := tarZstdEntries(t, body)
	if _, ok := entries["msg.db"]; ok {
		t.Fatalf("did not expect msg.db in scoped backup")
	}
	manifestData, ok := entries["manifest.json"]
	if !ok {
		t.Fatalf("manifest missing")
	}
	manifest := parseManifest(t, manifestData)
	if len(manifest.Databases) != 1 {
		t.Fatalf("expected 1 database, got %d", len(manifest.Databases))
	}
	if manifest.Databases[0].Name != "main" {
		t.Fatalf("expected main in manifest, got %s", manifest.Databases[0].Name)
	}
	if manifest.Options["db"] != "main" {
		t.Fatalf("expected manifest option db=main, got %s", manifest.Options["db"])
	}
}

func TestBackupDBAllRemainsMainOnlyAlias(t *testing.T) {
	server := newTestServer()
	defer server.Close()
	resp, err := http.Get(server.URL + "/backupdb?db=all")
	if err != nil {
		t.Fatalf("get backupdb all alias: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 got %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	entries := tarZstdEntries(t, body)
	if _, ok := entries["msg.db"]; ok {
		t.Fatalf("all compatibility alias must not include retired msg.db")
	}
	manifest := parseManifest(t, entries["manifest.json"])
	if manifest.Options["db"] != "all" || len(manifest.Databases) != 1 {
		t.Fatalf("unexpected all alias manifest: %+v", manifest)
	}
}

func TestBackupDBMediaModeMarksCompleteDataset(t *testing.T) {
	originalDirectory, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	if err = os.Chdir(t.TempDir()); err != nil {
		t.Fatalf("change working directory: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(originalDirectory) })

	server := newTestServer()
	defer server.Close()
	resp, err := http.Get(server.URL + "/backupdb?db=main&media=1")
	if err != nil {
		t.Fatalf("get complete media backup: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 got %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read complete backup: %v", err)
	}
	entries := tarZstdEntries(t, body)
	manifest := parseManifest(t, entries["manifest.json"])
	if !manifest.CompleteAIDataset || !manifest.Media.Included {
		t.Fatalf("media backup was not marked complete: %+v", manifest)
	}
	if manifest.Options["media"] != "true" {
		t.Fatalf("unexpected media option: %q", manifest.Options["media"])
	}
	if _, ok := entries[manifest.Media.ListPath]; !ok {
		t.Fatalf("media list %q missing", manifest.Media.ListPath)
	}
}

func TestBackupDBInvalidSelection(t *testing.T) {
	server := newTestServer()
	defer server.Close()

	resp, err := http.Get(server.URL + "/backupdb?db=unknown")
	if err != nil {
		t.Fatalf("get backupdb: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 got %d", resp.StatusCode)
	}

	resp, err = http.Get(server.URL + "/backupdb?db=msg")
	if err != nil {
		t.Fatalf("get invalid media scope: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("expected retired message database to return 410, got %d", resp.StatusCode)
	}
}

func TestBackupDBToken(t *testing.T) {
	t.Setenv(backupTokenEnvKey, "secret-token")

	server := newTestServer()
	defer server.Close()

	resp, err := http.Get(server.URL + "/backupdb")
	if err != nil {
		t.Fatalf("get backupdb without token: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 got %d", resp.StatusCode)
	}

	req, err := http.NewRequest(http.MethodGet, server.URL+"/backupdb", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("X-Backup-Token", "secret-token")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get backupdb with token: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 got %d", resp.StatusCode)
	}
}
