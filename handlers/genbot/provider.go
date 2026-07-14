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
)

var ErrDeepSeekVideoOnly = errors.New("当前模型不处理视频")

type AIUsage struct {
	InputTokens       int64
	CachedInputTokens int64
	OutputTokens      int64
	ThinkingTokens    int64
}

type AIResult struct {
	Text             string
	Usage            AIUsage
	ThoughtSignature string
	Feedback         string
	Raw              []byte
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

func generateAI(ctx context.Context, session *GeminiSession, systemPrompt, turnContext string, geminiConfig *genai.GenerateContentConfig) (*AIResult, error) {
	if session.Provider == ProviderDeepSeek {
		return generateDeepSeek(ctx, session, systemPrompt, turnContext)
	}
	geminiConfig.SystemInstruction = genai.NewContentFromText(systemPrompt, genai.RoleModel)
	response, err := generateGemini(ctx, session, turnContext, geminiConfig)
	if err != nil {
		return nil, err
	}
	result := &AIResult{Text: response.Text()}
	if usage := response.UsageMetadata; usage != nil {
		result.Usage = AIUsage{
			InputTokens:       int64(usage.PromptTokenCount),
			CachedInputTokens: int64(usage.CachedContentTokenCount),
			OutputTokens:      int64(usage.CandidatesTokenCount + usage.ThoughtsTokenCount),
			ThinkingTokens:    int64(usage.ThoughtsTokenCount),
		}
	}
	if len(response.Candidates) > 0 && response.Candidates[0].Content != nil {
		for _, part := range response.Candidates[0].Content.Parts {
			if len(part.ThoughtSignature) > 0 {
				result.ThoughtSignature = base64.StdEncoding.EncodeToString(part.ThoughtSignature)
				break
			}
		}
	}
	if response.PromptFeedback != nil {
		result.Feedback = string(response.PromptFeedback.BlockReason) + response.PromptFeedback.BlockReasonMessage
	}
	result.Raw, _ = response.MarshalJSON()
	return result, nil
}

type deepSeekMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
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
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
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

func generateDeepSeek(ctx context.Context, session *GeminiSession, systemPrompt, turnContext string) (*AIResult, error) {
	cfg := g.GetConfig()
	if cfg.DeepSeekKey == "" {
		return nil, errors.New("DeepSeek API Key 未配置")
	}
	messages, err := session.ToDeepSeekMessages(systemPrompt, turnContext)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 15 * time.Minute}
	request := deepSeekRequest{Model: session.Model, Messages: messages}
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
	return &AIResult{
		Text: decoded.Choices[0].Message.Content,
		Usage: AIUsage{
			InputTokens:       decoded.Usage.PromptTokens,
			CachedInputTokens: decoded.Usage.PromptCacheHitTokens,
			OutputTokens:      decoded.Usage.CompletionTokens,
			ThinkingTokens:    decoded.Usage.CompletionDetails.ReasoningTokens,
		},
	}, nil
}
