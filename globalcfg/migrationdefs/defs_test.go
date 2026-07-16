package migrationdefs

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAIV2MigrationChecksumIsFrozen(t *testing.T) {
	require.Equal(t, "41c89177935e668c192883c27130b6e943fa9f15eb6402b9abc9428a3679fe88",
		Checksum(AIV2OfflineSource))
}
