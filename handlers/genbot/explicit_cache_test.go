package genbot

import (
	"context"
	"encoding/json"
	"io"
	g "main/globalcfg"
	"main/globalcfg/q"
	genai "main/handlers/genbot/geminiapi"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func explicitCacheTestClient(t *testing.T, transport roundTripFunc) *genai.Client {
	t.Helper()
	client, err := genai.NewClient(context.Background(), &genai.ClientConfig{
		APIKey: "secret", BaseURL: "https://example.test/v1beta",
		HTTPClient: &http.Client{Transport: transport},
	})
	require.NoError(t, err)
	return client
}

func explicitCacheJSONResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header),
		Body: io.NopCloser(strings.NewReader(body))}
}

func TestExplicitCacheStartsAfterImplicitWindowAndUsesOnlyTail(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	stableUser := interactionTestContent(1, genai.RoleUser, "stable prefix")
	stableUser.SentTime.Time = now.Add(-10 * time.Minute)
	stableModel := interactionTestContent(2, genai.RoleModel, "stable answer")
	stableModel.SentTime.Time = now.Add(-9 * time.Minute)
	current := interactionTestContent(3, genai.RoleUser, "current question")
	current.SentTime.Time = now
	session := &GeminiSession{
		Contents: []q.GeminiContent{stableUser, stableModel}, TmpContents: []q.GeminiContent{current},
		AssistantPayloads: map[int64]g.AIAssistantPayload{}, Provider: ProviderGemini, Model: ModelGeminiFlash,
	}
	config := &genai.GenerateContentConfig{
		Tools:          []*genai.Tool{{GoogleSearch: &genai.GoogleSearch{}}},
		ThinkingConfig: &genai.ThinkingConfig{IncludeThoughts: true, ThinkingLevel: genai.ThinkingLevelLow},
	}
	requests := 0
	client := explicitCacheTestClient(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		switch requests {
		case 1:
			require.Equal(t, "/v1beta/models/gemini-3-flash-preview:countTokens", request.URL.Path)
			return explicitCacheJSONResponse(http.StatusOK, `{"totalTokens":5000}`), nil
		case 2:
			require.Equal(t, "/v1beta/cachedContents", request.URL.Path)
			var payload map[string]any
			require.NoError(t, json.NewDecoder(request.Body).Decode(&payload))
			require.Equal(t, "900s", payload["ttl"])
			require.Contains(t, payload, "systemInstruction")
			require.Contains(t, payload, "tools")
			return explicitCacheJSONResponse(http.StatusOK,
				`{"name":"cachedContents/session-1","expireTime":"2026-07-15T12:15:00Z","usageMetadata":{"totalTokenCount":5000}}`), nil
		case 3:
			require.Equal(t, "/v1beta/models/gemini-3-flash-preview:generateContent", request.URL.Path)
			var payload map[string]any
			require.NoError(t, json.NewDecoder(request.Body).Decode(&payload))
			require.Equal(t, "cachedContents/session-1", payload["cachedContent"])
			require.NotContains(t, payload, "systemInstruction")
			require.NotContains(t, payload, "tools")
			contents := payload["contents"].([]any)
			require.Len(t, contents, 1)
			require.Contains(t, string(mustJSON(contents)), "current question")
			require.NotContains(t, string(mustJSON(contents)), "stable prefix")
			return explicitCacheJSONResponse(http.StatusOK, `{
                  "candidates":[{"content":{"role":"model","parts":[{"text":"answer"}]}}],
                  "usageMetadata":{"promptTokenCount":5010,"cachedContentTokenCount":5000,"candidatesTokenCount":2}}`), nil
		default:
			t.Fatalf("unexpected request %d", requests)
			return nil, nil
		}
	}))
	result, used, err := tryGeminiExplicitCache(context.Background(), client, session, "system", config,
		session.prepareRequestWindow(), now)
	require.NoError(t, err)
	require.True(t, used)
	require.Equal(t, "answer", result.DisplayText)
	require.Equal(t, int64(5000), result.Usage.CachedInputTokens)
	require.Equal(t, "cachedContents/session-1", session.GeminiCache.Name)
	require.Equal(t, int64(1), session.GeminiCache.StartMsgID)
	require.Equal(t, int64(2), session.GeminiCache.EndMsgID)
	require.Empty(t, session.GeminiInteractionID)
	require.Equal(t, 3, requests)
}

func TestExplicitCacheRecentSessionKeepsInteractionsPath(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	stable := interactionTestContent(1, genai.RoleModel, "recent")
	stable.SentTime.Time = now.Add(-2 * time.Minute)
	current := interactionTestContent(2, genai.RoleUser, "current")
	session := &GeminiSession{Contents: []q.GeminiContent{stable}, TmpContents: []q.GeminiContent{current},
		Model: ModelGeminiFlash, Provider: ProviderGemini}
	client := explicitCacheTestClient(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		t.Fatalf("recent sessions must not call explicit cache API: %s", request.URL)
		return nil, nil
	}))
	result, used, err := tryGeminiExplicitCache(context.Background(), client, session, "system",
		&genai.GenerateContentConfig{}, session.prepareRequestWindow(), now)
	require.NoError(t, err)
	require.False(t, used)
	require.Nil(t, result)
}

func TestExplicitCacheCreationFailureFallsBackWithoutBreakingSession(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	stable := interactionTestContent(1, genai.RoleModel, "old long history")
	stable.SentTime.Time = now.Add(-10 * time.Minute)
	current := interactionTestContent(2, genai.RoleUser, "current")
	session := &GeminiSession{Contents: []q.GeminiContent{stable}, TmpContents: []q.GeminiContent{current},
		Model: ModelGeminiFlash, Provider: ProviderGemini}
	requests := 0
	client := explicitCacheTestClient(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		if requests == 1 {
			return explicitCacheJSONResponse(http.StatusOK, `{"totalTokens":5000}`), nil
		}
		require.Equal(t, "/v1beta/cachedContents", request.URL.Path)
		return explicitCacheJSONResponse(http.StatusBadRequest,
			`{"error":{"message":"cache unsupported"}}`), nil
	}))
	result, used, err := tryGeminiExplicitCache(context.Background(), client, session, "system",
		&genai.GenerateContentConfig{}, session.prepareRequestWindow(), now)
	require.NoError(t, err)
	require.False(t, used, "the caller must continue through Interactions when optional cache creation fails")
	require.Nil(t, result)
	require.Empty(t, session.GeminiCache.Name)
	require.Equal(t, 2, requests)
}

func TestExplicitCacheRenewsNearExpiryWithoutRecreating(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	config := &genai.GenerateContentConfig{}
	stable := interactionTestContent(1, genai.RoleModel, "stable")
	current := interactionTestContent(2, genai.RoleUser, "current")
	fingerprint := explicitCacheFingerprint(ModelGeminiFlash, "system", config.Tools)
	session := &GeminiSession{
		Contents: []q.GeminiContent{stable}, TmpContents: []q.GeminiContent{current},
		Provider: ProviderGemini, Model: ModelGeminiFlash,
		GeminiCache: geminiExplicitCacheState{
			Name: "cachedContents/current", ExpireTime: now.Add(4 * time.Minute),
			StartMsgID: 1, EndMsgID: 1, TokenCount: 5000, Fingerprint: fingerprint,
		},
	}
	requests := 0
	client := explicitCacheTestClient(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		require.Equal(t, http.MethodPatch, request.Method)
		require.Equal(t, "ttl", request.URL.Query().Get("updateMask"))
		return explicitCacheJSONResponse(http.StatusOK,
			`{"name":"cachedContents/current","expireTime":"2026-07-15T12:15:00Z","usageMetadata":{"totalTokenCount":5000}}`), nil
	}))
	state, endIndex, ok := ensureExplicitCache(context.Background(), client, session, "system", config,
		session.prepareRequestWindow(), now)
	require.True(t, ok)
	require.Equal(t, 1, endIndex)
	require.Equal(t, now.Add(15*time.Minute), state.ExpireTime)
	require.Equal(t, 1, requests)
}

func TestExplicitCacheRefreshesAtFiftyTailMessages(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	config := &genai.GenerateContentConfig{Tools: []*genai.Tool{{GoogleSearch: &genai.GoogleSearch{}}}}
	contents := make([]q.GeminiContent, 52)
	for index := range contents {
		contents[index] = interactionTestContent(int64(index+1), genai.RoleUser, "history")
		contents[index].SentTime.Time = now.Add(-10 * time.Minute)
	}
	current := interactionTestContent(53, genai.RoleUser, "current")
	fingerprint := explicitCacheFingerprint(ModelGeminiFlash, "system", config.Tools)
	session := &GeminiSession{
		Contents: contents, TmpContents: []q.GeminiContent{current}, Provider: ProviderGemini, Model: ModelGeminiFlash,
		GeminiCache: geminiExplicitCacheState{
			Name: "cachedContents/old", ExpireTime: now.Add(10 * time.Minute),
			StartMsgID: 1, EndMsgID: 2, TokenCount: 5000, Fingerprint: fingerprint,
		},
	}
	requests := 0
	client := explicitCacheTestClient(t, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		switch requests {
		case 1:
			require.Contains(t, request.URL.Path, ":countTokens")
			return explicitCacheJSONResponse(http.StatusOK, `{"totalTokens":5200}`), nil
		case 2:
			require.Equal(t, "/v1beta/cachedContents", request.URL.Path)
			return explicitCacheJSONResponse(http.StatusOK,
				`{"name":"cachedContents/new","expireTime":"2026-07-15T12:15:00Z","usageMetadata":{"totalTokenCount":5200}}`), nil
		case 3:
			require.Equal(t, http.MethodDelete, request.Method)
			require.Equal(t, "/v1beta/cachedContents/old", request.URL.Path)
			return explicitCacheJSONResponse(http.StatusNoContent, ""), nil
		default:
			t.Fatalf("unexpected request %d", requests)
			return nil, nil
		}
	}))
	state, endIndex, ok := ensureExplicitCache(context.Background(), client, session, "system", config,
		session.prepareRequestWindow(), now)
	require.True(t, ok)
	require.Equal(t, "cachedContents/new", state.Name)
	require.Equal(t, int64(52), state.EndMsgID)
	require.Equal(t, 52, endIndex)
	require.Equal(t, 3, requests)
}

func mustJSON(value any) []byte {
	data, _ := json.Marshal(value)
	return data
}
