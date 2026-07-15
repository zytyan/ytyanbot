package genbot

import (
	"context"
	"database/sql"
	"encoding/base64"
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

func interactionTestContent(id int64, role string, text string) q.GeminiContent {
	return q.GeminiContent{
		SessionID: 1, ChatID: -1, MsgID: id, Role: role, Username: "tester", MsgType: "text",
		SentTime: q.UnixTime{Time: time.Unix(id, 0)},
		Text:     sql.NullString{String: text, Valid: text != ""}, UserID: 1,
	}
}

func successfulInteractionResponse(id string) string {
	return `{"id":"` + id + `","status":"completed","steps":[` +
		`{"type":"thought","signature":"sig","summary":[{"type":"text","text":"summary"}]},` +
		`{"type":"google_search_call","id":"search-1","arguments":{"queries":["query"]}},` +
		`{"type":"google_search_result","call_id":"search-1","result":{"search_suggestions":"widget"}},` +
		`{"type":"code_execution_call","id":"code-1","arguments":{"code":"print(1)","language":"python"}},` +
		`{"type":"code_execution_result","call_id":"code-1","result":"1"},` +
		`{"type":"model_output","status":"done","content":[{"type":"text","text":"final answer","future":{"x":1}}]}],` +
		`"usage":{"total_input_tokens":5000,"total_cached_tokens":4096,"total_output_tokens":20,"total_thought_tokens":7,"total_tool_use_tokens":9}}`
}

func TestGeminiInteractionStatefulRequestAndRawSteps(t *testing.T) {
	oldUser := interactionTestContent(1, genai.RoleUser, "old")
	oldModel := interactionTestContent(2, genai.RoleModel, "old answer")
	current := interactionTestContent(3, genai.RoleUser, "current")
	oldSteps := []byte(`[{"type":"model_output","content":[{"type":"text","text":"old answer"}]}]`)
	session := &GeminiSession{
		Contents: []q.GeminiContent{oldUser, oldModel}, TmpContents: []q.GeminiContent{current},
		AssistantPayloads: map[int64]g.AIAssistantPayload{
			2: {MsgID: 2, Provider: ProviderGemini, Format: PayloadFormatGeminiInteractionSteps, Payload: oldSteps},
		},
		Provider: ProviderGemini, Model: ModelGeminiFlash, GeminiInteractionID: "int-old",
	}
	config := &genai.GenerateContentConfig{Tools: []*genai.Tool{{
		GoogleSearch: &genai.GoogleSearch{}, CodeExecution: &genai.ToolCodeExecution{},
	}}, ThinkingConfig: &genai.ThinkingConfig{IncludeThoughts: true, ThinkingLevel: genai.ThinkingLevelLow}}
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		require.Equal(t, "secret", request.Header.Get("x-goog-api-key"))
		require.Equal(t, geminiInteractionsRevision, request.Header.Get("Api-Revision"))
		var payload interactionRequest
		require.NoError(t, json.NewDecoder(request.Body).Decode(&payload))
		require.True(t, payload.Store)
		require.Equal(t, "int-old", payload.PreviousInteractionID)
		require.Equal(t, "system", payload.SystemInstruction)
		require.Len(t, payload.Tools, 2)
		require.Equal(t, "auto", payload.GenerationConfig["thinking_summaries"])
		require.Equal(t, "low", payload.GenerationConfig["thinking_level"])
		require.Len(t, payload.Input, 1, "stateful requests only send the current user turn")
		require.Contains(t, string(payload.Input[0]), "current")
		require.NotContains(t, string(payload.Input[0]), "old answer")
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header),
			Body: io.NopCloser(strings.NewReader(successfulInteractionResponse("int-new")))}, nil
	})}

	window := session.prepareRequestWindow()
	result, err := generateGeminiWithInteractionsClient(context.Background(), session, "system", config,
		window, client, "https://example.test/v1beta/interactions", "secret")
	require.NoError(t, err)
	require.Equal(t, "int-new", result.InteractionID)
	require.Equal(t, "final answer", result.DisplayText)
	require.Equal(t, int64(4096), result.Usage.CachedInputTokens)
	require.Equal(t, int64(27), result.Usage.OutputTokens)
	require.Equal(t, PayloadFormatGeminiInteractionSteps, result.AssistantPayloadFormat)
	require.Contains(t, string(result.AssistantPayload), `"type":"thought"`)
	require.Contains(t, string(result.AssistantPayload), `"type":"google_search_call"`)
	require.Contains(t, string(result.AssistantPayload), `"type":"code_execution_result"`)
	require.Contains(t, string(result.AssistantPayload), `"future":{"x":1}`)
	require.NotContains(t, result.DisplayText, "summary")
}

func TestGeminiInteractionInvalidPreviousIDRebuildsFromWindow(t *testing.T) {
	session := &GeminiSession{
		Contents: []q.GeminiContent{
			interactionTestContent(1, genai.RoleUser, "old"),
			interactionTestContent(2, genai.RoleModel, "old answer"),
		},
		TmpContents:       []q.GeminiContent{interactionTestContent(3, genai.RoleUser, "current")},
		AssistantPayloads: map[int64]g.AIAssistantPayload{},
		Provider:          ProviderGemini, Model: ModelGeminiFlash, GeminiInteractionID: "expired",
	}
	attempt := 0
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		attempt++
		var payload interactionRequest
		require.NoError(t, json.NewDecoder(request.Body).Decode(&payload))
		if attempt == 1 {
			require.Equal(t, "expired", payload.PreviousInteractionID)
			require.Len(t, payload.Input, 1)
			return &http.Response{StatusCode: http.StatusNotFound, Header: make(http.Header),
				Body: io.NopCloser(strings.NewReader(`{"error":{"message":"interaction expired"}}`))}, nil
		}
		require.Empty(t, payload.PreviousInteractionID)
		require.Len(t, payload.Input, 3)
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header),
			Body: io.NopCloser(strings.NewReader(successfulInteractionResponse("rebuilt")))}, nil
	})}
	result, err := generateGeminiWithInteractionsClient(context.Background(), session, "system",
		&genai.GenerateContentConfig{}, session.prepareRequestWindow(), client, "https://example.test", "secret")
	require.NoError(t, err)
	require.Equal(t, 2, attempt)
	require.Equal(t, "rebuilt", result.InteractionID)
}

func TestInteractionMediaBlocks(t *testing.T) {
	photo := interactionTestContent(1, genai.RoleUser, "caption")
	photo.MsgType = "photo"
	photo.Blob = []byte("image")
	photo.MimeType = sql.NullString{String: "image/jpeg", Valid: true}
	step, err := userInputInteractionStep(&photo)
	require.NoError(t, err)
	require.Contains(t, string(step), `"type":"image"`)
	require.Contains(t, string(step), `"mime_type":"image/jpeg"`)
	require.Contains(t, string(step), `"data":"aW1hZ2U="`)

	video := interactionTestContent(2, genai.RoleUser, "caption")
	video.MsgType = "video"
	video.Blob = []byte("video")
	video.MimeType = sql.NullString{String: "video/mp4", Valid: true}
	step, err = userInputInteractionStep(&video)
	require.NoError(t, err)
	require.Contains(t, string(step), `"type":"video"`)
}

func TestLossyInteractionRebuildDropsHistoricalMediaAndEncryptedSteps(t *testing.T) {
	purePhoto := interactionTestContent(1, genai.RoleUser, "")
	purePhoto.MsgType = "photo"
	purePhoto.Blob = []byte("old-pure-image")
	purePhoto.MimeType = sql.NullString{String: "image/jpeg", Valid: true}
	captionedPhoto := interactionTestContent(2, genai.RoleUser, "old caption")
	captionedPhoto.MsgType = "photo"
	captionedPhoto.Blob = []byte("old-caption-image")
	captionedPhoto.MimeType = sql.NullString{String: "image/jpeg", Valid: true}
	oldModel := interactionTestContent(3, genai.RoleModel, "portable answer")
	currentPhoto := interactionTestContent(4, genai.RoleUser, "current caption")
	currentPhoto.MsgType = "photo"
	currentPhoto.Blob = []byte("current-image")
	currentPhoto.MimeType = sql.NullString{String: "image/jpeg", Valid: true}

	session := &GeminiSession{
		Contents:    []q.GeminiContent{purePhoto, captionedPhoto, oldModel},
		TmpContents: []q.GeminiContent{currentPhoto}, HistoryRebuildLossy: true,
		AssistantPayloads: map[int64]g.AIAssistantPayload{3: {
			MsgID: 3, Provider: ProviderGemini, Format: PayloadFormatGeminiInteractionSteps,
			Payload: []byte(`[{"type":"thought","signature":"encrypted"},{"type":"model_output","content":[{"type":"text","text":"raw answer"}]}]`),
		}},
	}
	window := session.prepareRequestWindow()
	steps, err := interactionStepsForRebuild(session, window.Contents)
	require.NoError(t, err)
	joined := string(bytesJoinRaw(steps))
	require.NotContains(t, joined, "old-pure-image")
	require.NotContains(t, joined, "old-caption-image")
	require.NotContains(t, joined, "encrypted")
	require.NotContains(t, joined, "raw answer")
	require.Contains(t, joined, "old caption")
	require.Contains(t, joined, "portable answer")
	require.Contains(t, joined, base64.StdEncoding.EncodeToString([]byte("current-image")))
}

func TestSlidingWindowDropsFiftyAtTwoHundredOne(t *testing.T) {
	contents := make([]q.GeminiContent, 200)
	for i := range contents {
		contents[i] = interactionTestContent(int64(i+1), genai.RoleUser, "message")
	}
	session := &GeminiSession{Contents: contents,
		TmpContents: []q.GeminiContent{interactionTestContent(201, genai.RoleUser, "current")}}
	window := session.prepareRequestWindow()
	require.True(t, window.RebuildHistory)
	require.Equal(t, 50, window.Drop)
	require.Len(t, window.Contents, 151)
	require.Equal(t, int64(51), window.StartMsgID)
	require.Equal(t, int64(201), window.Contents[len(window.Contents)-1].MsgID)

	session.TmpContents = append(session.TmpContents, interactionTestContent(202, genai.RoleModel, "answer"))
	window = session.prepareRequestWindow()
	require.Equal(t, 50, window.Drop)
	require.Len(t, window.Contents, 152)
}

func TestLegacyGeminiPayloadConvertsThoughtAndCodeSteps(t *testing.T) {
	record := interactionTestContent(9, genai.RoleModel, "fallback")
	saved := &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{
		{Text: "thought", Thought: true, ThoughtSignature: []byte("signature")},
		{ExecutableCode: &genai.ExecutableCode{Code: "print(1)", Language: genai.LanguagePython}},
		{CodeExecutionResult: &genai.CodeExecutionResult{Outcome: genai.OutcomeOK, Output: "1"}},
		{Text: "final"},
	}}
	steps, err := legacyGeminiContentSteps(&record, saved)
	require.NoError(t, err)
	joined := string(bytesJoinRaw(steps))
	require.Contains(t, joined, `"type":"thought"`)
	require.Contains(t, joined, `"signature":"c2lnbmF0dXJl"`)
	require.Contains(t, joined, `"type":"code_execution_call"`)
	require.Contains(t, joined, `"language":"python"`)
	require.Contains(t, joined, `"type":"code_execution_result"`)
	require.Contains(t, joined, `"text":"final"`)
}

func bytesJoinRaw(steps []json.RawMessage) []byte {
	var result []byte
	for _, step := range steps {
		result = append(result, step...)
	}
	return result
}

func TestLegacyMemoryPromptVariableIsRemoved(t *testing.T) {
	require.NotContains(t, gDefaultSysPrompt, "%MEMORIES%")
	require.NotContains(t, gDefaultSysPrompt, "长期保留的记忆")
	_, exists := replaceMetaVar["MEMORIES"]
	require.False(t, exists)
}
