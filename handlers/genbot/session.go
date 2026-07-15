package genbot

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	g "main/globalcfg"
	"main/globalcfg/aiq"
	"main/globalcfg/h"
	"main/globalcfg/q"
	genai "main/handlers/genbot/geminiapi"
	"main/helpers/ent2md"
	"strings"
	"sync"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
)

type GeminiSession struct {
	q.GeminiSession
	mu                   sync.Mutex
	Contents             []q.GeminiContent
	TmpContents          []q.GeminiContent
	TmpContextOnlyMsgIDs map[int64]struct{}
	TmpPersisted         bool
	UpdateTime           time.Time
	AssistantPayloads    map[int64]g.AIAssistantPayload
	GeminiInteractionID  string
	WindowStartMsgID     int64
	HistoryRebuildLossy  bool
	GeminiCache          geminiExplicitCacheState

	AllowCodeExecution bool
	Provider           string
	Model              string
}

type aiRequestWindow struct {
	Contents       []q.GeminiContent
	Drop           int
	StartMsgID     int64
	RebuildHistory bool
}

func (s *GeminiSession) prepareRequestWindow() aiRequestWindow {
	contents := make([]q.GeminiContent, 0, len(s.Contents)+len(s.TmpContents))
	contents = append(contents, s.Contents...)
	contents = append(contents, s.TmpContents...)
	drop := 0
	for len(contents)-drop > geminiSessionContentLimit {
		drop += geminiSessionWindowStep
	}
	if drop > len(contents) {
		drop = len(contents)
	}
	contents = contents[drop:]
	window := aiRequestWindow{Contents: contents, Drop: drop, RebuildHistory: drop > 0}
	if len(contents) > 0 {
		window.StartMsgID = contents[0].MsgID
	}
	return window
}

var shanghaiLocation = time.FixedZone("Asia/Shanghai", 8*60*60)

var geminiSessions = struct {
	mu sync.RWMutex
	// session id -> session ，这是一个缓存
	sidToSess    map[int64]*GeminiSession
	chatIdToSess map[geminiTopic]*GeminiSession
}{
	sidToSess:    map[int64]*GeminiSession{},
	chatIdToSess: map[geminiTopic]*GeminiSession{},
}

func compactUserText(content *q.GeminiContent) string {
	header := fmt.Sprintf("[ %s %s ]\n", content.Username,
		content.SentTime.In(shanghaiLocation).Format("2006-01-02 15:04:05"))
	if content.Text.Valid {
		return header + content.Text.String
	}
	return header
}

func warnInvalidAssistantPayload(provider string, msgID int64, err error) {
	logger := log
	if logger == nil {
		logger = slog.Default()
	}
	logger.Warn("invalid assistant payload; falling back to saved text",
		"provider", provider, "msg_id", msgID, "err", err)
}

func fallbackGeminiAssistant(content *q.GeminiContent) *genai.Content {
	part := &genai.Part{}
	if content.Text.Valid {
		part.Text = content.Text.String
	}
	if content.ThoughtSignature.Valid {
		part.ThoughtSignature, _ = base64.StdEncoding.DecodeString(content.ThoughtSignature.String)
	}
	return &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{part}}
}

func databaseContentToGenaiPart(content *q.GeminiContent, payloads map[int64]g.AIAssistantPayload) (out *genai.Content) {
	if content.Role == genai.RoleModel {
		if payload, ok := payloads[content.MsgID]; ok && payload.Provider == ProviderGemini && payload.Format == PayloadFormatGeminiContent {
			var saved genai.Content
			if err := json.Unmarshal(payload.Payload, &saved); err == nil {
				return &saved
			} else {
				warnInvalidAssistantPayload(ProviderGemini, content.MsgID, err)
			}
		}
		return fallbackGeminiAssistant(content)
	}
	out = &genai.Content{Role: content.Role, Parts: []*genai.Part{{Text: compactUserText(content)}}}
	if len(content.Blob) > 0 && content.MimeType.Valid {
		out.Parts = append(out.Parts, &genai.Part{InlineData: &genai.Blob{
			Data:     content.Blob,
			MIMEType: content.MimeType.String,
		}})
	}
	return
}

func (s *GeminiSession) ToGenaiContents() []*genai.Content {
	databaseContents := make([]q.GeminiContent, 0, len(s.Contents)+len(s.TmpContents))
	databaseContents = append(databaseContents, s.Contents...)
	databaseContents = append(databaseContents, s.TmpContents...)
	return genaiContentsFromDatabase(databaseContents, s.AssistantPayloads)
}

func genaiContentsFromDatabase(databaseContents []q.GeminiContent,
	payloads map[int64]g.AIAssistantPayload,
) []*genai.Content {
	contents := make([]*genai.Content, 0, len(databaseContents))
	for i := range databaseContents {
		contents = append(contents, databaseContentToGenaiPart(&databaseContents[i], payloads))
	}
	return contents
}

func deepSeekContent(content *q.GeminiContent, payloads map[int64]g.AIAssistantPayload) (deepSeekMessage, bool) {
	if content.Role == genai.RoleModel {
		if payload, ok := payloads[content.MsgID]; ok && payload.Provider == ProviderDeepSeek && payload.Format == PayloadFormatDeepSeekMessage {
			var saved deepSeekMessage
			if err := json.Unmarshal(payload.Payload, &saved); err == nil && saved.Role == "assistant" {
				return saved, true
			} else {
				if err == nil {
					err = fmt.Errorf("unexpected role %q", saved.Role)
				}
				warnInvalidAssistantPayload(ProviderDeepSeek, content.MsgID, err)
			}
		}
		message := deepSeekMessage{Role: "assistant"}
		if content.Text.Valid {
			message.Content = content.Text.String
		}
		return message, true
	}
	if content.MsgType == "video" && !content.Text.Valid {
		return deepSeekMessage{}, false
	}
	text := strings.Builder{}
	text.WriteString(compactUserText(content))
	if content.MsgType == "photo" {
		if content.Text.Valid && content.Text.String != "" {
			text.WriteByte('\n')
		}
		text.WriteString("[图片]")
	} else if content.MsgType == "sticker" && !content.Text.Valid {
		text.WriteString("[贴纸]")
	}
	return deepSeekMessage{Role: "user", Content: text.String()}, true
}

func (s *GeminiSession) ToDeepSeekMessages(systemPrompt string) ([]deepSeekMessage, error) {
	allContents := make([]q.GeminiContent, 0, len(s.Contents)+len(s.TmpContents))
	allContents = append(allContents, s.Contents...)
	allContents = append(allContents, s.TmpContents...)
	if len(allContents) > 0 {
		last := allContents[len(allContents)-1]
		if last.MsgType == "video" && !last.Text.Valid {
			return nil, ErrDeepSeekVideoOnly
		}
	}
	messages := []deepSeekMessage{{Role: "system", Content: systemPrompt}}
	payloads := s.AssistantPayloads
	if s.HistoryRebuildLossy {
		// Payloads can contain provider-specific encrypted reasoning or tool
		// calls. A model-switched session reuses only the portable saved text.
		payloads = nil
	}
	for i := range allContents {
		if message, ok := deepSeekContent(&allContents[i], payloads); ok {
			messages = append(messages, message)
		}
	}
	return messages, nil
}

func (s *GeminiSession) AddTgMessage(bot *gotgbot.Bot, msg *gotgbot.Message) (err error) {
	return s.addTgMessage(bot, msg, "")
}

func (s *GeminiSession) addTgMessage(bot *gotgbot.Bot, msg *gotgbot.Message, roleOverride string) (err error) {
	if msg == nil {
		return nil
	}
	for i := range s.Contents {
		if msg.MessageId == s.Contents[i].MsgID {
			return nil
		}
	}
	for i := range s.TmpContents {
		if msg.MessageId == s.TmpContents[i].MsgID {
			return nil
		}
	}
	role := genai.RoleUser
	if msg.GetSender().Id() == mainBot.Id {
		role = genai.RoleModel
	}
	if roleOverride != "" {
		role = roleOverride
	}
	username := msg.GetSender().Username()
	content := q.GeminiContent{
		SessionID:      s.ID,
		ChatID:         msg.Chat.Id,
		MsgID:          msg.MessageId,
		Role:           role,
		SentTime:       q.UnixTime{Time: time.Unix(msg.Date, 0)},
		Username:       msg.GetSender().Name(),
		AtableUsername: sql.NullString{String: username, Valid: username != ""},
		UserID:         msg.GetSender().Id(),
	}
	mdTxt := ent2md.TgMsgTextToMarkdown(msg)
	if mdTxt != "" {
		content.Text.Valid = true
		content.Text.String = mdTxt
		content.MsgType = "text"
	}
	var data []byte
	if msg.Photo != nil {
		data, err = h.DownloadToMemoryCached(bot, msg.Photo[len(msg.Photo)-1].FileId)
		if err != nil {
			return err
		}
		content.MsgType = "photo"
		content.Blob = data
		content.MimeType.Valid = true
		content.MimeType.String = "image/jpeg"
	} else if msg.Sticker != nil {
		content.MsgType = "sticker"
		content.Text = sql.NullString{String: msg.Sticker.Emoji, Valid: msg.Sticker.Emoji != ""}
		if !content.Text.Valid {
			content.Text = sql.NullString{String: "[贴纸]", Valid: true}
		}
		if !msg.Sticker.IsAnimated && s.Provider != ProviderDeepSeek {
			data, err = h.DownloadToMemoryCached(bot, msg.Sticker.FileId)
			if err != nil {
				return err
			}
			content.Blob = data
			content.MimeType.Valid = true
			if msg.Sticker.IsVideo {
				s.AllowCodeExecution = false
				content.MimeType.String = "video/webm"
			} else {
				content.MimeType.String = "image/webp"
			}
		}
	} else if msg.Video != nil {
		content.MsgType = "video"
		if s.Provider == ProviderDeepSeek {
			s.TmpContents = append(s.TmpContents, content)
			return nil
		}
		if msg.Video.Duration <= 240 && msg.Video.FileSize <= 15*1024*1024 {
			s.AllowCodeExecution = false
			data, err = h.DownloadToMemoryCached(bot, msg.Video.FileId)
			if err != nil {
				return err
			}
			content.Blob = data
			content.MimeType.Valid = true
			content.MimeType.String = "video/mp4"
		} else {
			content.Text.Valid = true
			content.Text.String += "(用户发送了一个视频，但由于不满足 size<15MB且时长<=240s，无法上传)"
		}
	} else if msg.Animation != nil {
		content.MsgType = "video"
		if s.Provider == ProviderDeepSeek {
			s.TmpContents = append(s.TmpContents, content)
			return nil
		}
		s.AllowCodeExecution = false
		data, err = h.DownloadToMemoryCached(bot, msg.Animation.FileId)
		if err != nil {
			return err
		}
		content.Blob = data
		content.MimeType.Valid = true
		content.MimeType.String = "video/mp4"
	}
	s.TmpContents = append(s.TmpContents, content)
	return
}

func (s *GeminiSession) containsMessage(msgID int64) bool {
	for i := range s.Contents {
		if s.Contents[i].MsgID == msgID {
			return true
		}
	}
	for i := range s.TmpContents {
		if s.TmpContents[i].MsgID == msgID {
			return true
		}
	}
	return false
}

func (s *GeminiSession) setMessageRole(msgID int64, role string) {
	for i := range s.Contents {
		if s.Contents[i].MsgID == msgID {
			s.Contents[i].Role = role
		}
	}
	for i := range s.TmpContents {
		if s.TmpContents[i].MsgID == msgID {
			s.TmpContents[i].Role = role
		}
	}
}

// AddTgMessageWithReply appends a directly replied-to message when it is not
// already part of the active chain, then appends the current user message.
func (s *GeminiSession) AddTgMessageWithReply(ctx context.Context, bot *gotgbot.Bot, msg *gotgbot.Message) error {
	return s.AddTgMessageWithReplyMode(ctx, bot, msg, false)
}

// AddTgMessageWithReplyMode behaves like AddTgMessageWithReply. When
// replyContextOnly is true, the replied-to message is included in this request
// but is never assigned to the new session or otherwise persisted again.
func (s *GeminiSession) AddTgMessageWithReplyMode(ctx context.Context, bot *gotgbot.Bot, msg *gotgbot.Message, replyContextOnly bool) error {
	if msg == nil {
		return nil
	}
	if replied := msg.ReplyToMessage; replied != nil && s.containsMessage(replied.MessageId) &&
		replied.GetSender().Id() == mainBot.Id {
		isAIResponse, err := g.HasAIMessageResponse(ctx, msg.Chat.Id, replied.MessageId)
		if err != nil {
			return err
		}
		if !isAIResponse {
			if err = g.MarkMessageAsUserInput(ctx, msg.Chat.Id, replied.MessageId); err != nil {
				return err
			}
			s.setMessageRole(replied.MessageId, genai.RoleUser)
		}
	}
	if replied := msg.ReplyToMessage; replied != nil && !s.containsMessage(replied.MessageId) {
		// A stored message may have fallen out of the active sliding window. It is
		// The Telegram message body is global while session membership is
		// many-to-many. @new marks the replied message as request-only context so
		// it does not become stable history in the new branch.
		contextOnly := replyContextOnly
		if !contextOnly {
			_, lookupErr := g.AIQ.GetAISessionIDByMessage(ctx, msg.Chat.Id, replied.MessageId)
			contextOnly = lookupErr == nil
			if lookupErr != nil && !errors.Is(lookupErr, sql.ErrNoRows) {
				return lookupErr
			}
		}
		isAIResponse := false
		if contextOnly {
			var lookupErr error
			isAIResponse, lookupErr = g.HasAIMessageResponse(ctx, msg.Chat.Id, replied.MessageId)
			if lookupErr != nil {
				return lookupErr
			}
			if !isAIResponse {
				if lookupErr = g.MarkMessageAsUserInput(ctx, msg.Chat.Id, replied.MessageId); lookupErr != nil {
					return lookupErr
				}
			}
		}
		repliedCopy := *replied
		if repliedCopy.Chat.Id == 0 {
			repliedCopy.Chat = msg.Chat
		}
		before := len(s.TmpContents)
		roleOverride := ""
		if !isAIResponse {
			// A Bot-authored message that was never stored as an AI response is
			// external reference material, not assistant history. Treat the whole
			// text/media message as user input so provider adapters retain its parts.
			roleOverride = genai.RoleUser
		}
		if err := s.addTgMessage(bot, &repliedCopy, roleOverride); err != nil {
			return err
		}
		if len(s.TmpContents) > before {
			added := &s.TmpContents[len(s.TmpContents)-1]
			// DeepSeek deliberately skips a captionless video/animation, leaving
			// no database-storable body. Keep it in this request's ordered input
			// without attempting an invalid INSERT after the model replies.
			contextOnly = contextOnly || (!added.Text.Valid && len(added.Blob) == 0)
		}
		if contextOnly && len(s.TmpContents) > before {
			if s.TmpContextOnlyMsgIDs == nil {
				s.TmpContextOnlyMsgIDs = make(map[int64]struct{})
			}
			s.TmpContextOnlyMsgIDs[replied.MessageId] = struct{}{}
		}
	}
	return s.AddTgMessage(bot, msg)
}

func (s *GeminiSession) loadContentFromDatabase(ctx context.Context) error {
	rows, err := g.AIQ.ListAISessionMessages(ctx, s.ID)
	if err != nil {
		return err
	}
	start := 0
	if len(rows) > geminiSessionContentLimit {
		start = len(rows) - geminiSessionContentLimit
	}
	s.Contents = make([]q.GeminiContent, 0, len(rows)-start)
	for _, row := range rows[start:] {
		content := q.GeminiContent{
			SessionID: row.SessionID, ChatID: row.ChatID, MsgID: row.MsgID, Role: row.Role,
			SentTime: q.UnixTime{Time: time.Unix(row.SentAt, 0)}, Username: row.Username,
			MsgType: row.MsgType, ReplyToMsgID: row.ReplyToMsgID, Text: row.Text,
			QuotePart: row.QuotePart, AtableUsername: row.AtableUsername, UserID: row.UserID,
		}
		if err = loadAIContentMedia(ctx, &content); err != nil {
			return err
		}
		s.Contents = append(s.Contents, content)
	}
	s.AssistantPayloads, err = g.GetAISessionAssistantPayloads(ctx, s.ID)
	if err != nil {
		return err
	}
	return nil
}

func (s *GeminiSession) refreshCapabilities() {
	s.AllowCodeExecution = true
	for _, c := range s.Contents {
		if c.MimeType.Valid && strings.Contains(c.MimeType.String, "video") {
			s.AllowCodeExecution = false
			return
		}
	}
}

func (s *GeminiSession) loadModel(ctx context.Context) error {
	provider, model, err := g.GetAISessionModel(ctx, s.ID)
	if err != nil {
		return err
	}
	s.Provider, s.Model = provider, model
	state, err := g.GetAISessionRuntimeState(ctx, s.ID)
	if err != nil {
		return err
	}
	s.GeminiInteractionID = state.GeminiInteractionID
	s.WindowStartMsgID = state.WindowStartMsgID
	s.HistoryRebuildLossy = state.HistoryRebuildLossy
	s.GeminiCache = geminiExplicitCacheState{
		Name: state.GeminiCacheName, ExpireTime: time.Unix(state.GeminiCacheExpireTime, 0),
		StartMsgID: state.GeminiCacheStartMsgID, EndMsgID: state.GeminiCacheEndMsgID,
		TokenCount: state.GeminiCacheTokenCount, Fingerprint: state.GeminiCacheFingerprint,
	}
	if state.GeminiCacheExpireTime == 0 {
		s.GeminiCache.ExpireTime = time.Time{}
	}
	if s.WindowStartMsgID != 0 {
		found := false
		for i := range s.Contents {
			if s.Contents[i].MsgID == s.WindowStartMsgID {
				s.Contents = s.Contents[i:]
				found = true
				break
			}
		}
		if !found && len(s.Contents) > 0 {
			// The persisted remote chain can no longer be reconstructed safely.
			s.GeminiInteractionID = ""
			s.WindowStartMsgID = 0
		}
	}
	s.refreshCapabilities()
	return nil
}

func (s *GeminiSession) DiscardTmpUpdates() {
	if s.TmpPersisted {
		for _, item := range s.TmpContents {
			if _, contextOnly := s.TmpContextOnlyMsgIDs[item.MsgID]; contextOnly {
				continue
			}
			found := false
			for i := range s.Contents {
				if s.Contents[i].ChatID == item.ChatID && s.Contents[i].MsgID == item.MsgID {
					found = true
					break
				}
			}
			if !found {
				s.Contents = append(s.Contents, item)
			}
		}
	}
	s.TmpContents = nil
	s.TmpContextOnlyMsgIDs = nil
	s.TmpPersisted = false
}

func GeminiGetSession(ctx context.Context, msg *gotgbot.Message, createNewSession bool, ignoreSessionTimeout bool, mentionSessionId int64) *GeminiSession {
	geminiSessions.mu.Lock()
	defer geminiSessions.mu.Unlock()
	session := &GeminiSession{}
	topic := newTopic(msg)
	if (mentionSessionId != 0 || msg.ReplyToMessage != nil) && !createNewSession {
		var sessionId int64
		var err error
		if mentionSessionId == 0 {
			sessionId, err = g.AIQ.GetAISessionIDByMessage(ctx, msg.Chat.Id, msg.ReplyToMessage.MessageId)
		} else {
			sessionId = mentionSessionId
		}
		if err == nil {
			if sess, ok := geminiSessions.sidToSess[sessionId]; ok {
				return sess
			}
		}

		storedSession, lookupErr := g.AIQ.GetAISession(ctx, sessionId)
		err = lookupErr
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				goto create
			}
			return nil
		}
		session.GeminiSession = sessionFromV2(storedSession)
		session.UpdateTime = time.Unix(storedSession.UpdatedAt, 0)
		err = session.loadContentFromDatabase(ctx)
		if err != nil {
			return nil
		}
		if err = session.loadModel(ctx); err != nil {
			return nil
		}
		geminiSessions.sidToSess[sessionId] = session
		geminiSessions.chatIdToSess[topic] = session
		return session
	}
create:
	sess, ok := geminiSessions.chatIdToSess[topic]
	if ok && !createNewSession {
		if ignoreSessionTimeout {
			return sess
		}
		delete(geminiSessions.sidToSess, sess.ID)
	}
	delete(geminiSessions.chatIdToSess, topic)
	provider, model, err := g.GetAIChatSelection(ctx, topic.chatId,
		providerForModel(defaultAIModel), defaultAIModel)
	if err != nil {
		return nil
	}
	now := time.Now().Unix()
	storedSession, err := g.AIQ.CreateAISession(ctx, aiq.CreateAISessionParams{
		ChatID: msg.Chat.Id, TopicID: sql.NullInt64{Int64: topic.topicId, Valid: topic.topicId != 0},
		ChatName: getChatName(msg.Chat), ChatType: msg.Chat.Type, Provider: provider, Model: model,
		CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		return nil
	}
	session.GeminiSession = sessionFromV2(storedSession)
	session.UpdateTime = time.Unix(storedSession.UpdatedAt, 0)
	err = session.loadContentFromDatabase(ctx)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err = session.loadModel(ctx); err != nil {
		return nil
	}
	geminiSessions.sidToSess[session.ID] = session
	geminiSessions.chatIdToSess[topic] = session
	return session
}

func invalidateChatSessions(chatID int64) {
	geminiSessions.mu.Lock()
	defer geminiSessions.mu.Unlock()
	for topic, session := range geminiSessions.chatIdToSess {
		if topic.chatId == chatID {
			delete(geminiSessions.chatIdToSess, topic)
			delete(geminiSessions.sidToSess, session.ID)
		}
	}
}

func invalidateSession(sessionID int64) {
	geminiSessions.mu.Lock()
	defer geminiSessions.mu.Unlock()
	delete(geminiSessions.sidToSess, sessionID)
	for topic, session := range geminiSessions.chatIdToSess {
		if session.ID == sessionID {
			delete(geminiSessions.chatIdToSess, topic)
		}
	}
}
