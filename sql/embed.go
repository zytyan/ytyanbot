package aischema

import _ "embed"

// V2 is the canonical generic AI schema used by the offline migration tool.
//
//go:embed schema_ai_v2.sql
var V2 string

// Canonical schema fragments are ordered so a new main database can be
// created deterministically without loading retired migration-only schemas.
//
//go:embed schema_user.sql
var schemaUser string

//go:embed schema_chat.sql
var schemaChat string

//go:embed schema_coc.sql
var schemaCOC string

//go:embed schema_pics.sql
var schemaPics string

//go:embed schema_ytdl.sql
var schemaYTDL string

//go:embed schema_bilibili.sql
var schemaBilibili string

func Canonical() []string {
	return []string{schemaUser, schemaChat, schemaCOC, schemaPics, schemaYTDL, schemaBilibili, V2}
}
