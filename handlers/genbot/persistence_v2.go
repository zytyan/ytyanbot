package genbot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	g "main/globalcfg"
	"main/globalcfg/aiq"
	"main/globalcfg/q"
	"main/helpers/aimedia"
	"strings"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
)

func aiMediaStore() (*aimedia.Store, error) {
	return aimedia.NewStore(g.GetConfig().AIMediaPath)
}

func insertAIContent(ctx context.Context, queries *aiq.Queries, store *aimedia.Store,
	content q.GeminiContent, position int64, contextOnly bool,
) error {
	msgType := content.MsgType
	if msgType == "" {
		msgType = "text"
	}
	if err := queries.InsertAIMessage(ctx, aiq.InsertAIMessageParams{
		ChatID: content.ChatID, MsgID: content.MsgID, SentAt: content.SentTime.Unix(),
		UserID: content.UserID, Username: content.Username, AtableUsername: content.AtableUsername,
		MsgType: msgType, Text: content.Text, ReplyToMsgID: content.ReplyToMsgID,
	}); err != nil {
		return err
	}
	if len(content.Blob) > 0 {
		if store == nil {
			return errors.New("AI media store is not initialized")
		}
		if !content.MimeType.Valid || content.MimeType.String == "" {
			return fmt.Errorf("message %d has media without MIME type", content.MsgID)
		}
		object, err := store.Put(content.Blob)
		if err != nil {
			return err
		}
		if err = queries.InsertMediaObject(ctx, aiq.InsertMediaObjectParams{
			Sha256: object.SHA256, RelativePath: object.RelativePath, ByteSize: object.Size,
			MimeType: content.MimeType.String, CreatedAt: time.Now().Unix(),
		}); err != nil {
			return err
		}
		if err = queries.AddAIMessageMedia(ctx, aiq.AddAIMessageMediaParams{
			ChatID: content.ChatID, MsgID: content.MsgID, Ordinal: 0,
			MediaSha256: object.SHA256, MediaKind: msgType,
		}); err != nil {
			return err
		}
	}
	contextFlag := int64(0)
	if contextOnly {
		contextFlag = 1
	}
	return queries.AddAISessionMessage(ctx, aiq.AddAISessionMessageParams{
		SessionID: content.SessionID, Position: position, ChatID: content.ChatID, MsgID: content.MsgID,
		Role: content.Role, QuotePart: content.QuotePart, ContextOnly: contextFlag,
	})
}

// BeginAIRun durably records all input and quoted context before a provider is
// called. The unique request key makes retries return the existing Run.
func (s *GeminiSession) BeginAIRun(ctx context.Context) (run aiq.AiRun, err error) {
	if len(s.TmpContents) == 0 {
		return run, errors.New("cannot start AI run without a request message")
	}
	var store *aimedia.Store
	for i := range s.TmpContents {
		if len(s.TmpContents[i].Blob) > 0 {
			store, err = aiMediaStore()
			if err != nil {
				return run, err
			}
			break
		}
	}
	tx, err := g.RawMainDb().BeginTx(ctx, nil)
	if err != nil {
		return run, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	queries := g.AIQ.WithTx(tx)
	position, err := queries.GetNextAISessionMessagePosition(ctx, s.ID)
	if err != nil {
		return run, err
	}
	for i := range s.TmpContents {
		_, contextOnly := s.TmpContextOnlyMsgIDs[s.TmpContents[i].MsgID]
		if err = insertAIContent(ctx, queries, store, s.TmpContents[i], position, contextOnly); err != nil {
			return run, err
		}
		position++
	}
	if err = queries.TouchAISession(ctx, time.Now().Unix(), s.ID); err != nil {
		return run, err
	}
	request := s.TmpContents[len(s.TmpContents)-1]
	run, err = queries.CreateAIRun(ctx, aiq.CreateAIRunParams{
		SessionID: s.ID, RequestChatID: request.ChatID, RequestMsgID: request.MsgID,
		Provider: s.Provider, Model: s.Model, RequestedAt: time.Now().Unix(),
	})
	if err != nil {
		return run, err
	}
	if err = tx.Commit(); err != nil {
		return run, err
	}
	s.TmpPersisted = true
	return run, nil
}

func (s *GeminiSession) GetOrBeginAIRun(ctx context.Context, chatID, msgID int64) (aiq.AiRun, error) {
	run, err := g.AIQ.GetAIRunByRequest(ctx, s.ID, chatID, msgID)
	if err == nil {
		return run, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return run, err
	}
	return s.BeginAIRun(ctx)
}

func (s *GeminiSession) candidateRuntimeState(result *AIResult) g.AISessionRuntimeState {
	cacheExpire := int64(0)
	if !s.GeminiCache.ExpireTime.IsZero() {
		cacheExpire = s.GeminiCache.ExpireTime.Unix()
	}
	return g.AISessionRuntimeState{
		GeminiInteractionID: result.InteractionID, WindowStartMsgID: result.WindowStartMsgID,
		GeminiCacheName: s.GeminiCache.Name, GeminiCacheExpireTime: cacheExpire,
		GeminiCacheStartMsgID: s.GeminiCache.StartMsgID, GeminiCacheEndMsgID: s.GeminiCache.EndMsgID,
		GeminiCacheTokenCount: s.GeminiCache.TokenCount, GeminiCacheFingerprint: s.GeminiCache.Fingerprint,
	}
}

// CompleteAIRun stores the generated result and increments aggregate Token
// counters only when pending changes to generated for the first time.
func (s *GeminiSession) CompleteAIRun(ctx context.Context, runID int64, result *AIResult) (err error) {
	stateJSON, err := g.EncodeAISessionRuntimeState(s.candidateRuntimeState(result))
	if err != nil {
		return err
	}
	cacheExpire := s.candidateRuntimeState(result).GeminiCacheExpireTime
	tx, err := g.RawMainDb().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	queries := g.AIQ.WithTx(tx)
	rows, err := queries.MarkAIRunGenerated(ctx, aiq.MarkAIRunGeneratedParams{
		CompletedAt:            sql.NullInt64{Int64: time.Now().Unix(), Valid: true},
		InputTokens:            sql.NullInt64{Int64: result.Usage.InputTokens, Valid: true},
		OutputTokens:           sql.NullInt64{Int64: result.Usage.OutputTokens, Valid: true},
		CachedInputTokens:      sql.NullInt64{Int64: result.Usage.CachedInputTokens, Valid: true},
		InputMessageCount:      sql.NullInt64{Int64: result.InputMessageCount, Valid: true},
		InputFirstMsgID:        sql.NullInt64{Int64: result.InputFirstMsgID, Valid: result.InputMessageCount > 0},
		InputLastMsgID:         sql.NullInt64{Int64: result.InputLastMsgID, Valid: result.InputMessageCount > 0},
		ResponseText:           sql.NullString{String: result.DisplayText, Valid: result.DisplayText != ""},
		RawPayload:             result.Raw,
		ThoughtSignature:       sql.NullString{String: result.ThoughtSignature, Valid: result.ThoughtSignature != ""},
		AssistantPayload:       result.AssistantPayload,
		AssistantPayloadFormat: sql.NullString{String: result.AssistantPayloadFormat, Valid: result.AssistantPayloadFormat != ""},
		CacheExpireAt:          sql.NullInt64{Int64: cacheExpire, Valid: cacheExpire != 0},
		CandidateStateJson:     sql.NullString{String: stateJSON, Valid: true}, RunID: runID,
	})
	if err != nil {
		return err
	}
	if rows == 1 {
		err = queries.IncrementAISessionUsage(ctx, aiq.IncrementAISessionUsageParams{
			InputTokens: result.Usage.InputTokens, OutputTokens: result.Usage.OutputTokens,
			CachedInputTokens: result.Usage.CachedInputTokens, UpdatedAt: time.Now().Unix(), SessionID: s.ID,
		})
		if err != nil {
			return err
		}
	} else {
		saved, lookupErr := queries.GetAIRun(ctx, runID)
		if lookupErr != nil {
			return lookupErr
		}
		if saved.Status != "generated" && saved.Status != "delivery_failed" && saved.Status != "delivered" {
			return fmt.Errorf("AI run %d cannot complete from status %s", runID, saved.Status)
		}
	}
	return tx.Commit()
}

func MarkAIRunFailed(ctx context.Context, runID int64, status, code string, cause error) error {
	message := ""
	if cause != nil {
		message = cause.Error()
	}
	_, err := g.AIQ.MarkAIRunFailed(ctx, aiq.MarkAIRunFailedParams{
		Status: status, CompletedAt: sql.NullInt64{Int64: time.Now().Unix(), Valid: true},
		ErrorCode:    sql.NullString{String: code, Valid: code != ""},
		ErrorMessage: sql.NullString{String: message, Valid: message != ""}, RunID: runID,
	})
	return err
}

func resultFromStoredRun(run aiq.AiRun) *AIResult {
	result := &AIResult{
		DisplayText: run.ResponseText.String, Raw: append([]byte(nil), run.RawPayload...),
		AssistantPayload:       append([]byte(nil), run.AssistantPayload...),
		AssistantPayloadFormat: run.AssistantPayloadFormat.String,
		ThoughtSignature:       run.ThoughtSignature.String,
		Usage: AIUsage{InputTokens: run.InputTokens.Int64, OutputTokens: run.OutputTokens.Int64,
			CachedInputTokens: run.CachedInputTokens.Int64},
		InputMessageCount: run.InputMessageCount.Int64, InputFirstMsgID: run.InputFirstMsgID.Int64,
		InputLastMsgID: run.InputLastMsgID.Int64,
	}
	if run.CandidateStateJson.Valid {
		var state g.AISessionRuntimeState
		if err := json.Unmarshal([]byte(run.CandidateStateJson.String), &state); err == nil {
			result.InteractionID = state.GeminiInteractionID
			result.WindowStartMsgID = state.WindowStartMsgID
		}
	}
	return result
}

func modelContent(sessionID int64, msg *gotgbot.Message, result *AIResult) q.GeminiContent {
	content := q.GeminiContent{
		SessionID: sessionID, ChatID: msg.Chat.Id, MsgID: msg.MessageId, Role: "model",
		SentTime: q.UnixTime{Time: time.Unix(msg.Date, 0)}, Username: mainBot.FirstName,
		MsgType: "text", Text: sql.NullString{String: result.DisplayText, Valid: result.DisplayText != ""},
		UserID: mainBot.Id,
	}
	content.AtableUsername = sql.NullString{String: mainBot.Username, Valid: mainBot.Username != ""}
	content.ThoughtSignature = sql.NullString{String: result.ThoughtSignature, Valid: result.ThoughtSignature != ""}
	return content
}

// DeliverAIRun atomically stores the Telegram response, marks the Run
// delivered, and promotes the candidate provider state.
func (s *GeminiSession) DeliverAIRun(ctx context.Context, runID int64, msg *gotgbot.Message, result *AIResult) (err error) {
	run, err := g.AIQ.GetAIRun(ctx, runID)
	if err != nil {
		return err
	}
	if run.SessionID != s.ID {
		return fmt.Errorf("AI run %d belongs to session %d, not %d", runID, run.SessionID, s.ID)
	}
	tx, err := g.RawMainDb().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	queries := g.AIQ.WithTx(tx)
	storedSession, err := queries.GetAISession(ctx, s.ID)
	if err != nil {
		return err
	}
	promoteState := storedSession.Provider == run.Provider && storedSession.Model == run.Model
	position, err := queries.GetNextAISessionMessagePosition(ctx, s.ID)
	if err != nil {
		return err
	}
	content := modelContent(s.ID, msg, result)
	if err = insertAIContent(ctx, queries, nil, content, position, false); err != nil {
		return err
	}
	rows, err := queries.MarkAIRunDelivered(ctx,
		sql.NullInt64{Int64: msg.Chat.Id, Valid: true}, sql.NullInt64{Int64: msg.MessageId, Valid: true}, runID)
	if err != nil {
		return err
	}
	if rows != 1 {
		return fmt.Errorf("AI run %d is not deliverable", runID)
	}
	stateJSON := run.CandidateStateJson.String
	if stateJSON == "" {
		stateJSON, err = g.EncodeAISessionRuntimeState(s.candidateRuntimeState(result))
		if err != nil {
			return err
		}
	}
	if promoteState {
		if err = queries.UpsertAISessionProviderState(ctx, aiq.UpsertAISessionProviderStateParams{
			SessionID: s.ID, Provider: run.Provider, StateVersion: 1,
			StateJson: stateJSON, UpdatedAt: time.Now().Unix(),
		}); err != nil {
			return err
		}
		if err = queries.ClearAISessionHistoryRebuildLossy(ctx, time.Now().Unix(), s.ID); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	s.applyDeliveredRun(content, result, run.Provider, promoteState)
	var promoted g.AISessionRuntimeState
	if promoteState && json.Unmarshal([]byte(stateJSON), &promoted) == nil {
		s.GeminiInteractionID = promoted.GeminiInteractionID
		s.WindowStartMsgID = promoted.WindowStartMsgID
		s.GeminiCache = geminiExplicitCacheState{
			Name: promoted.GeminiCacheName, StartMsgID: promoted.GeminiCacheStartMsgID,
			EndMsgID: promoted.GeminiCacheEndMsgID, TokenCount: promoted.GeminiCacheTokenCount,
			Fingerprint: promoted.GeminiCacheFingerprint,
		}
		if promoted.GeminiCacheExpireTime != 0 {
			s.GeminiCache.ExpireTime = time.Unix(promoted.GeminiCacheExpireTime, 0)
		}
	}
	return nil
}

func (s *GeminiSession) applyDeliveredRun(content q.GeminiContent, result *AIResult, provider string, promoteState bool) {
	for _, item := range s.TmpContents {
		if _, contextOnly := s.TmpContextOnlyMsgIDs[item.MsgID]; !contextOnly {
			s.Contents = append(s.Contents, item)
		}
	}
	s.Contents = append(s.Contents, content)
	if result.WindowDrop > 0 {
		if result.WindowDrop >= len(s.Contents) {
			s.Contents = nil
		} else {
			s.Contents = s.Contents[result.WindowDrop:]
		}
	}
	if s.AssistantPayloads == nil {
		s.AssistantPayloads = make(map[int64]g.AIAssistantPayload)
	}
	s.AssistantPayloads[content.MsgID] = g.AIAssistantPayload{
		MsgID: content.MsgID, Provider: provider, Format: result.AssistantPayloadFormat,
		Payload: append([]byte(nil), result.AssistantPayload...),
	}
	if promoteState {
		s.GeminiInteractionID = result.InteractionID
		s.WindowStartMsgID = result.WindowStartMsgID
		s.HistoryRebuildLossy = false
	}
	s.TmpContents = nil
	s.TmpContextOnlyMsgIDs = nil
	s.TmpPersisted = false
	s.UpdateTime = time.Now()
	s.refreshCapabilities()
}

func loadAIContentMedia(ctx context.Context, content *q.GeminiContent) error {
	media, err := g.AIQ.ListAIMessageMedia(ctx, content.ChatID, content.MsgID)
	if err != nil {
		return err
	}
	if len(media) == 0 {
		return nil
	}
	store, err := aiMediaStore()
	if err != nil {
		return err
	}
	file, err := store.Open(media[0].Sha256)
	if err != nil {
		return err
	}
	defer file.Close()
	content.Blob, err = io.ReadAll(file)
	if err != nil {
		return err
	}
	content.MimeType = sql.NullString{String: media[0].MimeType, Valid: true}
	return nil
}

func sessionFromV2(row aiq.AiSession) q.GeminiSession {
	return q.GeminiSession{
		ID: row.ID, ChatID: row.ChatID, ChatName: row.ChatName, ChatType: row.ChatType,
		Frozen: row.Status != "active", TotalInputTokens: row.TotalInputTokens,
		TotalOutputTokens: row.TotalOutputTokens,
	}
}

func normalizeAIRunErrorCode(err error) string {
	if err == nil {
		return ""
	}
	code := strings.TrimSpace(fmt.Sprintf("%T", err))
	if len(code) > 120 {
		code = code[:120]
	}
	return code
}
