package g

import (
	"context"
	"database/sql"
	"main/globalcfg/aiq"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAISettingsUseV2QueriesAndPersistProvider(t *testing.T) {
	ctx := context.Background()
	const chatID int64 = -930001
	model, err := GetAIChatModel(ctx, chatID, "fallback")
	require.NoError(t, err)
	require.Equal(t, "fallback", model)
	require.NoError(t, SetAIChatModel(ctx, chatID, "deepseek", "deepseek-v4-flash"))
	settings, err := AIQ.GetAIChatSettings(ctx, chatID)
	require.NoError(t, err)
	require.Equal(t, "deepseek", settings.DefaultProvider)
	require.Equal(t, "deepseek-v4-flash", settings.DefaultModel)

	enabled, err := ToggleAIChatUsage(ctx, chatID, "gemini", "fallback")
	require.NoError(t, err)
	require.True(t, enabled)
	enabled, err = GetAIChatUsageEnabled(ctx, chatID)
	require.NoError(t, err)
	require.True(t, enabled)
}

func TestAISessionProviderStateIsVersionedJSONAndClearedOnModelChange(t *testing.T) {
	ctx := context.Background()
	now := time.Now().Unix()
	session, err := AIQ.CreateAISession(ctx, aiq.CreateAISessionParams{
		ChatID: -930002, ChatName: "test", ChatType: "private", Provider: "gemini",
		Model: "gemini-3-flash-preview", CreatedAt: now, UpdatedAt: now,
	})
	require.NoError(t, err)
	state := AISessionRuntimeState{
		GeminiInteractionID: "interaction", WindowStartMsgID: 51,
		GeminiCacheName: "cachedContents/test", GeminiCacheExpireTime: 1_800_000_000,
	}
	payload, err := EncodeAISessionRuntimeState(state)
	require.NoError(t, err)
	require.NoError(t, AIQ.UpsertAISessionProviderState(ctx, aiq.UpsertAISessionProviderStateParams{
		SessionID: session.ID, Provider: "gemini", StateVersion: 1,
		StateJson: payload, UpdatedAt: now,
	}))
	loaded, err := GetAISessionRuntimeState(ctx, session.ID)
	require.NoError(t, err)
	require.Equal(t, state, loaded)

	require.NoError(t, ChangeAISessionModel(ctx, session.ID, "deepseek", "deepseek-v4-flash"))
	_, err = AIQ.GetAISessionProviderState(ctx, session.ID)
	require.ErrorIs(t, err, sql.ErrNoRows)
	loaded, err = GetAISessionRuntimeState(ctx, session.ID)
	require.NoError(t, err)
	require.True(t, loaded.HistoryRebuildLossy)
	provider, model, err := GetAISessionModel(ctx, session.ID)
	require.NoError(t, err)
	require.Equal(t, "deepseek", provider)
	require.Equal(t, "deepseek-v4-flash", model)
}
