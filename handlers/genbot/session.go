package genbot

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
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
	mu          sync.Mutex
	Contents    []q.GeminiContent
	TmpContents []q.GeminiContent
	UpdateTime  time.Time
	Memories    []q.GeminiMemory

	AllowCodeExecution bool
	Provider           string
	Model              string
}

var geminiSessions = struct {
	mu sync.RWMutex
	// session id -> session ，这是一个缓存
	sidToSess    map[int64]*GeminiSession
	chatIdToSess map[geminiTopic]*GeminiSession
}{
	sidToSess:    map[int64]*GeminiSession{},
	chatIdToSess: map[geminiTopic]*GeminiSession{},
}

func databaseContentToGenaiPart(content *q.GeminiContent) (out *genai.Content) {
	out = &genai.Content{}
	label := fmt.Sprintf(`-start-label-
id:%d
time:%s
name:%s
type:%s
`, content.MsgID, content.SentTime.Format("2006-01-02 15:04:05"),
		content.Username,
		content.MsgType)
	if content.ReplyToMsgID.Valid {
		label += fmt.Sprintf("reply:%d\n", content.ReplyToMsgID.Int64)
	}
	if content.QuotePart.Valid {
		label += fmt.Sprintf("quote:%s\n", content.QuotePart.String)
	}
	label += "-end-label-\n"
	out.Role = content.Role
	textPart := &genai.Part{
		Text: label,
	}
	out.Parts = append(out.Parts, textPart)
	if content.Text.Valid {
		part := &genai.Part{Text: content.Text.String}
		if content.ThoughtSignature.Valid {
			part.ThoughtSignature, _ = base64.StdEncoding.DecodeString(content.ThoughtSignature.String)
		}
		out.Parts = append(out.Parts, part)
	} else if content.ThoughtSignature.Valid {
		textPart.ThoughtSignature, _ = base64.StdEncoding.DecodeString(content.ThoughtSignature.String)
	}
	if len(content.Blob) > 0 && content.MimeType.Valid {
		out.Parts = append(out.Parts, &genai.Part{InlineData: &genai.Blob{
			Data:     content.Blob,
			MIMEType: content.MimeType.String,
		}})
	}
	return
}

func (s *GeminiSession) ToGenaiContents(turnContext string) []*genai.Content {
	contents := make([]*genai.Content, 0, len(s.Contents)+len(s.TmpContents)+1)
	for i := range s.Contents {
		contents = append(contents, databaseContentToGenaiPart(&s.Contents[i]))
	}
	if turnContext != "" {
		contents = append(contents, genai.NewContentFromText(turnContext, genai.RoleUser))
	}
	for i := range s.TmpContents {
		contents = append(contents, databaseContentToGenaiPart(&s.TmpContents[i]))
	}
	return contents
}

func deepSeekContent(content *q.GeminiContent) (deepSeekMessage, bool) {
	role := "user"
	if content.Role == genai.RoleModel {
		role = "assistant"
	}
	if content.MsgType == "video" && !content.Text.Valid {
		return deepSeekMessage{}, false
	}
	var text strings.Builder
	_, _ = fmt.Fprintf(&text, "-start-label-\nid:%d\ntime:%s\nname:%s\ntype:%s\n-end-label-\n",
		content.MsgID, content.SentTime.Format("2006-01-02 15:04:05"), content.Username, content.MsgType)
	if content.Text.Valid {
		text.WriteString(content.Text.String)
	}
	if content.MsgType == "photo" {
		text.WriteString("\n[图片]")
	} else if content.MsgType == "sticker" && !content.Text.Valid {
		text.WriteString("[贴纸]")
	}
	return deepSeekMessage{Role: role, Content: text.String()}, true
}

func (s *GeminiSession) ToDeepSeekMessages(systemPrompt, turnContext string) ([]deepSeekMessage, error) {
	if len(s.TmpContents) > 0 {
		last := s.TmpContents[len(s.TmpContents)-1]
		if last.MsgType == "video" && !last.Text.Valid {
			return nil, ErrDeepSeekVideoOnly
		}
	}
	messages := []deepSeekMessage{{Role: "system", Content: systemPrompt}}
	for i := range s.Contents {
		if message, ok := deepSeekContent(&s.Contents[i]); ok {
			messages = append(messages, message)
		}
	}
	if turnContext != "" {
		messages = append(messages, deepSeekMessage{Role: "user", Content: turnContext})
	}
	for i := range s.TmpContents {
		if message, ok := deepSeekContent(&s.TmpContents[i]); ok {
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
	if msg.ReplyToMessage != nil {
		content.ReplyToMsgID.Valid = true
		content.ReplyToMsgID.Int64 = msg.ReplyToMessage.MessageId
		if msg.Quote != nil && msg.Quote.IsManual {
			content.QuotePart = sql.NullString{String: msg.Quote.Text, Valid: true}
		}
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

func (s *GeminiSession) AddModelMessage(msg *gotgbot.Message, text, thoughtSignature string) {
	content := q.GeminiContent{
		SessionID: s.ID,
		ChatID:    msg.Chat.Id,
		MsgID:     msg.MessageId,
		Role:      genai.RoleModel,
		SentTime:  q.UnixTime{Time: time.Unix(msg.Date, 0)},
		Username:  mainBot.FirstName,
		MsgType:   "text",
		Text:      sql.NullString{String: text, Valid: text != ""},
		UserID:    mainBot.Id,
	}
	if mainBot.Username != "" {
		content.AtableUsername = sql.NullString{String: mainBot.Username, Valid: true}
	}
	if thoughtSignature != "" {
		content.ThoughtSignature = sql.NullString{String: thoughtSignature, Valid: true}
	}
	s.TmpContents = append(s.TmpContents, content)
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
	s.Contents = append(s.Contents, s.TmpContents...)
	s.TmpContents = nil
	s.UpdateTime = time.Now()
	return tx.Commit()
}

func (s *GeminiSession) DiscardTmpUpdates() {
	s.TmpContents = nil
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
