package genbot

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	g "main/globalcfg"
	"main/globalcfg/q"
	genai "main/handlers/genbot/geminiapi"
	"net/http"
	"strings"
	"time"
)

const (
	// v1beta is required while the model menu still contains gemini-3-flash-preview.
	// The revision pins the current steps-based Interactions schema.
	geminiInteractionsEndpoint = "https://generativelanguage.googleapis.com/v1beta/interactions"
	geminiInteractionsRevision = "2026-05-20"
)

var geminiInteractionsHTTPClient = &http.Client{Timeout: 15 * time.Minute}

type interactionRequest struct {
	Model                 string            `json:"model"`
	Input                 []json.RawMessage `json:"input"`
	SystemInstruction     string            `json:"system_instruction,omitempty"`
	Tools                 []map[string]any  `json:"tools,omitempty"`
	Store                 bool              `json:"store"`
	GenerationConfig      map[string]any    `json:"generation_config,omitempty"`
	PreviousInteractionID string            `json:"previous_interaction_id,omitempty"`
}

type interactionResponse struct {
	ID     string            `json:"id"`
	Status string            `json:"status"`
	Steps  []json.RawMessage `json:"steps"`
	Usage  struct {
		TotalCachedTokens  int64 `json:"total_cached_tokens"`
		TotalInputTokens   int64 `json:"total_input_tokens"`
		TotalOutputTokens  int64 `json:"total_output_tokens"`
		TotalThoughtTokens int64 `json:"total_thought_tokens"`
		TotalToolUseTokens int64 `json:"total_tool_use_tokens"`
	} `json:"usage"`
}

type interactionHTTPError struct {
	StatusCode int
	Message    string
}

func (e *interactionHTTPError) Error() string {
	return fmt.Sprintf("Gemini Interactions API HTTP %d: %s", e.StatusCode, e.Message)
}

func rawInteractionJSON(value any) (json.RawMessage, error) {
	data, err := json.Marshal(value)
	return json.RawMessage(data), err
}

func interactionContentBlocks(content *q.GeminiContent) ([]map[string]any, error) {
	blocks := []map[string]any{{"type": "text", "text": compactUserText(content)}}
	if len(content.Blob) == 0 || !content.MimeType.Valid {
		return blocks, nil
	}
	mimeType := content.MimeType.String
	mediaType := ""
	switch {
	case strings.HasPrefix(mimeType, "image/"):
		mediaType = "image"
	case strings.HasPrefix(mimeType, "video/"):
		mediaType = "video"
	default:
		return nil, fmt.Errorf("unsupported Gemini interaction media type %q", mimeType)
	}
	blocks = append(blocks, map[string]any{
		"type": mediaType, "mime_type": mimeType,
		"data": base64.StdEncoding.EncodeToString(content.Blob),
	})
	return blocks, nil
}

func userInputInteractionStep(content *q.GeminiContent) (json.RawMessage, error) {
	blocks, err := interactionContentBlocks(content)
	if err != nil {
		return nil, err
	}
	return rawInteractionJSON(map[string]any{"type": "user_input", "content": blocks})
}

func fallbackModelOutputStep(content *q.GeminiContent) (json.RawMessage, error) {
	text := ""
	if content.Text.Valid {
		text = content.Text.String
	}
	return rawInteractionJSON(map[string]any{
		"type":    "model_output",
		"content": []map[string]any{{"type": "text", "text": text}},
	})
}

func legacyGeminiContentSteps(content *q.GeminiContent, saved *genai.Content) ([]json.RawMessage, error) {
	steps := make([]json.RawMessage, 0, len(saved.Parts))
	textBlocks := make([]map[string]any, 0, 2)
	lastCodeID := ""
	flushText := func() error {
		if len(textBlocks) == 0 {
			return nil
		}
		step, err := rawInteractionJSON(map[string]any{"type": "model_output", "content": textBlocks})
		if err != nil {
			return err
		}
		steps = append(steps, step)
		textBlocks = nil
		return nil
	}
	for index, part := range saved.Parts {
		if part == nil {
			continue
		}
		if part.Thought || len(part.ThoughtSignature) > 0 {
			if err := flushText(); err != nil {
				return nil, err
			}
			thought := map[string]any{"type": "thought"}
			if len(part.ThoughtSignature) > 0 {
				thought["signature"] = base64.StdEncoding.EncodeToString(part.ThoughtSignature)
			}
			if part.Text != "" {
				thought["summary"] = []map[string]any{{"type": "text", "text": part.Text}}
			}
			step, err := rawInteractionJSON(thought)
			if err != nil {
				return nil, err
			}
			steps = append(steps, step)
			if part.Thought {
				continue
			}
		}
		if executable := part.ExecutableCode; executable != nil {
			if err := flushText(); err != nil {
				return nil, err
			}
			lastCodeID = executable.ID
			if lastCodeID == "" {
				lastCodeID = fmt.Sprintf("legacy-code-%d-%d", content.MsgID, index)
			}
			language := strings.ToLower(strings.TrimPrefix(string(executable.Language), "LANGUAGE_"))
			step, err := rawInteractionJSON(map[string]any{
				"type": "code_execution_call", "id": lastCodeID,
				"arguments": map[string]any{"code": executable.Code, "language": language},
			})
			if err != nil {
				return nil, err
			}
			steps = append(steps, step)
			continue
		}
		if executionResult := part.CodeExecutionResult; executionResult != nil {
			if err := flushText(); err != nil {
				return nil, err
			}
			callID := executionResult.ID
			if callID == "" {
				callID = lastCodeID
			}
			if callID == "" {
				callID = fmt.Sprintf("legacy-code-%d-%d", content.MsgID, index)
			}
			step, err := rawInteractionJSON(map[string]any{
				"type": "code_execution_result", "call_id": callID,
				"result": executionResult.Output, "is_error": executionResult.Outcome != genai.OutcomeOK,
			})
			if err != nil {
				return nil, err
			}
			steps = append(steps, step)
			continue
		}
		if call := part.FunctionCall; call != nil {
			if err := flushText(); err != nil {
				return nil, err
			}
			callID := call.ID
			if callID == "" {
				callID = fmt.Sprintf("legacy-function-%d-%d", content.MsgID, index)
			}
			step, err := rawInteractionJSON(map[string]any{
				"type": "function_call", "id": callID, "name": call.Name, "arguments": call.Args,
			})
			if err != nil {
				return nil, err
			}
			steps = append(steps, step)
			continue
		}
		if response := part.FunctionResponse; response != nil {
			if err := flushText(); err != nil {
				return nil, err
			}
			callID := response.ID
			if callID == "" {
				callID = fmt.Sprintf("legacy-function-%d-%d", content.MsgID, index)
			}
			step, err := rawInteractionJSON(map[string]any{
				"type": "function_result", "call_id": callID, "name": response.Name, "result": response.Response,
			})
			if err != nil {
				return nil, err
			}
			steps = append(steps, step)
			continue
		}
		if part.Text != "" {
			textBlocks = append(textBlocks, map[string]any{"type": "text", "text": part.Text})
		}
	}
	if err := flushText(); err != nil {
		return nil, err
	}
	if len(steps) == 0 {
		fallback, err := fallbackModelOutputStep(content)
		if err != nil {
			return nil, err
		}
		steps = append(steps, fallback)
	}
	return steps, nil
}

func assistantInteractionSteps(content *q.GeminiContent, payloads map[int64]g.AIAssistantPayload) ([]json.RawMessage, error) {
	payload, ok := payloads[content.MsgID]
	if ok && payload.Provider == ProviderGemini {
		switch payload.Format {
		case PayloadFormatGeminiInteractionSteps:
			var steps []json.RawMessage
			if err := json.Unmarshal(payload.Payload, &steps); err == nil && len(steps) > 0 {
				return steps, nil
			} else {
				if err == nil {
					err = errors.New("empty interaction steps")
				}
				warnInvalidAssistantPayload(ProviderGemini, content.MsgID, err)
			}
		case PayloadFormatGeminiContent:
			var saved genai.Content
			if err := json.Unmarshal(payload.Payload, &saved); err == nil {
				return legacyGeminiContentSteps(content, &saved)
			} else {
				warnInvalidAssistantPayload(ProviderGemini, content.MsgID, err)
			}
		}
	}
	fallback, err := fallbackModelOutputStep(content)
	if err != nil {
		return nil, err
	}
	return []json.RawMessage{fallback}, nil
}

func interactionStepsForContents(contents []q.GeminiContent, payloads map[int64]g.AIAssistantPayload) ([]json.RawMessage, error) {
	steps := make([]json.RawMessage, 0, len(contents))
	for index := range contents {
		content := &contents[index]
		if content.Role == genai.RoleModel {
			assistantSteps, err := assistantInteractionSteps(content, payloads)
			if err != nil {
				return nil, err
			}
			steps = append(steps, assistantSteps...)
			continue
		}
		step, err := userInputInteractionStep(content)
		if err != nil {
			return nil, err
		}
		steps = append(steps, step)
	}
	return steps, nil
}

func lossyInteractionStepsForContents(contents []q.GeminiContent) ([]json.RawMessage, error) {
	steps := make([]json.RawMessage, 0, len(contents))
	for index := range contents {
		content := &contents[index]
		if content.Role == genai.RoleModel {
			if !content.Text.Valid || content.Text.String == "" {
				continue
			}
			step, err := fallbackModelOutputStep(content)
			if err != nil {
				return nil, err
			}
			steps = append(steps, step)
			continue
		}
		// Historical binary media cannot be safely migrated between model
		// families. Preserve its caption when present; otherwise omit the row.
		if len(content.Blob) > 0 && (!content.Text.Valid || content.Text.String == "") {
			continue
		}
		step, err := rawInteractionJSON(map[string]any{
			"type":    "user_input",
			"content": []map[string]any{{"type": "text", "text": compactUserText(content)}},
		})
		if err != nil {
			return nil, err
		}
		steps = append(steps, step)
	}
	return steps, nil
}

func interactionStepsForRebuild(session *GeminiSession, contents []q.GeminiContent) ([]json.RawMessage, error) {
	if !session.HistoryRebuildLossy {
		return interactionStepsForContents(contents, session.AssistantPayloads)
	}
	historyEnd := len(contents) - len(session.TmpContents)
	if historyEnd < 0 {
		historyEnd = 0
	}
	historySteps, err := lossyInteractionStepsForContents(contents[:historyEnd])
	if err != nil {
		return nil, err
	}
	currentSteps, err := interactionStepsForContents(contents[historyEnd:], session.AssistantPayloads)
	if err != nil {
		return nil, err
	}
	return append(historySteps, currentSteps...), nil
}

func interactionTools(config *genai.GenerateContentConfig) []map[string]any {
	tools := make([]map[string]any, 0, 2)
	for _, tool := range config.Tools {
		if tool == nil {
			continue
		}
		if tool.GoogleSearch != nil {
			tools = append(tools, map[string]any{"type": "google_search"})
		}
		if tool.CodeExecution != nil {
			tools = append(tools, map[string]any{"type": "code_execution"})
		}
	}
	return tools
}

func interactionGenerationConfig(config *genai.GenerateContentConfig) map[string]any {
	result := make(map[string]any)
	if config.MaxOutputTokens != 0 {
		result["max_output_tokens"] = config.MaxOutputTokens
	}
	if config.Temperature != nil {
		result["temperature"] = *config.Temperature
	}
	if config.TopP != nil {
		result["top_p"] = *config.TopP
	}
	if config.Seed != nil {
		result["seed"] = *config.Seed
	}
	if len(config.StopSequences) > 0 {
		result["stop_sequences"] = config.StopSequences
	}
	if config.ThinkingConfig != nil && config.ThinkingConfig.IncludeThoughts {
		result["thinking_summaries"] = "auto"
	}
	if config.ThinkingConfig != nil && config.ThinkingConfig.ThinkingLevel != "" &&
		config.ThinkingConfig.ThinkingLevel != genai.ThinkingLevelUnspecified {
		result["thinking_level"] = strings.ToLower(string(config.ThinkingConfig.ThinkingLevel))
	}
	return result
}

func callGeminiInteraction(ctx context.Context, client *http.Client, endpoint, apiKey string,
	request interactionRequest,
) (*interactionResponse, []byte, error) {
	payload, err := json.Marshal(request)
	if err != nil {
		return nil, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", apiKey)
	req.Header.Set("Api-Revision", geminiInteractionsRevision)
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32*1024*1024))
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var apiError struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(body, &apiError)
		message := apiError.Error.Message
		if message == "" {
			message = http.StatusText(resp.StatusCode)
		}
		return nil, body, &interactionHTTPError{StatusCode: resp.StatusCode, Message: message}
	}
	var decoded interactionResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, body, fmt.Errorf("decode Gemini interaction response: %w", err)
	}
	if decoded.ID == "" {
		return nil, body, errors.New("Gemini Interactions API did not return an interaction id")
	}
	return &decoded, body, nil
}

func interactionResult(response *interactionResponse, raw []byte) (*AIResult, error) {
	result := &AIResult{
		InteractionID: response.ID,
		Raw:           append([]byte(nil), raw...),
		Usage: AIUsage{
			InputTokens:       response.Usage.TotalInputTokens,
			CachedInputTokens: response.Usage.TotalCachedTokens,
			OutputTokens:      response.Usage.TotalOutputTokens + response.Usage.TotalThoughtTokens,
			ThinkingTokens:    response.Usage.TotalThoughtTokens,
		},
	}
	modelSteps := make([]json.RawMessage, 0, len(response.Steps))
	for _, rawStep := range response.Steps {
		var step struct {
			Type    string `json:"type"`
			Status  string `json:"status"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if err := json.Unmarshal(rawStep, &step); err != nil {
			return nil, fmt.Errorf("decode Gemini interaction step: %w", err)
		}
		if step.Type != "user_input" {
			modelSteps = append(modelSteps, append(json.RawMessage(nil), rawStep...))
		}
		if step.Type == "model_output" && (step.Status == "" || step.Status == "done" || step.Status == "completed") {
			var display strings.Builder
			for _, block := range step.Content {
				if block.Type == "text" {
					display.WriteString(block.Text)
				}
			}
			if display.Len() > 0 {
				result.DisplayText = display.String()
			}
		}
	}
	payload, err := json.Marshal(modelSteps)
	if err != nil {
		return nil, fmt.Errorf("serialize Gemini interaction steps: %w", err)
	}
	result.AssistantPayload = payload
	result.AssistantPayloadFormat = PayloadFormatGeminiInteractionSteps
	if response.Status != "" && response.Status != "completed" {
		result.Feedback = response.Status
	}
	return result, nil
}

func isInteractionPreviousIDError(err error) bool {
	var apiError *interactionHTTPError
	return errors.As(err, &apiError) && (apiError.StatusCode == http.StatusBadRequest || apiError.StatusCode == http.StatusNotFound)
}

func isInteractionCompatibilityError(err error) bool {
	var apiError *interactionHTTPError
	return errors.As(err, &apiError) && (apiError.StatusCode == http.StatusBadRequest ||
		apiError.StatusCode == http.StatusNotFound || apiError.StatusCode == http.StatusNotImplemented)
}

func generateContentFallback(ctx context.Context, session *GeminiSession, config *genai.GenerateContentConfig,
	window aiRequestWindow,
) (*AIResult, error) {
	contents := genaiContentsFromDatabase(window.Contents, session.AssistantPayloads)
	if session.HistoryRebuildLossy {
		historyEnd := len(window.Contents) - len(session.TmpContents)
		if historyEnd < 0 {
			historyEnd = 0
		}
		contents = make([]*genai.Content, 0, len(window.Contents))
		for index := range window.Contents[:historyEnd] {
			content := &window.Contents[index]
			if content.Role == genai.RoleModel {
				if content.Text.Valid && content.Text.String != "" {
					contents = append(contents, &genai.Content{Role: genai.RoleModel,
						Parts: []*genai.Part{{Text: content.Text.String}}})
				}
				continue
			}
			if len(content.Blob) > 0 && (!content.Text.Valid || content.Text.String == "") {
				continue
			}
			contents = append(contents, &genai.Content{Role: content.Role,
				Parts: []*genai.Part{{Text: compactUserText(content)}}})
		}
		contents = append(contents,
			genaiContentsFromDatabase(window.Contents[historyEnd:], session.AssistantPayloads)...)
	}
	response, err := generateGeminiContents(ctx, session.Model, contents, config)
	if err != nil {
		return nil, err
	}
	result, err := resultFromGeminiResponse(response)
	if err != nil {
		return nil, err
	}
	result.WindowStartMsgID = window.StartMsgID
	result.WindowDrop = window.Drop
	return result, nil
}

func generateGeminiWithInteractions(ctx context.Context, session *GeminiSession, systemPrompt string,
	config *genai.GenerateContentConfig, window aiRequestWindow,
) (*AIResult, error) {
	apiKey := g.GetConfig().GeminiKey
	if apiKey == "" {
		return nil, errors.New("Gemini API Key 未配置")
	}
	return generateGeminiWithInteractionsClient(ctx, session, systemPrompt, config, window,
		geminiInteractionsHTTPClient, geminiInteractionsEndpoint, apiKey)
}

func generateGeminiWithInteractionsClient(ctx context.Context, session *GeminiSession, systemPrompt string,
	config *genai.GenerateContentConfig, window aiRequestWindow, client *http.Client, endpoint, apiKey string,
) (*AIResult, error) {
	rebuild := window.RebuildHistory || session.GeminiInteractionID == ""
	inputContents := window.Contents
	previousID := session.GeminiInteractionID
	if !rebuild {
		inputContents = session.TmpContents
	} else {
		previousID = ""
	}
	steps, err := interactionStepsForContents(inputContents, session.AssistantPayloads)
	if rebuild {
		steps, err = interactionStepsForRebuild(session, inputContents)
	}
	if err != nil {
		return nil, err
	}
	request := interactionRequest{
		Model: session.Model, Input: steps, SystemInstruction: systemPrompt,
		Tools: interactionTools(config), Store: true,
		GenerationConfig: interactionGenerationConfig(config), PreviousInteractionID: previousID,
	}
	response, raw, err := callGeminiInteraction(ctx, client, endpoint, apiKey, request)
	if err != nil && previousID != "" && isInteractionPreviousIDError(err) {
		steps, buildErr := interactionStepsForRebuild(session, window.Contents)
		if buildErr != nil {
			return nil, buildErr
		}
		request.Input = steps
		request.PreviousInteractionID = ""
		response, raw, err = callGeminiInteraction(ctx, client, endpoint, apiKey, request)
	}
	if err != nil {
		if isInteractionCompatibilityError(err) {
			logger := log
			if logger == nil {
				logger = slog.Default()
			}
			logger.Warn("Gemini Interactions unavailable; falling back to generateContent", "err", err)
			config.SystemInstruction = genai.NewContentFromText(systemPrompt, genai.RoleModel)
			return generateContentFallback(ctx, session, config, window)
		}
		return nil, err
	}
	result, err := interactionResult(response, raw)
	if err != nil {
		return nil, err
	}
	result.WindowStartMsgID = window.StartMsgID
	result.WindowDrop = window.Drop
	return result, nil
}
