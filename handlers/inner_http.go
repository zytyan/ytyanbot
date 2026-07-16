package handlers

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	g "main/globalcfg"
	"main/globalcfg/aiq"
	"main/helpers/aimedia"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"path"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	json "github.com/json-iterator/go"
	"github.com/klauspost/compress/zstd"
	sqlite3 "github.com/mattn/go-sqlite3"
)

const (
	DioBanActionAdd = iota
	DioBanActionBanByWrongButton
	DioBanActionBanByNoButton
	DioBanActionBanByNoMsg
)

const (
	innerHTTPEnvKey               = "BOT_INNER_HTTP"
	innerHTTPDefaultAddr          = "127.0.0.1:4019"
	backupTokenEnvKey             = "GOYTYAN_BACKUP_TOKEN"
	backupMaxDurationEnv          = "GOYTYAN_BACKUP_MAX_DURATION"
	maxRequestBodyBytes           = 1 << 20 // 1MB upper bound for small JSON payloads
	defaultMediaBackupMaxDuration = 30 * time.Minute
)

type MarsInfo struct {
	GroupID   int64 `json:"group_id"`
	MarsCount int64 `json:"mars_count"`
}

type DioBanUser struct {
	UserId  int64 `json:"user_id"`
	GroupId int64 `json:"group_id"`
	Action  int   `json:"action"`
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

type countingWriter struct {
	w       io.Writer
	written int64
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.written += int64(n)
	return n, err
}

type backupSelection struct {
	includeMain bool
	raw         string
}

var errLegacyMessageBackupGone = errors.New("legacy message database has been archived and retired")

type backupManifestDB struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Size int64  `json:"size"`
}

type backupManifestMedia struct {
	Included      bool   `json:"included"`
	ObjectCount   int64  `json:"object_count"`
	TotalBytes    int64  `json:"total_bytes"`
	ListSHA256    string `json:"list_sha256,omitempty"`
	ListPath      string `json:"list_path,omitempty"`
	ArchivePrefix string `json:"archive_prefix,omitempty"`
}

type backupManifest struct {
	Timestamp         time.Time           `json:"timestamp"`
	Databases         []backupManifestDB  `json:"databases"`
	Media             backupManifestMedia `json:"media"`
	CompleteAIDataset bool                `json:"complete_ai_dataset"`
	Options           map[string]string   `json:"options"`
}

type backupTarget struct {
	name     string
	path     string
	db       *sql.DB
	destPath string
}

type backupMediaObject struct {
	SHA256       string
	RelativePath string
	Size         int64
	MIMEType     string
}

func (r *statusRecorder) WriteHeader(status int) {
	if r.status != 0 {
		return
	}
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
		r.ResponseWriter.WriteHeader(http.StatusOK)
	}
	return r.ResponseWriter.Write(b)
}

func parseBackupSelection(param string) (backupSelection, error) {
	value := strings.ToLower(strings.TrimSpace(param))
	if value == "" {
		value = "main"
	}
	switch value {
	case "all":
		return backupSelection{includeMain: true, raw: "all"}, nil
	case "main":
		return backupSelection{includeMain: true, raw: "main"}, nil
	case "msg":
		return backupSelection{}, errLegacyMessageBackupGone
	default:
		return backupSelection{}, fmt.Errorf("invalid db query: %s", param)
	}
}

func parseBackupMedia(param string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(param)) {
	case "", "0", "false":
		return false, nil
	case "1", "true":
		return true, nil
	default:
		return false, fmt.Errorf("invalid media query: %s", param)
	}
}

func backupRequestContext(r *http.Request, includeMedia bool) (context.Context, context.CancelFunc, error) {
	if !includeMedia {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		return ctx, cancel, nil
	}
	limit := defaultMediaBackupMaxDuration
	if configured := strings.TrimSpace(os.Getenv(backupMaxDurationEnv)); configured != "" {
		parsed, err := time.ParseDuration(configured)
		if err != nil || parsed <= 0 {
			return nil, nil, fmt.Errorf("invalid %s: %q", backupMaxDurationEnv, configured)
		}
		limit = parsed
	}
	ctx, cancel := context.WithTimeout(r.Context(), limit)
	return ctx, cancel, nil
}

func checkBackupToken(r *http.Request, token string) bool {
	if token == "" {
		return true
	}
	if q := r.URL.Query().Get("token"); q != "" && q == token {
		return true
	}
	return r.Header.Get("X-Backup-Token") == token
}

func decodeJSONBody(w http.ResponseWriter, req *http.Request, v any) error {
	defer req.Body.Close()
	reader := http.MaxBytesReader(w, req.Body, maxRequestBodyBytes)
	decoder := json.NewDecoder(reader)
	return decoder.Decode(v)
}

func marsCounter(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	var marsInfo MarsInfo
	if err := decodeJSONBody(w, r, &marsInfo); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	g.Q.ChatStatNow(marsInfo.GroupID).IncMarsCount(marsInfo.MarsCount)
	w.WriteHeader(http.StatusOK)
}

func dioBan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	var dioBanUser DioBanUser
	if err := decodeJSONBody(w, r, &dioBanUser); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	switch dioBanUser.Action {
	case DioBanActionAdd:
		g.Q.ChatStatNow(dioBanUser.GroupId).IncDioAddUserCount()
	case DioBanActionBanByWrongButton, DioBanActionBanByNoButton, DioBanActionBanByNoMsg:
		g.Q.ChatStatNow(dioBanUser.GroupId).IncDioBanUserCount()
	}
	w.WriteHeader(http.StatusOK)
}

func formatLoggers() string {
	buf := strings.Builder{}
	for name, logger := range g.GetAllLoggers() {
		level := logger.Level.Level()
		buf.WriteString(
			fmt.Sprintf("%-16s\t[%d]%s\n", name, level, level.String()),
		)
	}
	return buf.String()
}

func showLoggers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(formatLoggers()))
}

func parseLoggerParams(path string) (string, string, bool) {
	trimmed := strings.TrimPrefix(path, "/loggers/")
	parts := strings.Split(trimmed, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func setLoggerLevel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		http.NotFound(w, r)
		return
	}
	loggerName, levelParam, ok := parseLoggerParams(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}

	_, exists := g.GetAllLoggers()[loggerName]
	if !exists {
		_, _ = fmt.Fprintf(w, "logger %s not found\n%s", loggerName, formatLoggers())
		return
	}

	newLevel, err := strconv.ParseInt(levelParam, 10, 8)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(err.Error()))
		return
	}
	g.SetLoggerLevel(loggerName, slog.Level(newLevel))
}

func backupSQLiteDB(ctx context.Context, src *sql.DB, dstPath string) error {
	dstDB, err := sql.Open("sqlite3", dstPath)
	if err != nil {
		return err
	}
	defer dstDB.Close()

	srcConn, err := src.Conn(ctx)
	if err != nil {
		return err
	}
	defer srcConn.Close()

	dstConn, err := dstDB.Conn(ctx)
	if err != nil {
		return err
	}
	defer dstConn.Close()

	return dstConn.Raw(func(dest interface{}) error {
		destSQLite, ok := dest.(*sqlite3.SQLiteConn)
		if !ok {
			return errors.New("unexpected destination connection type")
		}
		return srcConn.Raw(func(source interface{}) error {
			srcSQLite, ok := source.(*sqlite3.SQLiteConn)
			if !ok {
				return errors.New("unexpected source connection type")
			}
			backup, err := destSQLite.Backup("main", srcSQLite, "main")
			if err != nil {
				return err
			}
			for {
				if ctx.Err() != nil {
					_ = backup.Finish()
					return ctx.Err()
				}
				done, err := backup.Step(128)
				if err != nil {
					_ = backup.Finish()
					return err
				}
				if done {
					return backup.Finish()
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	})
}

func addFileToTar(tw *tar.Writer, name, srcPath string) error {
	file, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return err
	}
	header := &tar.Header{
		Name:    name,
		Mode:    0600,
		Size:    info.Size(),
		ModTime: time.Now(),
	}
	if err := tw.WriteHeader(header); err != nil {
		return err
	}
	_, err = io.Copy(tw, file)
	return err
}

func addBytesToTar(tw *tar.Writer, name string, data []byte, mode int64) error {
	header := &tar.Header{Name: name, Mode: mode, Size: int64(len(data)), ModTime: time.Now()}
	if err := tw.WriteHeader(header); err != nil {
		return err
	}
	_, err := tw.Write(data)
	return err
}

func backupMediaList(objects []backupMediaObject) []byte {
	var list strings.Builder
	for _, object := range objects {
		_, _ = fmt.Fprintf(&list, "%s\t%d\t%s\t%s\n",
			object.SHA256, object.Size, object.MIMEType, object.RelativePath)
	}
	return []byte(list.String())
}

func verifyBackupMediaObject(ctx context.Context, store *aimedia.Store, hash string, expectedSize int64) error {
	file, err := store.Open(hash)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != expectedSize {
		return fmt.Errorf("AI media object %s size mismatch: got %d want %d", hash, info.Size(), expectedSize)
	}
	digest := sha256.New()
	if _, err = io.Copy(digest, contextReader{ctx: ctx, r: file}); err != nil {
		return err
	}
	if actual := hex.EncodeToString(digest.Sum(nil)); actual != hash {
		return fmt.Errorf("AI media object %s hash mismatch: got %s", hash, actual)
	}
	return nil
}

func loadBackupMedia(ctx context.Context, snapshotPath, mediaRoot string) ([]backupMediaObject, backupManifestMedia, error) {
	snapshot, err := sql.Open("sqlite3", snapshotPath)
	if err != nil {
		return nil, backupManifestMedia{}, err
	}
	defer snapshot.Close()
	queries := aiq.New(snapshot)
	hashes, err := queries.ListReferencedMediaHashes(ctx)
	if err != nil {
		return nil, backupManifestMedia{}, err
	}
	store, err := aimedia.NewStore(mediaRoot)
	if err != nil {
		return nil, backupManifestMedia{}, err
	}
	objects := make([]backupMediaObject, 0, len(hashes))
	manifest := backupManifestMedia{
		Included: true, ArchivePrefix: "ai-media/", ListPath: "media-manifest.tsv",
	}
	for _, hash := range hashes {
		if len(hash) != sha256.Size*2 {
			return nil, backupManifestMedia{}, fmt.Errorf("invalid media hash in snapshot: %q", hash)
		}
		row, err := queries.GetMediaObject(ctx, hash)
		if err != nil {
			return nil, backupManifestMedia{}, err
		}
		expectedPath := path.Join("sha256", hash[:2], hash)
		if row.RelativePath != expectedPath {
			return nil, backupManifestMedia{}, fmt.Errorf("media %s path mismatch: got %q want %q",
				hash, row.RelativePath, expectedPath)
		}
		if err = verifyBackupMediaObject(ctx, store, hash, row.ByteSize); err != nil {
			return nil, backupManifestMedia{}, err
		}
		object := backupMediaObject{
			SHA256: hash, RelativePath: row.RelativePath, Size: row.ByteSize, MIMEType: row.MimeType,
		}
		objects = append(objects, object)
		manifest.ObjectCount++
		manifest.TotalBytes += row.ByteSize
	}
	listDigest := sha256.Sum256(backupMediaList(objects))
	manifest.ListSHA256 = hex.EncodeToString(listDigest[:])
	return objects, manifest, nil
}

func addMediaToTar(ctx context.Context, tw *tar.Writer, store *aimedia.Store, object backupMediaObject) error {
	file, err := store.Open(object.SHA256)
	if err != nil {
		return err
	}
	defer file.Close()
	header := &tar.Header{
		Name: path.Join("ai-media", object.RelativePath), Mode: 0640,
		Size: object.Size, ModTime: time.Now(),
	}
	if err = tw.WriteHeader(header); err != nil {
		return err
	}
	digest := sha256.New()
	reader := io.TeeReader(contextReader{ctx: ctx, r: file}, digest)
	if _, err = io.CopyN(tw, reader, object.Size); err != nil {
		return err
	}
	if actual := hex.EncodeToString(digest.Sum(nil)); actual != object.SHA256 {
		return fmt.Errorf("AI media object %s changed while archiving: got %s", object.SHA256, actual)
	}
	return nil
}

func writeManifest(tw *tar.Writer, manifest backupManifest) error {
	data, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	header := &tar.Header{
		Name:    "manifest.json",
		Mode:    0600,
		Size:    int64(len(data)),
		ModTime: time.Now(),
	}
	if err := tw.WriteHeader(header); err != nil {
		return err
	}
	_, err = tw.Write(data)
	return err
}

func backupDBHandler(logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}

		token := os.Getenv(backupTokenEnvKey)
		if !checkBackupToken(r, token) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte("unauthorized"))
			return
		}

		selection, err := parseBackupSelection(r.URL.Query().Get("db"))
		if err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, errLegacyMessageBackupGone) {
				status = http.StatusGone
			}
			http.Error(w, err.Error(), status)
			return
		}
		includeMedia, err := parseBackupMedia(r.URL.Query().Get("media"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		cfg := g.GetConfig()
		if cfg == nil {
			http.Error(w, "config not initialized", http.StatusInternalServerError)
			return
		}

		targets := make([]backupTarget, 0, 1)
		if selection.includeMain {
			targets = append(targets, backupTarget{
				name: "main",
				path: cfg.DatabasePath,
				db:   g.RawMainDb(),
			})
		}
		for _, target := range targets {
			if target.db == nil {
				http.Error(w, "database not initialized", http.StatusInternalServerError)
				return
			}
		}

		tmpDir, err := os.MkdirTemp("", "backupdb-*")
		if err != nil {
			logger.Error("create backup temp dir", "err", err)
			http.Error(w, "failed to create temp dir", http.StatusInternalServerError)
			return
		}
		defer os.RemoveAll(tmpDir)

		ctx, cancel, err := backupRequestContext(r, includeMedia)
		if err != nil {
			logger.Error("configure backup timeout", "err", err)
			http.Error(w, "invalid backup timeout", http.StatusInternalServerError)
			return
		}
		defer cancel()

		start := time.Now()
		manifest := backupManifest{
			Timestamp: start.UTC(),
			Media:     backupManifestMedia{Included: false},
			Options:   map[string]string{"db": selection.raw, "media": strconv.FormatBool(includeMedia)},
		}

		for i := range targets {
			targets[i].destPath = filepath.Join(tmpDir, targets[i].name+".db")
			if err := backupSQLiteDB(ctx, targets[i].db, targets[i].destPath); err != nil {
				logger.Error("backup sqlite database", "db", targets[i].name, "err", err)
				http.Error(w, "backup failed", http.StatusInternalServerError)
				return
			}
			info, err := os.Stat(targets[i].destPath)
			if err != nil {
				logger.Error("stat backup file", "file", targets[i].destPath, "err", err)
				http.Error(w, "backup failed", http.StatusInternalServerError)
				return
			}
			manifest.Databases = append(manifest.Databases, backupManifestDB{
				Name: targets[i].name,
				Path: targets[i].path,
				Size: info.Size(),
			})
		}

		var mediaObjects []backupMediaObject
		if includeMedia {
			mainSnapshot := ""
			for _, target := range targets {
				if target.name == "main" {
					mainSnapshot = target.destPath
					break
				}
			}
			mediaObjects, manifest.Media, err = loadBackupMedia(ctx, mainSnapshot, cfg.AIMediaPath)
			if err != nil {
				logger.Error("prepare AI media backup", "err", err)
				http.Error(w, "media backup failed", http.StatusInternalServerError)
				return
			}
			manifest.CompleteAIDataset = true
		}

		filename := fmt.Sprintf("backup-%s.tar.zst", manifest.Timestamp.Format("20060102-150405Z"))
		w.Header().Set("Content-Type", "application/zstd")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filename))

		counter := &countingWriter{w: w}
		encoder, err := zstd.NewWriter(counter,
			zstd.WithEncoderLevel(zstd.SpeedBetterCompression),
			zstd.WithWindowSize(32<<20),
		)
		if err != nil {
			logger.Error("create zstd encoder", "err", err)
			http.Error(w, "failed to create encoder", http.StatusInternalServerError)
			return
		}

		tarWriter := tar.NewWriter(encoder)
		for _, target := range targets {
			if err := addFileToTar(tarWriter, target.name+".db", target.destPath); err != nil {
				logger.Error("write backup tar entry", "db", target.name, "err", err)
				return
			}
		}
		if includeMedia {
			if err := addBytesToTar(tarWriter, manifest.Media.ListPath, backupMediaList(mediaObjects), 0600); err != nil {
				logger.Error("write AI media manifest", "err", err)
				return
			}
			store, storeErr := aimedia.NewStore(cfg.AIMediaPath)
			if storeErr != nil {
				logger.Error("open AI media store", "err", storeErr)
				return
			}
			for _, object := range mediaObjects {
				if err := addMediaToTar(ctx, tarWriter, store, object); err != nil {
					logger.Error("write AI media tar entry", "hash", object.SHA256, "err", err)
					return
				}
			}
		}
		if err := writeManifest(tarWriter, manifest); err != nil {
			logger.Error("write backup manifest", "err", err)
			return
		}
		if err := tarWriter.Close(); err != nil {
			logger.Error("close backup tar", "err", err)
			return
		}
		if err := encoder.Close(); err != nil {
			logger.Error("close zstd encoder", "err", err)
			return
		}

		logger.Info("backup completed",
			"filename", filename,
			"duration", time.Since(start),
			"compressed_bytes", counter.written,
			"databases", manifest.Databases,
		)
	}
}

func listAllRoutes(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	_, _ = w.Write([]byte("GET /loggers\nPUT /loggers/<name>/<:level,int8>\nGET /backupdb?db=main|all&media=0|1\n"))
}

func withLoggingAndRecovery(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder := &statusRecorder{ResponseWriter: w}
		start := time.Now()

		defer func() {
			if rec := recover(); rec != nil {
				if recorder.status == 0 {
					recorder.WriteHeader(http.StatusInternalServerError)
				}
				logger.Error("inner http panic", "panic", rec, "stack", string(debug.Stack()))
			}

			if recorder.status == 0 {
				recorder.status = http.StatusOK
			}

			logger.Info("inner http request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", recorder.status,
				"duration", time.Since(start),
			)
		}()

		next.ServeHTTP(recorder, r)
	})
}

func pprofHandlers(mux *http.ServeMux) {
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
}

func buildHandler(logger *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/mars-counter", marsCounter)
	mux.HandleFunc("/dio-ban", dioBan)
	mux.HandleFunc("/backupdb", backupDBHandler(logger))
	mux.HandleFunc("/loggers", showLoggers)
	mux.HandleFunc("/loggers/", setLoggerLevel)
	mux.HandleFunc("/", listAllRoutes)
	pprofHandlers(mux)
	return withLoggingAndRecovery(logger, mux)
}

func resolveInnerHTTPAddr(envValue string) (addr string, enabled bool) {
	if strings.EqualFold(envValue, "OFF") {
		return "", false
	}
	if strings.TrimSpace(envValue) == "" {
		return innerHTTPDefaultAddr, true
	}
	if _, err := net.ResolveTCPAddr("tcp", envValue); err != nil {
		panic(err)
	}
	return envValue, true
}

func HttpListen4019() {
	logger := g.GetLogger("inner-http", slog.LevelWarn)
	addr, enabled := resolveInnerHTTPAddr(os.Getenv(innerHTTPEnvKey))
	if !enabled {
		logger.Info("inner http server disabled", "env", innerHTTPEnvKey)
		return
	}

	handler := buildHandler(logger)
	server := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 3 * time.Second,
	}

	logger.Info("inner http server listening", "addr", addr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("inner http server error", "err", err)
	}
}
