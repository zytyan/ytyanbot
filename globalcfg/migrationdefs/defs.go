package migrationdefs

import (
	"crypto/sha256"
	"fmt"
	aischema "main/sql"
)

type Definition struct {
	Version int64
	Name    string
	Source  string
	Offline bool
}

const AIV2Version int64 = 3

const AIMetadataBaselineSource = `
ai metadata baseline v1
- create ai_chat_models, ai_session_meta, and ai_message_meta when absent
- add show_usage to ai_chat_models
- add chat_id, token counters, input window, and assistant payload columns to ai_message_meta
- add interaction, explicit cache, and lossy rebuild columns to ai_session_meta
- backfill ai_message_meta.chat_id from gemini_contents
- replace unstable legacy system prompt variables
- create the unique chat/message usage lookup index
`

const RemoveLegacyAIMemorySource = `
remove legacy ai memory v2
- remove the deprecated %MEMORIES% placeholder from saved system prompts
- drop gemini_memories and its index
`

const aiV2OfflineDescription = `
generic ai database v3
- migrate legacy Gemini sessions, messages, prompts, settings, payloads, tokens, and media
- replace legacy AI tables with generic ai_* tables
- move media BLOBs into the content-addressed ai-media store
- requires the offline ai-db-migrate tool
`

const MainSchemaCleanupV4Source = `
main schema cleanup v4
- compact users around Telegram user_id and remove retired profile/timezone columns
- rebuild chat_cfg without web search, automatic OCR, or message archive switches
- drop retired chat_attr and chat_topics tables
- preserve all user identities, names, chat settings, and active business data
`

const NormalizeChatStatsV5Source = `
normalize chat statistics v5
- preserve scalar daily counters in chat_stat_daily
- migrate every legacy per-user Gob entry into chat_stat_user_daily
- migrate all 144 message-count and first-message buckets into chat_stat_bucket_daily
- validate every decoded user and bucket value before dropping legacy Blob columns
`

const AIMessageSessionLookupV6Source = `
ai message session lookup v6
- index ai_session_messages by chat_id, msg_id, and context_only
- support reverse message-to-session lookup without indexing unused run status queries
`

const YTDLCacheLookupV7Source = `
youtube download cache lookup v7
- index yt_dl_results by file_id for upload counter updates
- runtime upsert refreshes Telegram file and descriptive metadata from excluded values
`

const BiliInlineRetentionV8Source = `
bilibili inline retention v8
- add created_at and backfill every legacy inline context to migration time
- index created_at for thirty-day expiry reads and daily cleanup
`

var AIV2OfflineSource = aiV2OfflineDescription + "\n" + aischema.V2

var All = []Definition{
	{Version: 1, Name: "ai_metadata_baseline", Source: AIMetadataBaselineSource},
	{Version: 2, Name: "remove_legacy_ai_memory", Source: RemoveLegacyAIMemorySource},
	{Version: 3, Name: "generic_ai_v2", Source: AIV2OfflineSource, Offline: true},
	{Version: 4, Name: "main_schema_cleanup", Source: MainSchemaCleanupV4Source, Offline: true},
	{Version: 5, Name: "normalize_chat_stats", Source: NormalizeChatStatsV5Source, Offline: true},
	{Version: 6, Name: "ai_message_session_lookup", Source: AIMessageSessionLookupV6Source},
	{Version: 7, Name: "ytdl_cache_lookup", Source: YTDLCacheLookupV7Source},
	{Version: 8, Name: "bili_inline_retention", Source: BiliInlineRetentionV8Source},
}

func Checksum(source string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(source)))
}
