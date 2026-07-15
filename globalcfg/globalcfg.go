package g

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"log/slog"
	"main/globalcfg/aiq"
	"main/globalcfg/q"
	"main/helpers/azure"
	"main/helpers/meilisearch"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
	_ "github.com/mattn/go-sqlite3"
)

type Azure struct {
	Endpoint string `koanf:"endpoint"`
	ApiKey   string `koanf:"api-key"`
}

type OcrConfig struct {
	Azure    `koanf:",squash"`
	ApiVer   string `koanf:"api-ver"`
	Language string `koanf:"language"`
	Features string `koanf:"features"`
}

type MeiliConfig struct {
	BaseUrl    string `koanf:"base-url"`
	IndexName  string `koanf:"index-name"`
	PrimaryKey string `koanf:"primary-key"`
	MasterKey  string `koanf:"master-key"`
}

type Config struct {
	BotToken            string      `koanf:"bot-token"`
	God                 int64       `koanf:"god"`
	MyChats             []int64     `koanf:"my-chats"`
	AIChats             []int64     `koanf:"ai-chats"`
	MeiliConfig         MeiliConfig `koanf:"meili-config"`
	ContentModerator    Azure       `koanf:"content-moderator"`
	Ocr                 OcrConfig   `koanf:"ocr"`
	QrScanUrl           string      `koanf:"qr-scan-url"`
	SaveMessage         bool        `koanf:"save-message"`
	TgApiUrl            string      `koanf:"tg-api-url"`
	DropPendingUpdates  bool        `koanf:"drop-pending-updates"`
	LogLevel            int8        `koanf:"log-level"`
	DatabasePath        string      `koanf:"database-path"`
	AIMediaPath         string      `koanf:"ai-media-path"`
	GeminiKey           string      `koanf:"gemini-key"`
	GeminiExplicitCache *bool       `koanf:"gemini-explicit-cache"`
	DeepSeekKey         string      `koanf:"deepseek-key"`
	DeepSeekBaseURL     string      `koanf:"deepseek-base-url"`
	BackendAddr         string      `koanf:"backend-addr"`
	MsgDbPath           string      `koanf:"msg-db-path"`
	MeiliWalDbPath      string      `koanf:"meili-wal-db-path"`
	MeiliWalBatchSize   int         `koanf:"meili-wal-batch-size"`

	LogFile  string `koanf:"log-file"`
	NoStdout bool   `koanf:"no-stdout"`
}

const (
	DefaultMeiliWalDbPath    = "meili-wal.db"
	DefaultMeiliWalBatchSize = 500
	DefaultDeepSeekBaseURL   = "https://api.deepseek.com"
	DefaultBackendAddr       = "127.0.0.1:4021"
)

var gMu sync.Mutex
var config atomic.Pointer[Config]

type PtrLinkedCfg[T any] struct {
	cfg     *Config
	ptr     *T
	fn      func(new *Config) *T
	checker func(old, new *Config) bool
}

func (p *PtrLinkedCfg[T]) Get() *T {
	cfg := GetConfig()
	if p.ptr == nil || p.cfg != cfg {
		gMu.Lock()
		defer gMu.Unlock()
		if p.checker(p.cfg, cfg) {
			p.cfg = cfg
			p.ptr = p.fn(p.cfg)
		}
	}
	return p.ptr
}
func NewPtrLinkedCfg[T any](checker func(old, new *Config) bool, getter func(new *Config) *T) PtrLinkedCfg[T] {
	return PtrLinkedCfg[T]{
		fn: getter,
		checker: func(old, new *Config) bool {
			if new == nil {
				return false
			}
			if old == nil {
				return true
			}
			return checker(old, new)
		},
	}
}

var ocr = NewPtrLinkedCfg(
	func(old, new *Config) bool {
		return old.Ocr != new.Ocr
	},
	func(new *Config) *azure.Ocr {
		return &azure.Ocr{
			Client: *azure.NewClient(
				new.Ocr.Endpoint,
				new.Ocr.ApiKey,
				azure.OcrPath,
			),
			ApiVer:   new.Ocr.ApiVer,
			Language: new.Ocr.Language,
			Features: new.Ocr.Features,
		}
	},
)

var moderator = NewPtrLinkedCfg(
	func(old, new *Config) bool {
		return old.ContentModerator != new.ContentModerator
	},
	func(new *Config) *azure.ModeratorV2 {
		return &azure.ModeratorV2{
			Client: *azure.NewClient(
				new.ContentModerator.Endpoint,
				new.ContentModerator.ApiKey,
				azure.ContentModeratorV2Path,
			),
			Categories: []string{azure.ModerateV2CatSexual},
			OutputType: "FourSeverityLevels",
		}
	},
)

var meili = NewPtrLinkedCfg(
	func(old, new *Config) bool {
		return old.MeiliConfig != new.MeiliConfig
	},
	func(new *Config) *meilisearch.Client {
		return meilisearch.NewMeiliClient(
			new.MeiliConfig.BaseUrl,
			new.MeiliConfig.IndexName,
			new.MeiliConfig.MasterKey,
			new.MeiliConfig.PrimaryKey,
		)
	},
)

var loggers = make(map[string]LoggerWithLevel)

func loadConfig(k *koanf.Koanf, provider *file.File) (*Config, error) {
	err := k.Load(provider, yaml.Parser())
	if err != nil {
		return nil, err
	}
	var newCfg Config
	err = k.Unmarshal("", &newCfg)
	if err != nil {
		return nil, err
	}
	normalizeConfig(&newCfg)
	return &newCfg, nil
}

func normalizeConfig(cfg *Config) {
	if value := os.Getenv("YTYAN_BOT_TOKEN"); value != "" {
		cfg.BotToken = value
	}
	if value := os.Getenv("GEMINI_API_KEY"); value != "" {
		cfg.GeminiKey = value
	}
	if value := os.Getenv("DEEPSEEK_API_KEY"); value != "" {
		cfg.DeepSeekKey = value
	}
	if value := os.Getenv("YTYAN_BACKEND_ADDR"); value != "" {
		cfg.BackendAddr = value
	}
	if cfg.MeiliWalDbPath == "" {
		cfg.MeiliWalDbPath = DefaultMeiliWalDbPath
	}
	if cfg.MeiliWalBatchSize <= 0 {
		cfg.MeiliWalBatchSize = DefaultMeiliWalBatchSize
	}
	if cfg.DeepSeekBaseURL == "" {
		cfg.DeepSeekBaseURL = DefaultDeepSeekBaseURL
	}
	if cfg.BackendAddr == "" {
		cfg.BackendAddr = DefaultBackendAddr
	}
	if cfg.AIMediaPath == "" {
		if cfg.DatabasePath == "" || cfg.DatabasePath == ":memory:" {
			cfg.AIMediaPath = "ai-media"
		} else {
			cfg.AIMediaPath = filepath.Join(filepath.Dir(cfg.DatabasePath), "ai-media")
		}
	}
}

func getCfgFilename() string {
	cfgFile := os.Getenv("YTYAN_CONFIG_FILE")
	if cfgFile == "" {
		if testing.Testing() {
			return filepath.Join(mustGetProjectRootDir(), "config.example.yaml")
		}
		cfgFile = "config.yaml"
	}
	return cfgFile
}

func InitConfig() {
	k := koanf.New(".")
	cfgFile := getCfgFilename()
	provider := file.Provider(cfgFile)
	cfg, err := loadConfig(k, provider)
	if err != nil {
		panic(fmt.Sprintf("load config file failed: %v", err))
	}
	err = provider.Watch(func(event any, err error) {
		if err != nil {
			log.Printf("watch error: %v", err)
			return
		}
		cfg2, err := loadConfig(k, provider)
		if err != nil {
			log.Printf("load config file failed: %v", err)
			return
		}
		oldCfg := config.Load()
		if oldCfg.DatabasePath != cfg2.DatabasePath || oldCfg.MsgDbPath != cfg2.MsgDbPath ||
			oldCfg.MeiliWalDbPath != cfg2.MeiliWalDbPath || oldCfg.AIMediaPath != cfg2.AIMediaPath {
			log.Printf("database path cannot be changed without restart, old: %s, new: %s", oldCfg.DatabasePath, cfg2.DatabasePath)
			log.Printf("message database path cannot be changed without restart, old: %s, new: %s", oldCfg.MsgDbPath, cfg2.MsgDbPath)
			log.Printf("meili wal database path cannot be changed without restart, old: %s, new: %s", oldCfg.MeiliWalDbPath, cfg2.MeiliWalDbPath)
			log.Printf("AI media path cannot be changed without restart, old: %s, new: %s", oldCfg.AIMediaPath, cfg2.AIMediaPath)
			return
		}
		config.Store(cfg2)
		SetAllLoggerLevels(slog.Level(cfg2.LogLevel))
		log.Printf("config changed at %s", time.Now())
	})
	if err != nil {
		panic(err)
	}
	config.Store(cfg)
	SetAllLoggerLevels(slog.Level(cfg.LogLevel))
	db = getSqliteConn(config.Load().DatabasePath)
	msgDb = getSqliteConn(config.Load().MsgDbPath)
	meiliWalDbPath := config.Load().MeiliWalDbPath
	if testing.Testing() {
		meiliWalDbPath = ":memory:"
	}
	meiliWalDb = getSqliteConn(meiliWalDbPath)
	if err := InitMeiliWalDbSchema(meiliWalDb); err != nil {
		panic(err)
	}
	if testing.Testing() && config.Load().DatabasePath == ":memory:" {
		initMainDatabaseInMemory(db)
		_ = msgDb.Close()
		msgDb = db
	}
	if err := runDatabaseMigrations(db); err != nil {
		panic(err)
	}
	Q, err = q.PrepareWithLogger(context.Background(), db, nil)
	if err != nil {
		panic(err)
	}
	AIQ, err = aiq.Prepare(context.Background(), db)
	if err != nil {
		panic(err)
	}
	slog.Info("")
}

func GetConfig() *Config {
	return config.Load()
}

func GeminiExplicitCacheEnabled() bool {
	cfg := GetConfig()
	return cfg.GeminiExplicitCache == nil || *cfg.GeminiExplicitCache
}

func Ocr() *azure.Ocr {
	return ocr.Get()
}

func Moderator() *azure.ModeratorV2 {
	return moderator.Get()
}

func Meili() *meilisearch.Client {
	return meili.Get()
}

var db *sql.DB
var msgDb *sql.DB
var meiliWalDb *sql.DB

var Q *q.Queries
var AIQ *aiq.Queries

const meiliWalSchema = `
CREATE TABLE IF NOT EXISTS meili_wal
(
	id      INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
	content TEXT    NOT NULL CHECK (json_valid(content))
);`

func InitMeiliWalDbSchema(database *sql.DB) error {
	_, err := database.Exec(meiliWalSchema)
	return err
}

func getSqliteConn(dbPath string) *sql.DB {
	check := func(_ sql.Result, e error) {
		if e != nil {
			panic(e)
		}
	}
	dsn := dbPath
	if dbPath != ":memory:" {
		u := &url.URL{Scheme: "file", Path: dbPath}
		query := u.Query()
		query.Set("_foreign_keys", "on")
		query.Set("_busy_timeout", "5000")
		query.Set("_journal_mode", "WAL")
		query.Set("_synchronous", "NORMAL")
		query.Set("_cache_size", "-32768")
		u.RawQuery = query.Encode()
		dsn = u.String()
	}
	d, err := sql.Open("sqlite3", dsn)
	if err != nil {
		panic(err)
	}
	if dbPath == ":memory:" {
		d.SetMaxOpenConns(1)
		d.SetMaxIdleConns(1)
		check(d.Exec(`PRAGMA foreign_keys=ON;
			PRAGMA busy_timeout=5000;
			PRAGMA synchronous=NORMAL;`))
	} else {
		d.SetMaxOpenConns(4)
		d.SetMaxIdleConns(4)
	}
	check(d.Exec(`PRAGMA wal_autocheckpoint=1000;
						PRAGMA mmap_size=67108864;
						PRAGMA optimize;`))
	return d
}

func RawMainDb() *sql.DB {
	return db
}

func RawMsgsDb() *sql.DB {
	return msgDb
}

func RawMeiliWalDb() *sql.DB {
	return meiliWalDb
}

func init() {
	InitConfig()
}
