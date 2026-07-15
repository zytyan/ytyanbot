package geminiapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const defaultBaseURL = "https://generativelanguage.googleapis.com/v1beta"

type Backend int

const BackendGeminiAPI Backend = 1

type ClientConfig struct {
	APIKey     string
	Backend    Backend
	BaseURL    string
	HTTPClient *http.Client
}

type Client struct {
	Models *Models
}

type Models struct {
	apiKey     string
	baseURL    string
	httpClient *http.Client
}

type APIError struct {
	StatusCode int
	Message    string
	Body       []byte
}

func (e *APIError) Error() string {
	return fmt.Sprintf("Gemini API HTTP %d: %s", e.StatusCode, e.Message)
}

func NewClient(_ context.Context, config *ClientConfig) (*Client, error) {
	if config == nil {
		return nil, errors.New("Gemini client config is nil")
	}
	if config.Backend != 0 && config.Backend != BackendGeminiAPI {
		return nil, fmt.Errorf("unsupported Gemini backend %d", config.Backend)
	}
	baseURL := strings.TrimRight(config.BaseURL, "/")
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Minute}
	}
	return &Client{Models: &Models{
		apiKey: config.APIKey, baseURL: baseURL, httpClient: httpClient,
	}}, nil
}

type generateContentRequest struct {
	Contents          []*Content        `json:"contents"`
	SystemInstruction *Content          `json:"systemInstruction,omitempty"`
	Tools             []*Tool           `json:"tools,omitempty"`
	CachedContent     string            `json:"cachedContent,omitempty"`
	GenerationConfig  *generationConfig `json:"generationConfig,omitempty"`
}

type generationConfig struct {
	Temperature      *float32        `json:"temperature,omitempty"`
	TopP             *float32        `json:"topP,omitempty"`
	TopK             *float32        `json:"topK,omitempty"`
	CandidateCount   int32           `json:"candidateCount,omitempty"`
	MaxOutputTokens  int32           `json:"maxOutputTokens,omitempty"`
	StopSequences    []string        `json:"stopSequences,omitempty"`
	Seed             *int32          `json:"seed,omitempty"`
	ResponseMIMEType string          `json:"responseMimeType,omitempty"`
	ThinkingConfig   *ThinkingConfig `json:"thinkingConfig,omitempty"`
}

func generateRequest(contents []*Content, config *GenerateContentConfig) generateContentRequest {
	request := generateContentRequest{Contents: contents}
	if config == nil {
		return request
	}
	request.SystemInstruction = config.SystemInstruction
	request.Tools = config.Tools
	request.CachedContent = config.CachedContent
	generation := &generationConfig{
		Temperature: config.Temperature, TopP: config.TopP, TopK: config.TopK,
		CandidateCount: config.CandidateCount, MaxOutputTokens: config.MaxOutputTokens,
		StopSequences: config.StopSequences, Seed: config.Seed,
		ResponseMIMEType: config.ResponseMIMEType, ThinkingConfig: config.ThinkingConfig,
	}
	if generation.Temperature != nil || generation.TopP != nil || generation.TopK != nil ||
		generation.CandidateCount != 0 || generation.MaxOutputTokens != 0 || len(generation.StopSequences) > 0 ||
		generation.Seed != nil || generation.ResponseMIMEType != "" || generation.ThinkingConfig != nil {
		request.GenerationConfig = generation
	}
	return request
}

func (m *Models) GenerateContent(ctx context.Context, model string, contents []*Content,
	config *GenerateContentConfig,
) (*GenerateContentResponse, error) {
	var response GenerateContentResponse
	if err := m.call(ctx, model, "generateContent", generateRequest(contents, config), &response); err != nil {
		return nil, err
	}
	return &response, nil
}

type countTokensRequest struct {
	Contents          []*Content `json:"contents"`
	SystemInstruction *Content   `json:"systemInstruction,omitempty"`
	Tools             []*Tool    `json:"tools,omitempty"`
}

func (m *Models) CountTokens(ctx context.Context, model string, contents []*Content,
	config *CountTokensConfig,
) (*CountTokensResponse, error) {
	request := countTokensRequest{Contents: contents}
	if config != nil {
		request.SystemInstruction = config.SystemInstruction
		request.Tools = config.Tools
	}
	var response CountTokensResponse
	if err := m.call(ctx, model, "countTokens", request, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (m *Models) call(ctx context.Context, model, method string, request, response any) error {
	payload, err := json.Marshal(request)
	if err != nil {
		return err
	}
	model = strings.TrimPrefix(model, "models/")
	endpoint := m.baseURL + "/models/" + url.PathEscape(model) + ":" + method
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", m.apiKey)
	resp, err := m.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32*1024*1024))
	if err != nil {
		return err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		var apiResponse struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(body, &apiResponse)
		message := apiResponse.Error.Message
		if message == "" {
			message = http.StatusText(resp.StatusCode)
		}
		return &APIError{StatusCode: resp.StatusCode, Message: message, Body: body}
	}
	if err := json.Unmarshal(body, response); err != nil {
		return fmt.Errorf("decode Gemini %s response: %w", method, err)
	}
	return nil
}
