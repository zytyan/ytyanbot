package legacyschema

import _ "embed"

// V1 describes the legacy AI tables expected by the offline reader.
//
//go:embed schema_ai_v1.sql
var V1 string
