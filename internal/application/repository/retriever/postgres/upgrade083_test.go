package postgres

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestCompanyRDSUpsertRefreshesSearchContent(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		repo := &pgRepository{textSearch: enabled}
		found := false
		for _, assignment := range repo.upsertBySource().DoUpdates {
			found = found || assignment.Column.Name == "search_content"
		}
		require.Equal(t, enabled, found)
	}
	row := toDBVectorEmbedding(&types.IndexInfo{Content: "updated", IsEnabled: false}, nil, true)
	require.False(t, row.IsEnabled)
	require.Nil(t, row.Embedding)
	require.Equal(t, "updated", *row.SearchContent)
}
