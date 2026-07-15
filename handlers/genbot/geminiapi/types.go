// Package geminiapi contains the small subset of the Gemini Developer API used
// by the bot. Keeping these wire types local avoids coupling the provider layer
// to Google's generated Go SDK while preserving the existing JSON payloads.
package geminiapi

import (
	"encoding/json"
	"strings"
	"time"
)

const (
	RoleUser  = "user"
	RoleModel = "model"
)

type Outcome string

const (
	OutcomeUnspecified      Outcome = "OUTCOME_UNSPECIFIED"
	OutcomeOK               Outcome = "OUTCOME_OK"
	OutcomeFailed           Outcome = "OUTCOME_FAILED"
	OutcomeDeadlineExceeded Outcome = "OUTCOME_DEADLINE_EXCEEDED"
)

type Language string

const (
	LanguageUnspecified Language = "LANGUAGE_UNSPECIFIED"
	LanguagePython      Language = "PYTHON"
)

type ThinkingLevel string

const (
	ThinkingLevelUnspecified ThinkingLevel = "THINKING_LEVEL_UNSPECIFIED"
	ThinkingLevelMinimal     ThinkingLevel = "MINIMAL"
	ThinkingLevelLow         ThinkingLevel = "LOW"
	ThinkingLevelMedium      ThinkingLevel = "MEDIUM"
	ThinkingLevelHigh        ThinkingLevel = "HIGH"
)

type ToolType string
type BlockedReason string
type FinishReason string

type Blob struct {
	Data        []byte `json:"data,omitempty"`
	DisplayName string `json:"displayName,omitempty"`
	MIMEType    string `json:"mimeType,omitempty"`
}

type FileData struct {
	DisplayName string `json:"displayName,omitempty"`
	FileURI     string `json:"fileUri,omitempty"`
	MIMEType    string `json:"mimeType,omitempty"`
}

type ExecutableCode struct {
	Code     string   `json:"code,omitempty"`
	Language Language `json:"language,omitempty"`
	ID       string   `json:"id,omitempty"`
}

type CodeExecutionResult struct {
	Outcome Outcome `json:"outcome,omitempty"`
	Output  string  `json:"output,omitempty"`
	ID      string  `json:"id,omitempty"`
}

type FunctionCall struct {
	ID   string         `json:"id,omitempty"`
	Args map[string]any `json:"args,omitempty"`
	Name string         `json:"name,omitempty"`
}

type FunctionResponse struct {
	ID       string         `json:"id,omitempty"`
	Name     string         `json:"name,omitempty"`
	Response map[string]any `json:"response,omitempty"`
}

type ToolCall struct {
	ID       string         `json:"id,omitempty"`
	ToolType ToolType       `json:"toolType,omitempty"`
	Args     map[string]any `json:"args,omitempty"`
}

type ToolResponse struct {
	ID       string         `json:"id,omitempty"`
	ToolType ToolType       `json:"toolType,omitempty"`
	Response map[string]any `json:"response,omitempty"`
}

type Part struct {
	CodeExecutionResult *CodeExecutionResult `json:"codeExecutionResult,omitempty"`
	ExecutableCode      *ExecutableCode      `json:"executableCode,omitempty"`
	FileData            *FileData            `json:"fileData,omitempty"`
	FunctionCall        *FunctionCall        `json:"functionCall,omitempty"`
	FunctionResponse    *FunctionResponse    `json:"functionResponse,omitempty"`
	InlineData          *Blob                `json:"inlineData,omitempty"`
	Text                string               `json:"text,omitempty"`
	Thought             bool                 `json:"thought,omitempty"`
	ThoughtSignature    []byte               `json:"thoughtSignature,omitempty"`
	ToolCall            *ToolCall            `json:"toolCall,omitempty"`
	ToolResponse        *ToolResponse        `json:"toolResponse,omitempty"`
	PartMetadata        map[string]any       `json:"partMetadata,omitempty"`
}

type Content struct {
	Parts []*Part `json:"parts,omitempty"`
	Role  string  `json:"role,omitempty"`
}

func NewPartFromText(text string) *Part {
	return &Part{Text: text}
}

func NewPartFromBytes(data []byte, mimeType string) *Part {
	return &Part{InlineData: &Blob{Data: data, MIMEType: mimeType}}
}

func NewContentFromText(text, role string) *Content {
	if role == "" {
		role = RoleUser
	}
	return &Content{Role: role, Parts: []*Part{NewPartFromText(text)}}
}

type GoogleSearch struct{}
type ToolCodeExecution struct{}

type Tool struct {
	GoogleSearch  *GoogleSearch      `json:"googleSearch,omitempty"`
	CodeExecution *ToolCodeExecution `json:"codeExecution,omitempty"`
}

type ThinkingConfig struct {
	IncludeThoughts bool          `json:"includeThoughts,omitempty"`
	ThinkingBudget  *int32        `json:"thinkingBudget,omitempty"`
	ThinkingLevel   ThinkingLevel `json:"thinkingLevel,omitempty"`
}

type GenerateContentConfig struct {
	SystemInstruction *Content        `json:"systemInstruction,omitempty"`
	Temperature       *float32        `json:"temperature,omitempty"`
	TopP              *float32        `json:"topP,omitempty"`
	TopK              *float32        `json:"topK,omitempty"`
	CandidateCount    int32           `json:"candidateCount,omitempty"`
	MaxOutputTokens   int32           `json:"maxOutputTokens,omitempty"`
	StopSequences     []string        `json:"stopSequences,omitempty"`
	Seed              *int32          `json:"seed,omitempty"`
	ResponseMIMEType  string          `json:"responseMimeType,omitempty"`
	Tools             []*Tool         `json:"tools,omitempty"`
	CachedContent     string          `json:"cachedContent,omitempty"`
	ThinkingConfig    *ThinkingConfig `json:"thinkingConfig,omitempty"`
}

type GroundingMetadata struct {
	ImageSearchQueries []string `json:"imageSearchQueries,omitempty"`
	WebSearchQueries   []string `json:"webSearchQueries,omitempty"`
}

type Candidate struct {
	Content           *Content           `json:"content,omitempty"`
	FinishMessage     string             `json:"finishMessage,omitempty"`
	TokenCount        int32              `json:"tokenCount,omitempty"`
	FinishReason      FinishReason       `json:"finishReason,omitempty"`
	GroundingMetadata *GroundingMetadata `json:"groundingMetadata,omitempty"`
	AvgLogprobs       float64            `json:"avgLogprobs,omitempty"`
	Index             int32              `json:"index,omitempty"`
}

type GenerateContentResponsePromptFeedback struct {
	BlockReason        BlockedReason `json:"blockReason,omitempty"`
	BlockReasonMessage string        `json:"blockReasonMessage,omitempty"`
}

type GenerateContentResponseUsageMetadata struct {
	CachedContentTokenCount int32 `json:"cachedContentTokenCount,omitempty"`
	CandidatesTokenCount    int32 `json:"candidatesTokenCount,omitempty"`
	PromptTokenCount        int32 `json:"promptTokenCount,omitempty"`
	ThoughtsTokenCount      int32 `json:"thoughtsTokenCount,omitempty"`
	ToolUsePromptTokenCount int32 `json:"toolUsePromptTokenCount,omitempty"`
	TotalTokenCount         int32 `json:"totalTokenCount,omitempty"`
}

type GenerateContentResponse struct {
	Candidates     []*Candidate                           `json:"candidates,omitempty"`
	ModelVersion   string                                 `json:"modelVersion,omitempty"`
	PromptFeedback *GenerateContentResponsePromptFeedback `json:"promptFeedback,omitempty"`
	ResponseID     string                                 `json:"responseId,omitempty"`
	UsageMetadata  *GenerateContentResponseUsageMetadata  `json:"usageMetadata,omitempty"`
	raw            []byte
}

func (r *GenerateContentResponse) Text() string {
	if r == nil || len(r.Candidates) == 0 || r.Candidates[0].Content == nil {
		return ""
	}
	var text strings.Builder
	for _, part := range r.Candidates[0].Content.Parts {
		if part != nil && !part.Thought {
			text.WriteString(part.Text)
		}
	}
	return text.String()
}

func (r *GenerateContentResponse) MarshalJSON() ([]byte, error) {
	if len(r.raw) > 0 {
		return append([]byte(nil), r.raw...), nil
	}
	type wire GenerateContentResponse
	return json.Marshal((*wire)(r))
}

func (r *GenerateContentResponse) UnmarshalJSON(data []byte) error {
	type wire GenerateContentResponse
	var decoded wire
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*r = GenerateContentResponse(decoded)
	r.raw = append(r.raw[:0], data...)
	return nil
}

type CountTokensConfig struct {
	SystemInstruction *Content `json:"systemInstruction,omitempty"`
	Tools             []*Tool  `json:"tools,omitempty"`
}

type CountTokensResponse struct {
	TotalTokens             int32 `json:"totalTokens,omitempty"`
	CachedContentTokenCount int32 `json:"cachedContentTokenCount,omitempty"`
}

type CreateCachedContentConfig struct {
	DisplayName       string
	SystemInstruction *Content
	Contents          []*Content
	Tools             []*Tool
	TTL               time.Duration
}

type CachedContentUsageMetadata struct {
	TotalTokenCount int32 `json:"totalTokenCount,omitempty"`
}

type CachedContent struct {
	Name          string                      `json:"name,omitempty"`
	DisplayName   string                      `json:"displayName,omitempty"`
	Model         string                      `json:"model,omitempty"`
	CreateTime    time.Time                   `json:"createTime,omitempty"`
	UpdateTime    time.Time                   `json:"updateTime,omitempty"`
	ExpireTime    time.Time                   `json:"expireTime,omitempty"`
	UsageMetadata *CachedContentUsageMetadata `json:"usageMetadata,omitempty"`
}
