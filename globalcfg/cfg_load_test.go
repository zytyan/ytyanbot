package g

import (
	"fmt"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLoadConfig(t *testing.T) {
	as := assert.New(t)
	cfg := GetConfig()
	as.NotNil(cfg)

	as.Equal("554277510:AAEKxRdcRfhEjtSIfxpaYtL19XFgdDcY23U", cfg.BotToken)
	as.Equal(int64(123456789), cfg.God)
	as.Equal([]int64{-1001471592463}, cfg.MyChats)
	as.Nil(cfg.AIChats)

	as.Equal("https://example-ocr.cognitiveservices.azure.com", cfg.ContentModerator.Endpoint)
	as.Equal("1234567890abcdef", cfg.ContentModerator.ApiKey)
	as.Equal("https://example-ocr.cognitiveservices.azure.com", cfg.Ocr.Endpoint)
	as.Equal("1234567890abcdef", cfg.Ocr.ApiKey)
	as.Equal("2023-10-01", cfg.Ocr.ApiVer)
	as.Equal("", cfg.Ocr.Language)
	as.Equal("Read", cfg.Ocr.Features)

	as.Equal("http://localhost:4023/scanqr", cfg.QrScanUrl)
	as.True(cfg.SaveMessage)
	as.Equal("http://localhost:8081", cfg.TgApiUrl)
	as.False(cfg.DropPendingUpdates)
	as.Equal("ABCDEFGHIJKLMNOPQRST", cfg.GeminiKey)
	as.NotNil(cfg.GeminiExplicitCache)
	as.True(*cfg.GeminiExplicitCache)
	as.Empty(cfg.DeepSeekKey)
	as.Equal(DefaultDeepSeekBaseURL, cfg.DeepSeekBaseURL)
	as.Equal(DefaultBackendAddr, cfg.BackendAddr)

	as.Equal("http://localhost:7700", cfg.MeiliConfig.BaseUrl)
	as.Equal("tgmsgs", cfg.MeiliConfig.IndexName)
	as.Equal("mongo_id", cfg.MeiliConfig.PrimaryKey)
	as.Equal("ABCDEFGHIJKLMNOPQRST", cfg.MeiliConfig.MasterKey)

	as.Equal(int8(-1), cfg.LogLevel)
	as.Equal(":memory:", cfg.DatabasePath)
	as.Equal(":memory:", cfg.MsgDbPath)
	as.Equal("meili-wal.db", cfg.MeiliWalDbPath)
	as.Equal(500, cfg.MeiliWalBatchSize)
	logger := GetLogger("test", -1)
	fmt.Println(logger)
	logger.Info("test logger")

	namedLogger := GetLogger("test-config-log-level", slog.LevelInfo)
	as.NotNil(namedLogger)
	as.Equal(slog.Level(-1), GetAllLoggers()["test-config-log-level"].Level.Level())
}
