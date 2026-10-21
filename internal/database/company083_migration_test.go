package database

import (
	"io"
	"testing"

	"github.com/golang-migrate/migrate/v4/source"
	"github.com/stretchr/testify/require"
)

func TestCompany083MigrationGraph(t *testing.T) {
	driver, err := source.Open("file://" + sqliteRepoRoot(t) + "/migrations/versioned")
	require.NoError(t, err, "migration versions must be unique")
	defer driver.Close()
	for _, version := range []uint{111, 119} {
		reader, _, err := driver.ReadUp(version)
		require.NoError(t, err)
		data, err := io.ReadAll(reader)
		require.NoError(t, err)
		require.NoError(t, reader.Close())
		require.Contains(t, string(data), "model_catalog_configs")
		if version == 119 {
			require.Contains(t, string(data), "search_content")
		}
	}
}
