package genbot

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	g "main/globalcfg"
	"net/http"
	"strings"
	"time"

	"google.golang.org/genai"
)

const (
	ModelGeminiFlash     = "gemini-3-flash-preview"
	ModelGeminiFlashLite = "gemini-3.1-flash-lite"
	ModelDeepSeekFlash   = "deepseek-v4-flash"
	defaultAIModel       = ModelGeminiFlash
)

const (
	ProviderGemini   = "gemini"
	ProviderDeepSeek = "deepseek"

	PayloadFormatGeminiContent          = "gemini-content-v1"
	PayloadFormatGeminiInteractionSteps = "gemini-interaction-steps-v1"
	PayloadFormatDeepSeekMessage        = "deepseek-message-v1"
)

var ErrDeepSeekVideoOnly = errors.New("当前模型不处理视频")

type AIUsage struct {
	InputTokens       int64
	CachedInputTokens int64
	OutputTokens      int64
	ThinkingTokens    int64
}

type AIResult struct {
	DisplayText            string
	AssistantPayload       []byte
	AssistantPayloadFormat string
	Usage                  AIUsage
	ThoughtSignature       string
	Feedback               string
	Raw                    []byte
	InteractionID          string
	WindowStartMsgID       int64
	WindowDrop             int
	InputMessageCount      int64
	InputFirstMsgID        int64
	InputLastMsgID         int64
}

type modelOption struct {
	Model    string
	Label    string
	Provider string
}

var modelOptions = []modelOption{
	{Model: ModelGeminiFlash, Label: "Gemini 3 Flash Preview", Provider: ProviderGemini},
	{Model: ModelGeminiFlashLite, Label: "Gemini 3.1 Flash-Lite", Provider: ProviderGemini},
	{Model: ModelDeepSeekFlash, Label: "DeepSeek V4 Flash", Provider: ProviderDeepSeek},
}

func getModelOption(model string) (modelOption, bool) {
	for _, option := range modelOptions {
		if option.Model == model {
			return option, true
		}
	}
	return modelOption{}, false
}

func providerForModel(model string) string {
	if option, ok := getModelOption(model); ok {
		return option.Provider
	}
	return ProviderGemini
}

func configureGeminiThinking(model string, config *genai.GenerateContentConfig) {
	if config.ThinkingConfig == nil {
		config.ThinkingConfig = &genai.ThinkingConfig{}
	}
	config.ThinkingConfig.IncludeThoughts = true
	if model == ModelGeminiFlash && (config.ThinkingConfig.ThinkingLevel == "" ||
		config.ThinkingConfig.ThinkingLevel == genai.ThinkingLevelUnspecified) {
		config.ThinkingConfig.ThinkingLevel = genai.ThinkingLevelLow
	}
}

func generateAI(ctx context.Context, session *GeminiSession, systemPrompt string, geminiConfig *genai.GenerateContentConfig) (*AIResult, error) {
	window := session.prepareRequestWindow()
	var result *AIResult
	var err error
	if session.Provider == ProviderDeepSeek {
		result, err = generateDeepSeek(ctx, session, systemPrompt, window)
	} else {
		configureGeminiThinking(session.Model, geminiConfig)
		result, err = generateGeminiWithInteractions(ctx, session, systemPrompt, geminiConfig, window)
	}
	if result != nil {
		result.WindowStartMsgID = window.StartMsgID
		result.WindowDrop = window.Drop
		result.InputMessageCount = int64(len(window.Contents))
		if len(window.Contents) > 0 {
			result.InputFirstMsgID = window.Contents[0].MsgID
			result.InputLastMsgID = window.Contents[len(window.Contents)-1].MsgID
		}
	}
	return result, err
}

func resultFromGeminiResponse(response *genai.GenerateContentResponse) (*AIResult, error) {
	result := &AIResult{}
	if usage := response.UsageMetadata; usage != nil {
		result.Usage = AIUsage{
			InputTokens:       int64(usage.PromptTokenCount),
			CachedInputTokens: int64(usage.CachedContentTokenCount),
			OutputTokens:      int64(usage.CandidatesTokenCount + usage.ThoughtsTokenCount),
			ThinkingTokens:    int64(usage.ThoughtsTokenCount),
		}
	}
	if len(response.Candidates) > 0 && response.Candidates[0].Content != nil {
		content := response.Candidates[0].Content
		payload, err := json.Marshal(content)
		if err != nil {
			return nil, fmt.Errorf("serialize Gemini assistant content: %w", err)
		}
		result.AssistantPayload = payload
		result.AssistantPayloadFormat = PayloadFormatGeminiContent
		var display strings.Builder
		for _, part := range content.Parts {
			if !part.Thought {
				display.WriteString(part.Text)
			}
			if len(part.ThoughtSignature) > 0 {
				if result.ThoughtSignature == "" {
					result.ThoughtSignature = base64.StdEncoding.EncodeToString(part.ThoughtSignature)
				}
			}
		}
		result.DisplayText = display.String()
	}
	if response.PromptFeedback != nil {
		result.Feedback = string(response.PromptFeedback.BlockReason) + response.PromptFeedback.BlockReasonMessage
	}
	result.Raw, _ = response.MarshalJSON()
	return result, nil
}

type deepSeekMessage struct {
	Role             string          `json:"role"`
	Content          string          `json:"content"`
	ReasoningContent string          `json:"reasoning_content,omitempty"`
	ToolCalls        json.RawMessage `json:"tool_calls,omitempty"`
	raw              json.RawMessage
}

func (m deepSeekMessage) MarshalJSON() ([]byte, error) {
	if len(m.raw) > 0 {
		return append([]byte(nil), m.raw...), nil
	}
	type wireMessage deepSeekMessage
	return json.Marshal(wireMessage(m))
}

func (m *deepSeekMessage) UnmarshalJSON(data []byte) error {
	type wireMessage deepSeekMessage
	var decoded wireMessage
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*m = deepSeekMessage(decoded)
	m.raw = append(m.raw[:0], data...)
	return nil
}

type deepSeekRequest struct {
	Model     string            `json:"model"`
	Messages  []deepSeekMessage `json:"messages"`
	MaxTokens int               `json:"max_tokens,omitempty"`
	Thinking  *deepSeekThinking `json:"thinking,omitempty"`
}

type deepSeekThinking struct {
	Type string `json:"type"`
}

type deepSeekResponse struct {
	Choices []struct {
		Message deepSeekMessage `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens          int64 `json:"prompt_tokens"`
		PromptCacheHitTokens  int64 `json:"prompt_cache_hit_tokens"`
		PromptCacheMissTokens int64 `json:"prompt_cache_miss_tokens"`
		CompletionTokens      int64 `json:"completion_tokens"`
		CompletionDetails     struct {
			ReasoningTokens int64 `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
	} `json:"usage"`
}

func generateDeepSeek(ctx context.Context, session *GeminiSession, systemPrompt string, window aiRequestWindow) (*AIResult, error) {
	cfg := g.GetConfig()
	if cfg.DeepSeekKey == "" {
		return nil, errors.New("DeepSeek API Key 未配置")
	}
	requestSession := &GeminiSession{
		Contents: window.Contents, AssistantPayloads: session.AssistantPayloads,
		HistoryRebuildLossy: session.HistoryRebuildLossy,
	}
	messages, err := requestSession.ToDeepSeekMessages(systemPrompt)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 15 * time.Minute}
	request := deepSeekRequest{
		Model: session.Model, Messages: messages,
		Thinking: &deepSeekThinking{Type: "enabled"},
	}
	var result *AIResult
	for attempt := 0; attempt < 3; attempt++ {
		result, err = callDeepSeek(ctx, client, cfg.DeepSeekBaseURL, cfg.DeepSeekKey, request)
		if err == nil {
			return result, nil
		}
		if attempt == 2 {
			break
		}
		timer := time.NewTimer(time.Duration(1<<attempt) * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return nil, err
}

func callDeepSeek(ctx context.Context, client *http.Client, baseURL, apiKey string, request deepSeekRequest) (*AIResult, error) {
	payload, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(baseURL, "/")+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var decoded deepSeekResponse
	decoder := json.NewDecoder(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var apiErr struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = decoder.Decode(&apiErr)
		return nil, fmt.Errorf("DeepSeek API HTTP %d: %s", resp.StatusCode, apiErr.Error.Message)
	}
	if err := decoder.Decode(&decoded); err != nil {
		return nil, err
	}
	if len(decoded.Choices) == 0 {
		return nil, errors.New("DeepSeek API 没有返回候选结果")
	}
	message := decoded.Choices[0].Message
	if message.Role == "" {
		message.Role = "assistant"
		message.raw = nil
	}
	assistantPayload, err := message.MarshalJSON()
	if err != nil {
		return nil, fmt.Errorf("serialize DeepSeek assistant message: %w", err)
	}
	return &AIResult{
		DisplayText:            message.Content,
		AssistantPayload:       assistantPayload,
		AssistantPayloadFormat: PayloadFormatDeepSeekMessage,
		Usage: AIUsage{
			InputTokens:       decoded.Usage.PromptTokens,
			CachedInputTokens: decoded.Usage.PromptCacheHitTokens,
			OutputTokens:      decoded.Usage.CompletionTokens,
			ThinkingTokens:    decoded.Usage.CompletionDetails.ReasoningTokens,
		},
	}, nil
}
