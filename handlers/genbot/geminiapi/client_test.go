package geminiapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

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
