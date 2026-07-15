package aischema

import _ "embed"

// V2 is the canonical generic AI schema used by the offline migration tool.
//
//go:embed schema_ai_v2.sql
var V2 string
