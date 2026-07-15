package genbot

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	g "main/globalcfg"
	"main/globalcfg/q"
	genai "main/handlers/genbot/geminiapi"

	"github.com/stretchr/testify/require"
)

const liveCacheSessionID = int64(103)
const aiAPITestEnv = "YTYAN_TEST_AI_API"

var aiAPITestNotice sync.Once

func loadLiveSession(t *testing.T) *GeminiSession {
	t.Helper()
	contents, err := g.Q.GetAllMsgInSession(context.Background(), liveCacheSessionID, geminiSessionContentLimit)
	require.NoError(t, err)
	require.NotEmpty(t, contents)
	payloads, err := g.GetAISessionAssistantPayloads(context.Background(), liveCacheSessionID)
	require.NoError(t, err)
	return &GeminiSession{
		Contents:          contents,
		AssistantPayloads: payloads,
		Provider:          ProviderGemini,
		Model:             ModelGeminiFlash,
	}
}

func requireLiveAI(t *testing.T) {
	t.Helper()
	enabled := os.Getenv(aiAPITestEnv) == "1"
	aiAPITestNotice.Do(func() {
		fmt.Printf("external AI API tests: set %s=1 to enable Gemini/DeepSeek checks (enabled=%t)\n", aiAPITestEnv, enabled)
	})
	if !enabled {
		t.SkipNow()
	}
}

func TestLiveGeminiImplicitCache(t *testing.T) {
	requireLiveAI(t)
	session := loadLiveSession(t)
	client := getGenAiClient()
	contents := session.ToGenaiContents()
	count, err := client.Models.CountTokens(context.Background(), session.Model, contents, nil)
	require.NoError(t, err)
	require.GreaterOrEqual(t, count.TotalTokens, int32(4096))

	config := &genai.GenerateContentConfig{
		SystemInstruction: genai.NewContentFromText("缓存验证；只回答 OK。", genai.RoleModel),
		MaxOutputTokens:   16,
	}
	first, err := generateGemini(context.Background(), session, config)
	require.NoError(t, err)
	require.NotNil(t, first.UsageMetadata)
	time.Sleep(3 * time.Second)
	second, err := generateGemini(context.Background(), session, config)
	require.NoError(t, err)
	require.NotNil(t, second.UsageMetadata)
	t.Logf("gemini prompt=%d cached=%d", second.UsageMetadata.PromptTokenCount, second.UsageMetadata.CachedContentTokenCount)
	require.Greater(t, second.UsageMetadata.CachedContentTokenCount, int32(0))
}

func TestLiveGeminiInteractionsImplicitCache(t *testing.T) {
	requireLiveAI(t)
	stablePrefix := q.GeminiContent{
		MsgID: 101, Role: genai.RoleUser, Username: "cache tester", MsgType: "text",
		SentTime: q.UnixTime{Time: time.Unix(101, 0)},
		Text: sql.NullString{String: strings.Repeat(
			"这是用于验证 Interactions 隐式缓存的稳定公共前缀，不包含任何真实用户数据。", 2500), Valid: true},
	}
	firstQuestion := q.GeminiContent{
		MsgID: 102, Role: genai.RoleUser, Username: "cache tester", MsgType: "text",
		SentTime: q.UnixTime{Time: time.Unix(102, 0)}, Text: sql.NullString{String: "只回答 OK。", Valid: true},
	}
	session := &GeminiSession{
		Contents: []q.GeminiContent{stablePrefix}, TmpContents: []q.GeminiContent{firstQuestion},
		AssistantPayloads: make(map[int64]g.AIAssistantPayload), Provider: ProviderGemini, Model: ModelGeminiFlash,
	}
	config := &genai.GenerateContentConfig{ThinkingConfig: &genai.ThinkingConfig{IncludeThoughts: true}, MaxOutputTokens: 32}
	first, err := generateGeminiWithInteractions(context.Background(), session, "缓存验证；只回答 OK。", config,
		session.prepareRequestWindow())
	require.NoError(t, err)
	require.NotEmpty(t, first.InteractionID)

	modelRecord := q.GeminiContent{
		MsgID: 103, Role: genai.RoleModel, Username: "bot", MsgType: "text",
		SentTime: q.UnixTime{Time: time.Unix(103, 0)}, Text: sql.NullString{String: first.DisplayText, Valid: true},
	}
	session.Contents = append(session.Contents, firstQuestion, modelRecord)
	session.AssistantPayloads[103] = g.AIAssistantPayload{
		MsgID: 103, Provider: ProviderGemini, Format: first.AssistantPayloadFormat, Payload: first.AssistantPayload,
	}
	session.GeminiInteractionID = first.InteractionID
	session.TmpContents = []q.GeminiContent{{
		MsgID: 104, Role: genai.RoleUser, Username: "cache tester", MsgType: "text",
		SentTime: q.UnixTime{Time: time.Unix(104, 0)}, Text: sql.NullString{String: "再次只回答 OK。", Valid: true},
	}}
	var second *AIResult
	for attempt := 0; attempt < 3; attempt++ {
		second, err = generateGeminiWithInteractions(context.Background(), session, "缓存验证；只回答 OK。", config,
			session.prepareRequestWindow())
		require.NoError(t, err)
		t.Logf("gemini interactions attempt=%d input=%d cached=%d", attempt+1,
			second.Usage.InputTokens, second.Usage.CachedInputTokens)
		if second.Usage.CachedInputTokens > 0 {
			break
		}
		time.Sleep(3 * time.Second)
	}
	require.Greater(t, second.Usage.CachedInputTokens, int64(0))
}

func TestLiveGeminiInteractionsFlashLite(t *testing.T) {
	requireLiveAI(t)
	stablePrefix := q.GeminiContent{
		MsgID: 151, Role: genai.RoleUser, Username: "cache tester", MsgType: "text",
		SentTime: q.UnixTime{Time: time.Unix(151, 0)},
		Text: sql.NullString{String: strings.Repeat(
			"这是用于验证 Flash-Lite Interactions 缓存的稳定公共前缀。", 2500), Valid: true},
	}
	question := q.GeminiContent{
		MsgID: 152, Role: genai.RoleUser, Username: "cache tester", MsgType: "text",
		SentTime: q.UnixTime{Time: time.Unix(152, 0)}, Text: sql.NullString{String: "只回答 OK。", Valid: true},
	}
	session := &GeminiSession{
		Contents: []q.GeminiContent{stablePrefix}, TmpContents: []q.GeminiContent{question},
		AssistantPayloads: make(map[int64]g.AIAssistantPayload), Provider: ProviderGemini, Model: ModelGeminiFlashLite,
	}
	config := &genai.GenerateContentConfig{ThinkingConfig: &genai.ThinkingConfig{IncludeThoughts: true}, MaxOutputTokens: 32}
	first, err := generateGeminiWithInteractions(context.Background(), session, "缓存验证；只回答 OK。", config,
		session.prepareRequestWindow())
	require.NoError(t, err)
	require.NotEmpty(t, first.InteractionID)
	modelRecord := q.GeminiContent{
		MsgID: 153, Role: genai.RoleModel, Username: "bot", MsgType: "text",
		SentTime: q.UnixTime{Time: time.Unix(153, 0)}, Text: sql.NullString{String: first.DisplayText, Valid: true},
	}
	session.Contents = append(session.Contents, question, modelRecord)
	session.AssistantPayloads[153] = g.AIAssistantPayload{
		MsgID: 153, Provider: ProviderGemini, Format: first.AssistantPayloadFormat, Payload: first.AssistantPayload,
	}
	session.GeminiInteractionID = first.InteractionID
	session.TmpContents = []q.GeminiContent{{
		MsgID: 154, Role: genai.RoleUser, Username: "cache tester", MsgType: "text",
		SentTime: q.UnixTime{Time: time.Unix(154, 0)}, Text: sql.NullString{String: "再次只回答 OK。", Valid: true},
	}}
	second, err := generateGeminiWithInteractions(context.Background(), session, "缓存验证；只回答 OK。", config,
		session.prepareRequestWindow())
	require.NoError(t, err)
	require.NotEmpty(t, second.InteractionID)
	t.Logf("gemini flash-lite interactions input=%d cached=%d", second.Usage.InputTokens, second.Usage.CachedInputTokens)
}

func TestLiveGeminiInteractionsToolsAndRawReplay(t *testing.T) {
	requireLiveAI(t)
	stablePrefix := q.GeminiContent{
		MsgID: 201, Role: genai.RoleUser, Username: "cache tester", MsgType: "text",
		SentTime: q.UnixTime{Time: time.Unix(201, 0)},
		Text: sql.NullString{String: strings.Repeat(
			"这是用于验证 Interactions 工具历史与缓存的稳定公共前缀。", 2500), Valid: true},
	}
	question := q.GeminiContent{
		MsgID: 202, Role: genai.RoleUser, Username: "cache tester", MsgType: "text",
		SentTime: q.UnixTime{Time: time.Unix(202, 0)},
		Text:     sql.NullString{String: "必须调用 Google Search 查询今天美元兑欧元汇率，再调用代码执行计算 12345 美元可兑换多少欧元。", Valid: true},
	}
	session := &GeminiSession{
		Contents: []q.GeminiContent{stablePrefix}, TmpContents: []q.GeminiContent{question},
		AssistantPayloads: make(map[int64]g.AIAssistantPayload), Provider: ProviderGemini, Model: ModelGeminiFlash,
	}
	config := &genai.GenerateContentConfig{
		Tools:          []*genai.Tool{{GoogleSearch: &genai.GoogleSearch{}, CodeExecution: &genai.ToolCodeExecution{}}},
		ThinkingConfig: &genai.ThinkingConfig{IncludeThoughts: true}, MaxOutputTokens: 2048,
	}
	first, err := generateGeminiWithInteractions(context.Background(), session, "严格调用用户要求的工具。", config,
		session.prepareRequestWindow())
	require.NoError(t, err)
	payload := string(first.AssistantPayload)
	require.Contains(t, payload, `"type":"google_search_call"`)
	require.Contains(t, payload, `"type":"code_execution_call"`)
	require.Contains(t, payload, `"type":"code_execution_result"`)
	require.Contains(t, payload, `"type":"model_output"`)

	modelRecord := q.GeminiContent{
		MsgID: 203, Role: genai.RoleModel, Username: "bot", MsgType: "text",
		SentTime: q.UnixTime{Time: time.Unix(203, 0)}, Text: sql.NullString{String: first.DisplayText, Valid: true},
	}
	session.Contents = append(session.Contents, question, modelRecord)
	session.AssistantPayloads[203] = g.AIAssistantPayload{
		MsgID: 203, Provider: ProviderGemini, Format: first.AssistantPayloadFormat, Payload: first.AssistantPayload,
	}
	session.GeminiInteractionID = first.InteractionID
	session.TmpContents = []q.GeminiContent{{
		MsgID: 204, Role: genai.RoleUser, Username: "cache tester", MsgType: "text",
		SentTime: q.UnixTime{Time: time.Unix(204, 0)}, Text: sql.NullString{String: "确认上一轮工具结果可读，只回答 OK。", Valid: true},
	}}
	second, err := generateGeminiWithInteractions(context.Background(), session, "严格调用用户要求的工具。", config,
		session.prepareRequestWindow())
	require.NoError(t, err)
	require.NotEmpty(t, second.DisplayText)
	t.Logf("gemini interactions tools input=%d cached=%d output=%d", second.Usage.InputTokens,
		second.Usage.CachedInputTokens, second.Usage.OutputTokens)
	require.Greater(t, second.Usage.CachedInputTokens, int64(0))
}

func TestLiveGeminiToolsRawReplayAndCache(t *testing.T) {
	requireLiveAI(t)
	stablePrefix := q.GeminiContent{
		MsgID: 1, Role: genai.RoleUser, Username: "cache tester", MsgType: "text",
		SentTime: q.UnixTime{Time: time.Unix(1, 0)},
		Text:     sql.NullString{String: strings.Repeat("这是用于验证隐式缓存的稳定公共前缀，不包含任何真实用户数据。", 2500), Valid: true},
	}
	firstQuestion := q.GeminiContent{
		MsgID: 2, Role: genai.RoleUser, Username: "cache tester", MsgType: "text",
		SentTime: q.UnixTime{Time: time.Unix(2, 0)},
		Text:     sql.NullString{String: "必须调用 Google Search 查询今天美元兑欧元汇率，再必须通过代码执行工具运行 Python，计算 12345 美元按该汇率可兑换多少欧元。简短列出来源和计算结果。", Valid: true},
	}
	session := &GeminiSession{
		Contents: []q.GeminiContent{stablePrefix}, TmpContents: []q.GeminiContent{firstQuestion},
		AssistantPayloads: make(map[int64]g.AIAssistantPayload), Provider: ProviderGemini, Model: ModelGeminiFlash,
		AllowCodeExecution: true,
	}
	config := &genai.GenerateContentConfig{
		SystemInstruction: genai.NewContentFromText("这是工具与缓存结构测试。严格按用户要求调用工具。", genai.RoleModel),
		Tools:             []*genai.Tool{{GoogleSearch: &genai.GoogleSearch{}, CodeExecution: &genai.ToolCodeExecution{}}},
		ThinkingConfig:    &genai.ThinkingConfig{IncludeThoughts: true},
		MaxOutputTokens:   2048,
	}
	count, err := getGenAiClient().Models.CountTokens(context.Background(), session.Model, session.ToGenaiContents(), nil)
	require.NoError(t, err)
	require.GreaterOrEqual(t, count.TotalTokens, int32(4096))

	first, err := generateGemini(context.Background(), session, config)
	require.NoError(t, err)
	require.NotEmpty(t, first.Candidates)
	candidate := first.Candidates[0]
	require.NotNil(t, candidate.Content)
	var executableCode, codeResults, toolCalls, toolResponses, thoughts, signatures int
	for _, part := range candidate.Content.Parts {
		if part.ExecutableCode != nil {
			executableCode++
		}
		if part.CodeExecutionResult != nil {
			codeResults++
		}
		if part.ToolCall != nil {
			toolCalls++
		}
		if part.ToolResponse != nil {
			toolResponses++
		}
		if part.Thought {
			thoughts++
		}
		if len(part.ThoughtSignature) > 0 {
			signatures++
		}
	}
	searchQueries := 0
	if candidate.GroundingMetadata != nil {
		searchQueries = len(candidate.GroundingMetadata.WebSearchQueries)
	}
	require.Greater(t, searchQueries, 0, "Google Search grounding metadata missing")
	require.Greater(t, executableCode, 0, "executable code part missing")
	require.Greater(t, codeResults, 0, "code execution result part missing")

	parsed, err := resultFromGeminiResponse(first)
	require.NoError(t, err)
	require.NotEmpty(t, parsed.DisplayText)
	var replayed genai.Content
	require.NoError(t, json.Unmarshal(parsed.AssistantPayload, &replayed))
	require.Equal(t, candidate.Content, &replayed)

	modelRecord := q.GeminiContent{
		MsgID: 3, Role: genai.RoleModel, MsgType: "text", Username: "bot",
		SentTime: q.UnixTime{Time: time.Unix(3, 0)},
		Text:     sql.NullString{String: parsed.DisplayText, Valid: true},
	}
	session.Contents = append(session.Contents, firstQuestion, modelRecord)
	session.TmpContents = []q.GeminiContent{{
		MsgID: 4, Role: genai.RoleUser, Username: "cache tester", MsgType: "text",
		SentTime: q.UnixTime{Time: time.Unix(4, 0)},
		Text:     sql.NullString{String: "基于上一轮原始工具结果，仅确认历史内容可读，并简短回答 OK。", Valid: true},
	}}
	session.AssistantPayloads[3] = g.AIAssistantPayload{
		MsgID: 3, Provider: ProviderGemini, Format: parsed.AssistantPayloadFormat, Payload: parsed.AssistantPayload,
	}
	time.Sleep(3 * time.Second)
	var second *genai.GenerateContentResponse
	var cached int32
	for attempt := 0; attempt < 3 && cached == 0; attempt++ {
		second, err = generateGemini(context.Background(), session, config)
		require.NoError(t, err)
		require.NotNil(t, second.UsageMetadata)
		cached = second.UsageMetadata.CachedContentTokenCount
		t.Logf("gemini tools attempt=%d prompt=%d cached=%d", attempt+1,
			second.UsageMetadata.PromptTokenCount, cached)
		if cached == 0 && attempt < 2 {
			time.Sleep(5 * time.Second)
		}
	}
	t.Logf("gemini tools search_queries=%d executable_code=%d code_results=%d tool_calls=%d tool_responses=%d thoughts=%d signatures=%d first_prompt=%d first_cached=%d final_prompt=%d final_cached=%d",
		searchQueries, executableCode, codeResults, toolCalls, toolResponses, thoughts, signatures,
		first.UsageMetadata.PromptTokenCount, first.UsageMetadata.CachedContentTokenCount,
		second.UsageMetadata.PromptTokenCount, cached)
	require.Greater(t, cached, int32(0))
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
	require.NotEmpty(t, first.DisplayText)

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

func TestLiveDeepSeekThinkingRoundTrip(t *testing.T) {
	requireLiveAI(t)
	require.NotEmpty(t, g.GetConfig().DeepSeekKey)
	request := deepSeekRequest{
		Model: ModelDeepSeekFlash,
		Messages: []deepSeekMessage{
			{Role: "system", Content: "只需简短回答。"},
			{Role: "user", Content: "计算 17 乘以 19。"},
		},
		MaxTokens: 256,
		Thinking:  &deepSeekThinking{Type: "enabled"},
	}
	result, err := callDeepSeek(context.Background(), &http.Client{Timeout: 15 * time.Minute},
		g.GetConfig().DeepSeekBaseURL, g.GetConfig().DeepSeekKey, request)
	require.NoError(t, err)
	require.NotEmpty(t, result.DisplayText)
	require.Equal(t, PayloadFormatDeepSeekMessage, result.AssistantPayloadFormat)
	var assistant deepSeekMessage
	require.NoError(t, json.Unmarshal(result.AssistantPayload, &assistant))
	require.NotEmpty(t, assistant.ReasoningContent)
	replayed, err := json.Marshal(assistant)
	require.NoError(t, err)
	require.JSONEq(t, string(result.AssistantPayload), string(replayed))
	t.Logf("deepseek reasoning_chars=%d output_chars=%d", len([]rune(assistant.ReasoningContent)), len([]rune(assistant.Content)))
}
