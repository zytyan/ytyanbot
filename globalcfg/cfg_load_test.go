package g

import (
	"fmt"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNormalizeConfigSub2APIEnvironment(t *testing.T) {
	t.Setenv("SUB2API_API_KEY", "env-sub2api-key")
	t.Setenv("SUB2API_BASE_URL", "http://127.0.0.1:19090/antigravity/v1beta")
	cfg := Config{Sub2APIKey: "file-key", Sub2APIBaseURL: "http://file.invalid/antigravity/v1beta"}
	normalizeConfig(&cfg)
	assert.Equal(t, "env-sub2api-key", cfg.Sub2APIKey)
	assert.Equal(t, "http://127.0.0.1:19090/antigravity/v1beta", cfg.Sub2APIBaseURL)
}

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

	as.Equal("http://localhost:8081", cfg.TgApiUrl)
	as.False(cfg.DropPendingUpdates)
	as.Equal("ABCDEFGHIJKLMNOPQRST", cfg.GeminiKey)
	as.NotNil(cfg.GeminiExplicitCache)
	as.True(*cfg.GeminiExplicitCache)
	as.Empty(cfg.Sub2APIKey)
	as.Equal(DefaultSub2APIBaseURL, cfg.Sub2APIBaseURL)
	as.Empty(cfg.DeepSeekKey)
	as.Equal(DefaultDeepSeekBaseURL, cfg.DeepSeekBaseURL)

	as.Equal(int8(-1), cfg.LogLevel)
	as.Equal(":memory:", cfg.DatabasePath)
	as.Equal("ai-media", cfg.AIMediaPath)
	logger := GetLogger("test", -1)
	fmt.Println(logger)
	logger.Info("test logger")

	namedLogger := GetLogger("test-config-log-level", slog.LevelInfo)
	as.NotNil(namedLogger)
	as.Equal(slog.Level(-1), GetAllLoggers()["test-config-log-level"].Level.Level())
}
