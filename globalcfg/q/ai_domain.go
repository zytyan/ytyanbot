package q

import "database/sql"

// GeminiContent is an in-memory provider-neutral message record retained by
// the model adapters. Persistence is implemented by the dedicated aiq sqlc
// package; this type contains no database behavior.
type GeminiContent struct {
	SessionID        int64
	ChatID           int64
	MsgID            int64
	Role             string
	SentTime         UnixTime
	Username         string
	MsgType          string
	ReplyToMsgID     sql.NullInt64
	Text             sql.NullString
	Blob             []byte
	MimeType         sql.NullString
	QuotePart        sql.NullString
	ThoughtSignature sql.NullString
	AtableUsername   sql.NullString
	UserID           int64
}

// GeminiSession is the small in-memory session view used by existing model
// adapters. Its source of truth is aiq.AiSession.
type GeminiSession struct {
	ID                int64
	ChatID            int64
	ChatName          string
	ChatType          string
	Frozen            bool
	TotalInputTokens  int64
	TotalOutputTokens int64
}
