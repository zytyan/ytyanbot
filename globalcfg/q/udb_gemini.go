package q

import (
	"context"
	"slices"
)

func (q *Queries) GetAllMsgInSession(ctx context.Context, sessionID int64, limit int64) ([]GeminiContent, error) {
	contents, err := q.getAllMsgInSessionReversed(ctx, sessionID, limit)
	slices.Reverse(contents)
	return contents, err
}
func (g *GeminiContent) Save(ctx context.Context, q *Queries) error {
	return q.AddGeminiMessage(ctx, AddGeminiMessageParams{
		SessionID:        g.SessionID,
		ChatID:           g.ChatID,
		MsgID:            g.MsgID,
		Role:             g.Role,
		SentTime:         g.SentTime,
		Username:         g.Username,
		MsgType:          g.MsgType,
		ReplyToMsgID:     g.ReplyToMsgID,
		Text:             g.Text,
		Blob:             g.Blob,
		MimeType:         g.MimeType,
		QuotePart:        g.QuotePart,
		ThoughtSignature: g.ThoughtSignature,
		AtableUsername:   g.AtableUsername,
		UserID:           g.UserID,
	})
}
