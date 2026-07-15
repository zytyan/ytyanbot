package geminiapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestGenerateContentRequestAndResponse(t *testing.T) {
	t.Parallel()
	var received map[string]any
	httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		require.Equal(t, "/v1beta/models/gemini-3-flash-preview:generateContent", r.URL.Path)
		require.Equal(t, "test-key", r.Header.Get("x-goog-api-key"))
		require.NoError(t, json.NewDecoder(r.Body).Decode(&received))
		return jsonResponse(http.StatusOK, `{
            "candidates":[{"content":{"role":"model","parts":[
              {"thought":true,"text":"private","thoughtSignature":"AQID"},
              {"executableCode":{"language":"PYTHON","code":"print(1)","id":"code-1"}},
              {"codeExecutionResult":{"outcome":"OUTCOME_OK","output":"1","id":"code-1"}},
              {"text":"answer"}
            ]},"groundingMetadata":{"webSearchQueries":["query"]}}],
            "usageMetadata":{"promptTokenCount":101,"cachedContentTokenCount":80,
              "candidatesTokenCount":4,"thoughtsTokenCount":3,"totalTokenCount":108}
		  }`), nil
	})}

	client, err := NewClient(context.Background(), &ClientConfig{
		APIKey: "test-key", Backend: BackendGeminiAPI,
		BaseURL: "https://example.test/v1beta", HTTPClient: httpClient,
	})
	require.NoError(t, err)
	temperature := float32(0.25)
	response, err := client.Models.GenerateContent(context.Background(), "gemini-3-flash-preview",
		[]*Content{{Role: RoleUser, Parts: []*Part{
			{Text: "inspect"}, {InlineData: &Blob{Data: []byte{1, 2, 3}, MIMEType: "image/jpeg"}},
		}}}, &GenerateContentConfig{
			SystemInstruction: NewContentFromText("system", RoleModel),
			Temperature:       &temperature,
			MaxOutputTokens:   256,
			Tools: []*Tool{{
				GoogleSearch: &GoogleSearch{}, CodeExecution: &ToolCodeExecution{},
			}},
			ThinkingConfig: &ThinkingConfig{IncludeThoughts: true, ThinkingLevel: ThinkingLevelLow},
		})
	require.NoError(t, err)

	require.Equal(t, "answer", response.Text())
	require.Equal(t, int32(80), response.UsageMetadata.CachedContentTokenCount)
	require.Equal(t, []byte{1, 2, 3}, response.Candidates[0].Content.Parts[0].ThoughtSignature)
	require.Equal(t, LanguagePython, response.Candidates[0].Content.Parts[1].ExecutableCode.Language)
	require.Equal(t, []string{"query"}, response.Candidates[0].GroundingMetadata.WebSearchQueries)

	require.Contains(t, received, "systemInstruction")
	require.Contains(t, received, "tools")
	require.NotContains(t, received, "thinkingConfig")
	generation := received["generationConfig"].(map[string]any)
	require.Equal(t, true, generation["thinkingConfig"].(map[string]any)["includeThoughts"])
	require.Equal(t, "LOW", generation["thinkingConfig"].(map[string]any)["thinkingLevel"])
	contents := received["contents"].([]any)
	parts := contents[0].(map[string]any)["parts"].([]any)
	require.Equal(t, "AQID", parts[1].(map[string]any)["inlineData"].(map[string]any)["data"])
}

func TestCountTokensAndAPIError(t *testing.T) {
	t.Parallel()
	requests := 0
	httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		if requests == 1 {
			require.Equal(t, "/models/gemini-3.1-flash-lite:countTokens", r.URL.Path)
			return jsonResponse(http.StatusOK, `{"totalTokens":42,"cachedContentTokenCount":9}`), nil
		}
		return jsonResponse(http.StatusBadRequest, `{"error":{"message":"bad request"}}`), nil
	})}

	client, err := NewClient(context.Background(), &ClientConfig{
		APIKey: "test-key", BaseURL: "https://example.test", HTTPClient: httpClient,
	})
	require.NoError(t, err)
	count, err := client.Models.CountTokens(context.Background(), "models/gemini-3.1-flash-lite",
		[]*Content{NewContentFromText("hello", RoleUser)}, nil)
	require.NoError(t, err)
	require.Equal(t, int32(42), count.TotalTokens)
	require.Equal(t, int32(9), count.CachedContentTokenCount)

	_, err = client.Models.CountTokens(context.Background(), "gemini-3.1-flash-lite", nil, nil)
	var apiError *APIError
	require.ErrorAs(t, err, &apiError)
	require.Equal(t, http.StatusBadRequest, apiError.StatusCode)
	require.Equal(t, "bad request", apiError.Message)
}

func TestCountTokensRejectsDeveloperAPIUnsupportedConfig(t *testing.T) {
	t.Parallel()
	client, err := NewClient(context.Background(), &ClientConfig{APIKey: "test"})
	require.NoError(t, err)
	_, err = client.Models.CountTokens(context.Background(), "gemini-3-flash-preview", nil,
		&CountTokensConfig{SystemInstruction: NewContentFromText("system", RoleModel)})
	require.ErrorContains(t, err, "does not support systemInstruction or tools")
}

func TestCachedContentsLifecycle(t *testing.T) {
	t.Parallel()
	requests := 0
	httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		require.Equal(t, "test-key", r.Header.Get("x-goog-api-key"))
		switch requests {
		case 1:
			require.Equal(t, http.MethodPost, r.Method)
			require.Equal(t, "/v1beta/cachedContents", r.URL.Path)
			var payload map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
			require.Equal(t, "models/gemini-3-flash-preview", payload["model"])
			require.Equal(t, "900s", payload["ttl"])
			require.Equal(t, "session-7", payload["displayName"])
			require.Contains(t, payload, "systemInstruction")
			require.Contains(t, payload, "tools")
			return jsonResponse(http.StatusOK, `{
                  "name":"cachedContents/cache-1","model":"models/gemini-3-flash-preview",
                  "expireTime":"2026-07-15T12:15:00Z","usageMetadata":{"totalTokenCount":5000}}`), nil
		case 2:
			require.Equal(t, http.MethodPatch, r.Method)
			require.Equal(t, "/v1beta/cachedContents/cache-1", r.URL.Path)
			require.Equal(t, "ttl", r.URL.Query().Get("updateMask"))
			var payload map[string]string
			require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
			require.Equal(t, "900s", payload["ttl"])
			return jsonResponse(http.StatusOK, `{
                  "name":"cachedContents/cache-1","expireTime":"2026-07-15T12:20:00Z",
                  "usageMetadata":{"totalTokenCount":5000}}`), nil
		case 3:
			require.Equal(t, http.MethodDelete, r.Method)
			require.Equal(t, "/v1beta/cachedContents/cache-1", r.URL.Path)
			return jsonResponse(http.StatusNoContent, ""), nil
		default:
			t.Fatalf("unexpected request %d", requests)
			return nil, nil
		}
	})}
	client, err := NewClient(context.Background(), &ClientConfig{
		APIKey: "test-key", BaseURL: "https://example.test/v1beta", HTTPClient: httpClient,
	})
	require.NoError(t, err)
	created, err := client.Caches.Create(context.Background(), "gemini-3-flash-preview",
		&CreateCachedContentConfig{
			DisplayName: "session-7", TTL: 15 * time.Minute,
			SystemInstruction: NewContentFromText("system", RoleModel),
			Contents:          []*Content{NewContentFromText("stable prefix", RoleUser)},
			Tools:             []*Tool{{GoogleSearch: &GoogleSearch{}}},
		})
	require.NoError(t, err)
	require.Equal(t, "cachedContents/cache-1", created.Name)
	require.Equal(t, int32(5000), created.UsageMetadata.TotalTokenCount)
	require.Equal(t, time.Date(2026, 7, 15, 12, 15, 0, 0, time.UTC), created.ExpireTime)

	updated, err := client.Caches.UpdateTTL(context.Background(), created.Name, 15*time.Minute)
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 7, 15, 12, 20, 0, 0, time.UTC), updated.ExpireTime)
	require.NoError(t, client.Caches.Delete(context.Background(), created.Name))
	require.Equal(t, 3, requests)
}

func TestCachedContentRejectsInvalidResourceName(t *testing.T) {
	t.Parallel()
	client, err := NewClient(context.Background(), &ClientConfig{APIKey: "test"})
	require.NoError(t, err)
	_, err = client.Caches.UpdateTTL(context.Background(), "models/not-a-cache", time.Minute)
	require.ErrorContains(t, err, "invalid Gemini cached content name")
	require.Error(t, client.Caches.Delete(context.Background(), "cachedContents/a/b"))
}
