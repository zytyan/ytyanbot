package genbot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	g "main/globalcfg"
	"main/globalcfg/q"
	genai "main/handlers/genbot/geminiapi"
	"net/http"
	"time"
)

const (
	geminiExplicitCacheMinTokens       = int32(4096)
	geminiImplicitCacheLifetime        = 5 * time.Minute
	geminiExplicitCacheTTL             = 15 * time.Minute
	geminiExplicitCacheRenewBefore     = 5 * time.Minute
	geminiExplicitCacheRefreshMessages = 50
)

type geminiExplicitCacheState struct {
	Name        string
	ExpireTime  time.Time
	StartMsgID  int64
	EndMsgID    int64
	TokenCount  int64
	Fingerprint string
}

func explicitCacheFingerprint(model, systemPrompt string, tools []*genai.Tool) string {
	payload, _ := json.Marshal(struct {
		Model        string        `json:"model"`
		SystemPrompt string        `json:"system_prompt"`
		Tools        []*genai.Tool `json:"tools"`
	}{Model: model, SystemPrompt: systemPrompt, Tools: tools})
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func explicitCacheHistoryEnd(session *GeminiSession, window aiRequestWindow) int {
	end := len(window.Contents) - len(session.TmpContents)
	if end < 0 {
		return 0
	}
	return end
}

func explicitCacheEndIndex(contents []q.GeminiContent, state geminiExplicitCacheState) (int, bool) {
	if len(contents) == 0 || state.StartMsgID == 0 || state.EndMsgID == 0 ||
		contents[0].MsgID != state.StartMsgID {
		return 0, false
	}
	for index := range contents {
		if contents[index].MsgID == state.EndMsgID {
			return index + 1, true
		}
	}
	return 0, false
}

func shouldStartExplicitCache(session *GeminiSession, now time.Time) bool {
	if len(session.Contents) == 0 {
		return false
	}
	lastStable := session.Contents[len(session.Contents)-1].SentTime.Time
	return !lastStable.IsZero() && now.Sub(lastStable) >= geminiImplicitCacheLifetime
}

func logExplicitCache(message string, attrs ...any) {
	logger := log
	if logger == nil {
		logger = slog.Default()
	}
	logger.Info(message, attrs...)
}

func deleteExplicitCache(ctx context.Context, client *genai.Client, name string) {
	if name == "" {
		return
	}
	if err := client.Caches.Delete(ctx, name); err != nil {
		var apiError *genai.APIError
		if !errors.As(err, &apiError) || apiError.StatusCode != http.StatusNotFound {
			logExplicitCache("delete Gemini explicit cache failed", "cache", name, "err", err)
		}
	}
}

func stableExplicitCacheContents(session *GeminiSession, records []q.GeminiContent) []*genai.Content {
	if session.HistoryRebuildLossy {
		return lossyGeminiContents(records)
	}
	return genaiContentsFromDatabase(records, session.AssistantPayloads)
}

func createExplicitCache(ctx context.Context, client *genai.Client, session *GeminiSession,
	systemPrompt string, config *genai.GenerateContentConfig, window aiRequestWindow, now time.Time,
) (geminiExplicitCacheState, bool) {
	historyEnd := explicitCacheHistoryEnd(session, window)
	if historyEnd == 0 {
		return geminiExplicitCacheState{}, false
	}
	records := window.Contents[:historyEnd]
	contents := stableExplicitCacheContents(session, records)
	if len(contents) == 0 {
		return geminiExplicitCacheState{}, false
	}
	count, err := client.Models.CountTokens(ctx, session.Model, contents, nil)
	if err != nil {
		logExplicitCache("count Gemini explicit cache tokens failed", "session_id", session.ID, "err", err)
		return geminiExplicitCacheState{}, false
	}
	if count.TotalTokens < geminiExplicitCacheMinTokens {
		return geminiExplicitCacheState{}, false
	}
	created, err := client.Caches.Create(ctx, session.Model, &genai.CreateCachedContentConfig{
		DisplayName: fmt.Sprintf("ytyan-session-%d", session.ID), TTL: geminiExplicitCacheTTL,
		SystemInstruction: genai.NewContentFromText(systemPrompt, genai.RoleModel),
		Contents:          contents, Tools: config.Tools,
	})
	if err != nil {
		logExplicitCache("create Gemini explicit cache failed", "session_id", session.ID,
			"model", session.Model, "tokens", count.TotalTokens, "err", err)
		return geminiExplicitCacheState{}, false
	}
	if created.Name == "" {
		logExplicitCache("create Gemini explicit cache returned no name", "session_id", session.ID)
		return geminiExplicitCacheState{}, false
	}
	expireTime := created.ExpireTime
	if expireTime.IsZero() {
		expireTime = now.Add(geminiExplicitCacheTTL)
	}
	tokenCount := int64(count.TotalTokens)
	if created.UsageMetadata != nil && created.UsageMetadata.TotalTokenCount > 0 {
		tokenCount = int64(created.UsageMetadata.TotalTokenCount)
	}
	state := geminiExplicitCacheState{
		Name: created.Name, ExpireTime: expireTime,
		StartMsgID: records[0].MsgID, EndMsgID: records[len(records)-1].MsgID,
		TokenCount:  tokenCount,
		Fingerprint: explicitCacheFingerprint(session.Model, systemPrompt, config.Tools),
	}
	logExplicitCache("created Gemini explicit cache", "session_id", session.ID, "model", session.Model,
		"tokens", state.TokenCount, "ttl", geminiExplicitCacheTTL)
	return state, true
}

func ensureExplicitCache(ctx context.Context, client *genai.Client, session *GeminiSession,
	systemPrompt string, config *genai.GenerateContentConfig, window aiRequestWindow, now time.Time,
) (geminiExplicitCacheState, int, bool) {
	state := session.GeminiCache
	fingerprint := explicitCacheFingerprint(session.Model, systemPrompt, config.Tools)
	endIndex, prefixPresent := explicitCacheEndIndex(window.Contents, state)
	valid := state.Name != "" && now.Before(state.ExpireTime) && state.Fingerprint == fingerprint && prefixPresent
	if valid && len(window.Contents)-endIndex < geminiExplicitCacheRefreshMessages {
		if state.ExpireTime.Sub(now) <= geminiExplicitCacheRenewBefore {
			updated, err := client.Caches.UpdateTTL(ctx, state.Name, geminiExplicitCacheTTL)
			if err == nil {
				state.ExpireTime = updated.ExpireTime
				if state.ExpireTime.IsZero() {
					state.ExpireTime = now.Add(geminiExplicitCacheTTL)
				}
				session.GeminiCache = state
			} else {
				var apiError *genai.APIError
				if errors.As(err, &apiError) && apiError.StatusCode == http.StatusNotFound {
					valid = false
				} else {
					logExplicitCache("renew Gemini explicit cache failed", "session_id", session.ID, "err", err)
				}
			}
		}
		if valid {
			return state, endIndex, true
		}
	}

	oldName := state.Name
	hadCache := oldName != ""
	session.GeminiCache = geminiExplicitCacheState{}
	if !hadCache && !shouldStartExplicitCache(session, now) {
		return geminiExplicitCacheState{}, 0, false
	}
	created, ok := createExplicitCache(ctx, client, session, systemPrompt, config, window, now)
	if !ok {
		return geminiExplicitCacheState{}, 0, false
	}
	session.GeminiCache = created
	if oldName != "" && oldName != created.Name {
		deleteExplicitCache(ctx, client, oldName)
	}
	endIndex, ok = explicitCacheEndIndex(window.Contents, created)
	return created, endIndex, ok
}

func tryGeminiExplicitCache(ctx context.Context, client *genai.Client, session *GeminiSession,
	systemPrompt string, config *genai.GenerateContentConfig, window aiRequestWindow, now time.Time,
) (*AIResult, bool, error) {
	state, endIndex, ok := ensureExplicitCache(ctx, client, session, systemPrompt, config, window, now)
	if !ok {
		return nil, false, nil
	}
	tail := genaiContentsFromDatabase(window.Contents[endIndex:], session.AssistantPayloads)
	requestConfig := *config
	requestConfig.SystemInstruction = nil
	requestConfig.Tools = nil
	requestConfig.CachedContent = state.Name
	response, err := generateGeminiContentsWithClient(ctx, client, session.Model, tail, &requestConfig)
	if err != nil {
		var apiError *genai.APIError
		if errors.As(err, &apiError) && (apiError.StatusCode == http.StatusBadRequest ||
			apiError.StatusCode == http.StatusNotFound) {
			deleteExplicitCache(ctx, client, state.Name)
			session.GeminiCache = geminiExplicitCacheState{}
			return nil, false, nil
		}
		return nil, true, err
	}
	result, err := resultFromGeminiResponse(response)
	if err != nil {
		return nil, true, err
	}
	session.GeminiInteractionID = ""
	result.WindowStartMsgID = window.StartMsgID
	result.WindowDrop = window.Drop
	return result, true, nil
}

func generateGeminiWithCachePolicy(ctx context.Context, session *GeminiSession, systemPrompt string,
	config *genai.GenerateContentConfig, window aiRequestWindow,
) (*AIResult, error) {
	if !g.GeminiExplicitCacheEnabled() {
		return generateGeminiWithInteractions(ctx, session, systemPrompt, config, window)
	}
	result, used, err := tryGeminiExplicitCache(ctx, getGenAiClient(), session, systemPrompt, config, window, time.Now())
	if used || err != nil {
		return result, err
	}
	return generateGeminiWithInteractions(ctx, session, systemPrompt, config, window)
}
