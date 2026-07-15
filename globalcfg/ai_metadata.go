package g

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type AIMessageUsage struct {
	SessionID             int64
	Provider              string
	Model                 string
	InputTokens           int64
	OutputTokens          int64
	CachedInputTokens     int64
	InputMessageCount     int64
	InputFirstMsgID       int64
	InputLastMsgID        int64
	GeminiCacheExpireTime int64
}

type AIAssistantPayload struct {
	MsgID    int64
	Provider string
	Format   string
	Payload  []byte
}

// AISessionRuntimeState is provider-private state. It is serialized into the
// versioned ai_session_provider_state JSON document and is promoted only when
// the matching Telegram response has been delivered.
type AISessionRuntimeState struct {
	GeminiInteractionID    string `json:"gemini_interaction_id,omitempty"`
	WindowStartMsgID       int64  `json:"window_start_msg_id,omitempty"`
	GeminiCacheName        string `json:"gemini_cache_name,omitempty"`
	GeminiCacheExpireTime  int64  `json:"gemini_cache_expire_time,omitempty"`
	GeminiCacheStartMsgID  int64  `json:"gemini_cache_start_msg_id,omitempty"`
	GeminiCacheEndMsgID    int64  `json:"gemini_cache_end_msg_id,omitempty"`
	GeminiCacheTokenCount  int64  `json:"gemini_cache_token_count,omitempty"`
	GeminiCacheFingerprint string `json:"gemini_cache_fingerprint,omitempty"`
	HistoryRebuildLossy    bool   `json:"history_rebuild_lossy,omitempty"`
}

func GetAIChatModel(ctx context.Context, chatID int64, fallback string) (string, error) {
	settings, err := AIQ.GetAIChatSettings(ctx, chatID)
	if errors.Is(err, sql.ErrNoRows) {
		return fallback, nil
	}
	return settings.DefaultModel, err
}

func GetAIChatSelection(ctx context.Context, chatID int64, fallbackProvider, fallbackModel string) (string, string, error) {
	settings, err := AIQ.GetAIChatSettings(ctx, chatID)
	if errors.Is(err, sql.ErrNoRows) {
		return fallbackProvider, fallbackModel, nil
	}
	if err != nil {
		return "", "", err
	}
	return settings.DefaultProvider, settings.DefaultModel, nil
}

func SetAIChatModel(ctx context.Context, chatID int64, provider, model string) error {
	_, err := AIQ.SetAIChatModelSetting(ctx, chatID, provider, model, time.Now().Unix())
	return err
}

func GetAIChatUsageEnabled(ctx context.Context, chatID int64) (bool, error) {
	settings, err := AIQ.GetAIChatSettings(ctx, chatID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return settings.ShowUsage != 0, err
}

func ToggleAIChatUsage(ctx context.Context, chatID int64, fallbackProvider, fallbackModel string) (enabled bool, err error) {
	settings, err := AIQ.ToggleAIChatSettingsUsage(ctx, chatID, fallbackProvider, fallbackModel, time.Now().Unix())
	if err != nil {
		return false, err
	}
	return settings.ShowUsage != 0, nil
}

func GetAISessionModel(ctx context.Context, sessionID int64) (provider, model string, err error) {
	session, err := AIQ.GetAISession(ctx, sessionID)
	if err != nil {
		return "", "", err
	}
	return session.Provider, session.Model, nil
}

func SetAISessionModel(ctx context.Context, sessionID int64, provider, model string) error {
	return AIQ.SetAISessionModel(ctx, provider, model, time.Now().Unix(), sessionID)
}

func ChangeAISessionModel(ctx context.Context, sessionID int64, provider, model string) (err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	queries := AIQ.WithTx(tx)
	if err = queries.SetAISessionModel(ctx, provider, model, time.Now().Unix(), sessionID); err != nil {
		return err
	}
	if err = queries.DeleteAISessionProviderState(ctx, sessionID); err != nil {
		return err
	}
	return tx.Commit()
}

func GetAISessionRuntimeState(ctx context.Context, sessionID int64) (AISessionRuntimeState, error) {
	row, err := AIQ.GetAISessionProviderState(ctx, sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		session, sessionErr := AIQ.GetAISession(ctx, sessionID)
		if sessionErr != nil {
			return AISessionRuntimeState{}, sessionErr
		}
		return AISessionRuntimeState{HistoryRebuildLossy: session.HistoryRebuildLossy != 0}, nil
	}
	if err != nil {
		return AISessionRuntimeState{}, err
	}
	var state AISessionRuntimeState
	if err = json.Unmarshal([]byte(row.StateJson), &state); err != nil {
		return state, fmt.Errorf("decode AI provider state for session %d: %w", sessionID, err)
	}
	return state, nil
}

func EncodeAISessionRuntimeState(state AISessionRuntimeState) (string, error) {
	payload, err := json.Marshal(state)
	return string(payload), err
}

func GetAISessionAssistantPayloads(ctx context.Context, sessionID int64) (map[int64]AIAssistantPayload, error) {
	runs, err := AIQ.ListAISessionAssistantRuns(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	result := make(map[int64]AIAssistantPayload, len(runs))
	for _, run := range runs {
		result[run.ResponseMsgID.Int64] = AIAssistantPayload{
			MsgID: run.ResponseMsgID.Int64, Provider: run.Provider,
			Format: run.AssistantPayloadFormat.String, Payload: append([]byte(nil), run.AssistantPayload...),
		}
	}
	return result, nil
}

func GetAIMessageUsage(ctx context.Context, chatID, msgID int64) (usage AIMessageUsage, err error) {
	run, err := AIQ.GetAIRunByResponse(ctx, sql.NullInt64{Int64: chatID, Valid: true}, sql.NullInt64{Int64: msgID, Valid: true})
	if err != nil {
		return usage, err
	}
	usage = AIMessageUsage{
		SessionID: run.SessionID, Provider: run.Provider, Model: run.Model,
		InputTokens: run.InputTokens.Int64, OutputTokens: run.OutputTokens.Int64,
		CachedInputTokens: run.CachedInputTokens.Int64,
		InputMessageCount: run.InputMessageCount.Int64, InputFirstMsgID: run.InputFirstMsgID.Int64,
		InputLastMsgID: run.InputLastMsgID.Int64, GeminiCacheExpireTime: run.CacheExpireAt.Int64,
	}
	return usage, nil
}

func HasAIMessageResponse(ctx context.Context, chatID, msgID int64) (bool, error) {
	_, err := AIQ.GetAIRunByResponse(ctx, sql.NullInt64{Int64: chatID, Valid: true}, sql.NullInt64{Int64: msgID, Valid: true})
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func MarkMessageAsUserInput(ctx context.Context, chatID, msgID int64) error {
	return AIQ.MarkAIMessageAsUserInput(ctx, chatID, msgID)
}
