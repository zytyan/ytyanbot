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
	"main/globalcfg/h"
	"main/globalcfg/q"
	"main/helpers/ent2md"
	"strings"
	"sync"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"google.golang.org/genai"
)

type GeminiSession struct {
	q.GeminiSession
	mu                sync.Mutex
	Contents          []q.GeminiContent
	TmpContents       []q.GeminiContent
	UpdateTime        time.Time
	Memories          []q.GeminiMemory
	AssistantPayloads map[int64]g.AIAssistantPayload
	PendingResponses  map[int64]pendingAssistantResponse

	AllowCodeExecution bool
	Provider           string
	Model              string
}

type pendingAssistantResponse struct {
	ChatID        int64
	Provider      string
	Model         string
	Usage         AIUsage
	Payload       []byte
	PayloadFormat string
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
	contents := make([]*genai.Content, 0, len(s.Contents)+len(s.TmpContents))
	for i := range s.Contents {
		contents = append(contents, databaseContentToGenaiPart(&s.Contents[i], s.AssistantPayloads))
	}
	for i := range s.TmpContents {
		contents = append(contents, databaseContentToGenaiPart(&s.TmpContents[i], s.AssistantPayloads))
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
	if len(s.TmpContents) > 0 {
		last := s.TmpContents[len(s.TmpContents)-1]
		if last.MsgType == "video" && !last.Text.Valid {
			return nil, ErrDeepSeekVideoOnly
		}
	}
	messages := []deepSeekMessage{{Role: "system", Content: systemPrompt}}
	for i := range s.Contents {
		if message, ok := deepSeekContent(&s.Contents[i], s.AssistantPayloads); ok {
			messages = append(messages, message)
		}
	}
	for i := range s.TmpContents {
		if message, ok := deepSeekContent(&s.TmpContents[i], s.AssistantPayloads); ok {
			messages = append(messages, message)
		}
	}
	return messages, nil
}

func (s *GeminiSession) AddTgMessage(bot *gotgbot.Bot, msg *gotgbot.Message) (err error) {
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

func (s *GeminiSession) AddModelMessage(msg *gotgbot.Message, result *AIResult) {
	content := q.GeminiContent{
		SessionID: s.ID,
		ChatID:    msg.Chat.Id,
		MsgID:     msg.MessageId,
		Role:      genai.RoleModel,
		SentTime:  q.UnixTime{Time: time.Unix(msg.Date, 0)},
		Username:  mainBot.FirstName,
		MsgType:   "text",
		Text:      sql.NullString{String: result.DisplayText, Valid: result.DisplayText != ""},
		UserID:    mainBot.Id,
	}
	if mainBot.Username != "" {
		content.AtableUsername = sql.NullString{String: mainBot.Username, Valid: true}
	}
	if result.ThoughtSignature != "" {
		content.ThoughtSignature = sql.NullString{String: result.ThoughtSignature, Valid: true}
	}
	s.TmpContents = append(s.TmpContents, content)
	if s.PendingResponses == nil {
		s.PendingResponses = make(map[int64]pendingAssistantResponse)
	}
	s.PendingResponses[msg.MessageId] = pendingAssistantResponse{
		ChatID: msg.Chat.Id, Provider: s.Provider, Model: s.Model, Usage: result.Usage,
		Payload: result.AssistantPayload, PayloadFormat: result.AssistantPayloadFormat,
	}
}

func (s *GeminiSession) loadContentFromDatabase(ctx context.Context) error {
	content, err := g.Q.GetAllMsgInSession(ctx, s.ID, geminiSessionContentLimit)
	if err != nil {
		return err
	}
	s.AllowCodeExecution = true
	for _, c := range content {
		if c.MimeType.Valid && strings.Contains(c.MimeType.String, "video") {
			s.AllowCodeExecution = false
		}
	}
	s.Contents = content
	s.AssistantPayloads, err = g.GetAISessionAssistantPayloads(ctx, s.ID)
	if err != nil {
		return err
	}
	return nil
}

func (s *GeminiSession) loadModel(ctx context.Context, fallback string) error {
	provider, model, err := g.GetAISessionModel(ctx, s.ID)
	if errors.Is(err, sql.ErrNoRows) {
		model = fallback
		provider = providerForModel(model)
		err = g.SetAISessionModel(ctx, s.ID, provider, model)
	}
	if err != nil {
		return err
	}
	s.Provider, s.Model = provider, model
	return nil
}

func (s *GeminiSession) PersistTmpUpdates(ctx context.Context) error {
	if len(s.TmpContents) == 0 {
		return nil
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
	newQ := g.Q.WithTx(tx)
	for i := range s.TmpContents {
		err = s.TmpContents[i].Save(ctx, newQ)
		if err != nil {
			return err
		}
	}
	for msgID, response := range s.PendingResponses {
		err = g.UpsertAIMessageResponse(ctx, tx, s.ID, msgID, response.ChatID, g.AIMessageUsage{
			Provider: response.Provider, Model: response.Model,
			InputTokens: response.Usage.InputTokens, OutputTokens: response.Usage.OutputTokens,
			CachedInputTokens: response.Usage.CachedInputTokens,
		}, response.PayloadFormat, response.Payload)
		if err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	s.Contents = append(s.Contents, s.TmpContents...)
	if s.AssistantPayloads == nil {
		s.AssistantPayloads = make(map[int64]g.AIAssistantPayload)
	}
	for msgID, response := range s.PendingResponses {
		s.AssistantPayloads[msgID] = g.AIAssistantPayload{
			MsgID: msgID, Provider: response.Provider, Format: response.PayloadFormat,
			Payload: append([]byte(nil), response.Payload...),
		}
	}
	s.TmpContents = nil
	s.PendingResponses = nil
	s.UpdateTime = time.Now()
	return nil
}

func (s *GeminiSession) DiscardTmpUpdates() {
	s.TmpContents = nil
	s.PendingResponses = nil
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
			sessionId, err = g.Q.GetSessionIdByMessage(ctx, msg.Chat.Id, msg.ReplyToMessage.MessageId)
		} else {
			sessionId = mentionSessionId
		}
		if err == nil {
			if sess, ok := geminiSessions.sidToSess[sessionId]; ok {
				return sess
			}
		}

		session.GeminiSession, err = g.Q.GetSessionById(ctx, sessionId)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				goto create
			}
			return nil
		}
		err = session.loadContentFromDatabase(ctx)
		if err != nil {
			return nil
		}
		if err = session.loadModel(ctx, defaultAIModel); err != nil {
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
	var err error
	session.GeminiSession, err = g.Q.CreateNewGeminiSession(ctx, msg.Chat.Id, getChatName(msg.Chat), msg.Chat.Type)
	if err != nil {
		return nil
	}
	err = session.loadContentFromDatabase(ctx)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	model, err := g.GetAIChatModel(ctx, topic.chatId, defaultAIModel)
	if err != nil {
		return nil
	}
	if err = session.loadModel(ctx, model); err != nil {
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
