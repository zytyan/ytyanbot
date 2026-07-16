package legacyschema

import _ "embed"

// V1 describes the legacy AI tables expected by the offline reader.
//
//go:embed schema_ai_v1.sql
var V1 string

// V2V3 freezes the exact generic AI schema used to checksum migration V3.
// Later canonical schema changes must be registered as newer migrations.
//
//go:embed schema_ai_v2_v3.sql
var V2V3 string
