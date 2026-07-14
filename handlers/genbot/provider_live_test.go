//go:build integration

package genbot

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	g "main/globalcfg"

	"github.com/stretchr/testify/require"
	"google.golang.org/genai"
)

const liveCacheSessionID = int64(103)

func loadLiveSession(t *testing.T) *GeminiSession {
	t.Helper()
	contents, err := g.Q.GetAllMsgInSession(context.Background(), liveCacheSessionID, geminiSessionContentLimit)
	require.NoError(t, err)
	require.NotEmpty(t, contents)
	return &GeminiSession{
		Contents: contents,
		Provider: ProviderGemini,
		Model:    ModelGeminiFlash,
	}
}

func requireLiveAI(t *testing.T) {
	t.Helper()
	if os.Getenv("LIVE_AI_TEST") != "1" {
		t.Skip("set LIVE_AI_TEST=1 to run paid provider checks")
	}
}

func TestLiveGeminiImplicitCache(t *testing.T) {
	requireLiveAI(t)
	session := loadLiveSession(t)
	client := getGenAiClient()
	contents := session.ToGenaiContents("cache probe")
	count, err := client.Models.CountTokens(context.Background(), session.Model, contents, nil)
	require.NoError(t, err)
	require.GreaterOrEqual(t, count.TotalTokens, int32(4096))

	config := &genai.GenerateContentConfig{
		SystemInstruction: genai.NewContentFromText("缓存验证；只回答 OK。", genai.RoleModel),
		MaxOutputTokens:   16,
	}
	first, err := generateGemini(context.Background(), session, "cache probe A", config)
	require.NoError(t, err)
	require.NotNil(t, first.UsageMetadata)
	time.Sleep(3 * time.Second)
	second, err := generateGemini(context.Background(), session, "cache probe B", config)
	require.NoError(t, err)
	require.NotNil(t, second.UsageMetadata)
	t.Logf("gemini prompt=%d cached=%d", second.UsageMetadata.PromptTokenCount, second.UsageMetadata.CachedContentTokenCount)
	require.Greater(t, second.UsageMetadata.CachedContentTokenCount, int32(0))
}

func TestLiveDeepSeekCache(t *testing.T) {
	requireLiveAI(t)
	require.NotEmpty(t, g.GetConfig().DeepSeekKey)
	commonPrefix := strings.Repeat("这是无隐私数据的缓存测试公共前缀。", 3000)
	messages := []deepSeekMessage{
		{Role: "system", Content: "缓存验证；只回答 OK。"},
		{Role: "user", Content: commonPrefix},
		{Role: "user", Content: "cache probe A"},
	}
	request := deepSeekRequest{
		Model: ModelDeepSeekFlash, Messages: messages, MaxTokens: 32,
		Thinking: &deepSeekThinking{Type: "disabled"},
	}
	client := &http.Client{Timeout: 15 * time.Minute}
	first, err := callDeepSeek(context.Background(), client, g.GetConfig().DeepSeekBaseURL, g.GetConfig().DeepSeekKey, request)
	require.NoError(t, err)
	require.NotEmpty(t, first.Text)

	var cached int64
	for attempt := 0; attempt < 2 && cached == 0; attempt++ {
		time.Sleep(5 * time.Second)
		request.Messages[len(request.Messages)-1].Content = "cache probe B"
		result, callErr := callDeepSeek(context.Background(), client, g.GetConfig().DeepSeekBaseURL, g.GetConfig().DeepSeekKey, request)
		require.NoError(t, callErr)
		cached = result.Usage.CachedInputTokens
		t.Logf("deepseek prompt=%d cached=%d", result.Usage.InputTokens, cached)
	}
	require.Greater(t, cached, int64(0))
}
