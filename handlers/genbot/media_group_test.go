package genbot

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	g "main/globalcfg"
	"main/globalcfg/aiq"
	"main/globalcfg/q"
	genai "main/handlers/genbot/geminiapi"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"
	"github.com/stretchr/testify/require"
)

func putTestAlbum(t *testing.T, chatID int64, groupID string, expiresAt int64, photoOnly int64,
	photos ...aiq.UpsertAIMediaGroupPhotoParams,
) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, g.AIQ.UpsertAIMediaGroup(ctx, aiq.UpsertAIMediaGroupParams{
		ChatID: chatID, MediaGroupID: groupID, FirstSeenAt: expiresAt - 10,
		UpdatedAt: expiresAt - 9, ExpiresAt: expiresAt, PhotoOnly: photoOnly,
	}))
	for _, photo := range photos {
		require.NoError(t, g.AIQ.UpsertAIMediaGroupPhoto(ctx, photo))
	}
}

func TestCaptureAIMediaGroupPersistsPhotosAndMarksMixedGroups(t *testing.T) {
	chatID := int64(-941001)
	config := g.GetConfig()
	previousChats := append([]int64(nil), config.AIChats...)
	config.AIChats = append(config.AIChats, chatID)
	t.Cleanup(func() { config.AIChats = previousChats })
	user := &gotgbot.User{Id: 7, FirstName: "Alice", Username: "alice"}
	photo := &gotgbot.Message{MessageId: 3, Date: 100, Chat: gotgbot.Chat{Id: chatID}, From: user,
		MediaGroupId: "capture-album", Caption: "@testbot inspect",
		Photo: []gotgbot.PhotoSize{{FileId: "small"}, {FileId: "large"}}}
	require.True(t, IsAIMediaGroup(photo))
	require.NoError(t, CaptureAIMediaGroup(nil, &ext.Context{EffectiveMessage: photo}))
	group, err := g.AIQ.GetAIMediaGroup(context.Background(), chatID, "capture-album")
	require.NoError(t, err)
	require.Equal(t, int64(1), group.PhotoOnly)
	require.Equal(t, int64(100)+int64(aiMediaGroupTTL/time.Second), group.ExpiresAt)
	items, err := g.AIQ.ListAIMediaGroupPhotos(context.Background(), chatID, "capture-album")
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, "large", items[0].TelegramFileID)
	require.Equal(t, "@testbot inspect", items[0].Caption.String)

	video := &gotgbot.Message{MessageId: 4, Date: 101, Chat: gotgbot.Chat{Id: chatID}, From: user,
		MediaGroupId: "capture-album", Video: &gotgbot.Video{FileId: "video"}}
	require.NoError(t, CaptureAIMediaGroup(nil, &ext.Context{EffectiveMessage: video}))
	group, err = g.AIQ.GetAIMediaGroup(context.Background(), chatID, "capture-album")
	require.NoError(t, err)
	require.Zero(t, group.PhotoOnly)
	require.ErrorIs(t, validateAIMediaGroup(group, time.Unix(102, 0)), ErrAIMediaGroupMixed)
}

func TestAIMediaGroupWaitUsesLastActivityAndClaimsOnce(t *testing.T) {
	key := aiMediaGroupKey{chatID: -941002, mediaGroupID: "wait-album"}
	aiMediaGroupActivities.Lock()
	delete(aiMediaGroupActivities.items, key)
	aiMediaGroupActivities.Unlock()
	t.Cleanup(func() {
		aiMediaGroupActivities.Lock()
		delete(aiMediaGroupActivities.items, key)
		aiMediaGroupActivities.Unlock()
	})
	noteAIMediaGroupActivity(key)
	require.True(t, claimAIMediaGroup(key))
	require.False(t, claimAIMediaGroup(key))
	start := time.Now()
	go func() {
		time.Sleep(300 * time.Millisecond)
		noteAIMediaGroupActivity(key)
	}()
	require.NoError(t, waitForAIMediaGroup(context.Background(), key))
	require.GreaterOrEqual(t, time.Since(start), 1200*time.Millisecond)
}

func TestReplyToAlbumLoadsEveryPhotoInOrderAndMarksNewSessionContext(t *testing.T) {
	previousBot := mainBot
	mainBot = &gotgbot.Bot{User: gotgbot.User{Id: 999, FirstName: "bot", Username: "testbot"}}
	t.Cleanup(func() { mainBot = previousBot })
	chatID := int64(-941003)
	groupID := "reply-album"
	dir := t.TempDir()
	paths := map[string]string{}
	photos := make([]aiq.UpsertAIMediaGroupPhotoParams, 0, 3)
	for _, id := range []int64{13, 11, 12} {
		fileID := "album-file-" + string(rune('a'+id-11))
		path := dir + "/" + fileID + ".jpg"
		require.NoError(t, os.WriteFile(path, []byte(fileID), 0o600))
		paths[fileID] = path
		photos = append(photos, aiq.UpsertAIMediaGroupPhotoParams{
			ChatID: chatID, MediaGroupID: groupID, MsgID: id, SentAt: id, UserID: 7,
			Username: "Alice", Caption: sql.NullString{String: "caption", Valid: id == 11},
			TelegramFileID: fileID,
		})
	}
	putTestAlbum(t, chatID, groupID, time.Now().Add(time.Hour).Unix(), 1, photos...)
	bot := &gotgbot.Bot{BotClient: localMediaBotClient{paths: paths}}
	replied := &gotgbot.Message{MessageId: 12, Date: 12, Chat: gotgbot.Chat{Id: chatID},
		From: &gotgbot.User{Id: 7, FirstName: "Alice"}, MediaGroupId: groupID,
		Photo: []gotgbot.PhotoSize{{FileId: "album-file-b"}}}
	current := &gotgbot.Message{MessageId: 14, Date: 14, Chat: gotgbot.Chat{Id: chatID},
		From: &gotgbot.User{Id: 8, FirstName: "Bob"}, Text: "@testbot compare", ReplyToMessage: replied}
	session := &GeminiSession{GeminiSession: q.GeminiSession{ID: 941003}, Provider: ProviderGemini}
	require.NoError(t, session.AddTgMessageWithReplyMode(context.Background(), bot, current, true))
	require.Len(t, session.TmpContents, 4)
	require.Equal(t, []int64{11, 12, 13, 14}, []int64{
		session.TmpContents[0].MsgID, session.TmpContents[1].MsgID,
		session.TmpContents[2].MsgID, session.TmpContents[3].MsgID,
	})
	for _, id := range []int64{11, 12, 13} {
		_, contextOnly := session.TmpContextOnlyMsgIDs[id]
		require.True(t, contextOnly)
	}
	_, currentContextOnly := session.TmpContextOnlyMsgIDs[14]
	require.False(t, currentContextOnly)
	steps, err := interactionStepsForContents(session.TmpContents, nil)
	require.NoError(t, err)
	require.Equal(t, 3, strings.Count(string(bytesJoinRaw(steps)), `"type":"image"`))
	direct := &gotgbot.Message{MessageId: 12, Date: 12, Chat: gotgbot.Chat{Id: chatID},
		From: &gotgbot.User{Id: 7, FirstName: "Alice"}, MediaGroupId: groupID,
		Photo: []gotgbot.PhotoSize{{FileId: "album-file-b"}}}
	directSession := &GeminiSession{GeminiSession: q.GeminiSession{ID: 941004}, Provider: ProviderGemini}
	require.NoError(t, directSession.AddTgMessageWithReply(context.Background(), bot, direct))
	require.Equal(t, []int64{11, 12, 13}, []int64{
		directSession.TmpContents[0].MsgID, directSession.TmpContents[1].MsgID, directSession.TmpContents[2].MsgID,
	})
}

func TestAlbumProviderRequestsContainEveryImageAndRunUsesTriggerIdentity(t *testing.T) {
	contents := []q.GeminiContent{
		{ChatID: -941004, MsgID: 21, Role: genai.RoleUser, Username: "Alice", MsgType: "photo",
			SentTime: q.UnixTime{Time: time.Unix(21, 0)}, Blob: []byte("one"),
			MimeType: sql.NullString{String: "image/jpeg", Valid: true}},
		{ChatID: -941004, MsgID: 22, Role: genai.RoleUser, Username: "Alice", MsgType: "photo",
			SentTime: q.UnixTime{Time: time.Unix(22, 0)}, Blob: []byte("two"),
			MimeType: sql.NullString{String: "image/jpeg", Valid: true}},
	}
	geminiSession := &GeminiSession{TmpContents: contents, Provider: ProviderGemini}
	require.Len(t, geminiSession.ToGenaiContents(), 2)
	for _, content := range geminiSession.ToGenaiContents() {
		require.Len(t, content.Parts, 2)
		require.NotNil(t, content.Parts[1].InlineData)
	}
	deepSeekSession := &GeminiSession{TmpContents: contents, Provider: ProviderDeepSeek, Model: ModelDeepSeekVision}
	messages, err := deepSeekSession.ToDeepSeekMessages("system")
	require.NoError(t, err)
	require.Len(t, messages, 2)
	imageCount := 0
	for _, part := range messages[1].ContentParts {
		if part.Type == "image_url" {
			imageCount++
		}
	}
	require.Equal(t, 2, imageCount)

	stored := createV2TestSession(t, -941004, ProviderGemini, ModelGeminiFlash)
	runContents := []q.GeminiContent{
		v2TestUserContent(stored.ID, -941004, 21, "first album item"),
		v2TestUserContent(stored.ID, -941004, 22, "last album item"),
	}
	runSession := &GeminiSession{GeminiSession: stored, Provider: ProviderGemini,
		Model: ModelGeminiFlash, TmpContents: runContents}
	run, err := runSession.GetOrBeginAIRun(context.Background(), -941004, 21)
	require.NoError(t, err)
	require.Equal(t, int64(21), run.RequestMsgID)
}

func TestExpiredAndMissingAlbumRepliesDoNotFallBackToOnePhoto(t *testing.T) {
	chatID := int64(-941005)
	putTestAlbum(t, chatID, "expired-album", time.Now().Unix(), 1,
		aiq.UpsertAIMediaGroupPhotoParams{ChatID: chatID, MediaGroupID: "expired-album",
			MsgID: 31, SentAt: 31, UserID: 7, Username: "Alice", TelegramFileID: "unused"})
	for _, groupID := range []string{"expired-album", "missing-album"} {
		replied := &gotgbot.Message{MessageId: 31, Chat: gotgbot.Chat{Id: chatID}, MediaGroupId: groupID,
			From: &gotgbot.User{Id: 7}, Photo: []gotgbot.PhotoSize{{FileId: "unused"}}}
		current := &gotgbot.Message{MessageId: 32, Chat: gotgbot.Chat{Id: chatID},
			From: &gotgbot.User{Id: 8}, Text: "@testbot inspect", ReplyToMessage: replied}
		session := &GeminiSession{GeminiSession: q.GeminiSession{ID: 941005}}
		err := session.AddTgMessageWithReply(context.Background(), nil, current)
		require.True(t, errors.Is(err, ErrAIMediaGroupUnavailable), groupID)
		require.Empty(t, session.TmpContents)
	}
}

func TestAlbumDownloadFailureDoesNotAppendPartialGroup(t *testing.T) {
	chatID := int64(-941006)
	dir := t.TempDir()
	goodPath := dir + "/good.jpg"
	require.NoError(t, os.WriteFile(goodPath, []byte("good"), 0o600))
	putTestAlbum(t, chatID, "broken-album", time.Now().Add(time.Hour).Unix(), 1,
		aiq.UpsertAIMediaGroupPhotoParams{ChatID: chatID, MediaGroupID: "broken-album",
			MsgID: 41, SentAt: 41, UserID: 7, Username: "Alice", TelegramFileID: "good-file"},
		aiq.UpsertAIMediaGroupPhotoParams{ChatID: chatID, MediaGroupID: "broken-album",
			MsgID: 42, SentAt: 42, UserID: 7, Username: "Alice", TelegramFileID: "missing-file"})
	bot := &gotgbot.Bot{BotClient: localMediaBotClient{paths: map[string]string{
		"good-file": goodPath, "missing-file": dir + "/missing.jpg",
	}}}
	group, err := g.AIQ.GetAIMediaGroup(context.Background(), chatID, "broken-album")
	require.NoError(t, err)
	session := &GeminiSession{GeminiSession: q.GeminiSession{ID: 941006}, Provider: ProviderGemini}
	err = session.addAIMediaGroup(context.Background(), bot, group, false)
	require.ErrorIs(t, err, ErrAIMediaGroupDownload)
	require.Empty(t, session.TmpContents)
}
