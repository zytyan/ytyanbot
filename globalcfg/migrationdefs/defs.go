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

var AIV2OfflineSource = aiV2OfflineDescription + "\n" + aischema.V2

var All = []Definition{
	{Version: 1, Name: "ai_metadata_baseline", Source: AIMetadataBaselineSource},
	{Version: 2, Name: "remove_legacy_ai_memory", Source: RemoveLegacyAIMemorySource},
	{Version: 3, Name: "generic_ai_v2", Source: AIV2OfflineSource, Offline: true},
	{Version: 4, Name: "main_schema_cleanup", Source: MainSchemaCleanupV4Source, Offline: true},
}

func Checksum(source string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(source)))
}
