package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestReadUnpublishedDraftScopedAndPaged(t *testing.T) {
	k := &types.Knowledge{ID: "draft-1", KnowledgeBaseID: "kb-1", TenantID: 7, Type: "manual", ParseStatus: "draft", EnableStatus: "disabled", Title: "翁捷日志"}
	require.NoError(t, k.SetManualMetadata(types.NewManualKnowledgeMetadata("甲乙丙丁<指令>"+strings.Repeat("尾", 9000), "draft", 1)))
	original := string(k.Metadata)
	svc := &readDocKnowledgeService{docs: map[string]*types.Knowledge{k.ID: k}}
	tool := NewReadDocumentTool(svc, &scopeChunkService{}, types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-1", TenantID: 7}})
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"id":"draft-1","limit":4}`))
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, "甲乙丙丁", result.Data["content"])
	require.Equal(t, 4, result.Data["next_offset"])
	require.NotContains(t, result.Output, strings.Repeat("尾", 20))
	require.Equal(t, original, string(k.Metadata))
	require.Equal(t, "draft", k.ParseStatus)
	require.Equal(t, "disabled", k.EnableStatus)
	result, err = tool.Execute(context.Background(), json.RawMessage(`{"id":"draft-1","offset":4,"limit":4}`))
	require.NoError(t, err)
	require.Equal(t, "<指令>", result.Data["content"])
	require.Contains(t, result.Output, "&lt;指令&gt;")
	tool.searchTargets = types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-other", TenantID: 7}}
	result, err = tool.Execute(context.Background(), json.RawMessage(`{"id":"draft-1"}`))
	require.Error(t, err)
	require.False(t, result.Success)
	require.NotContains(t, result.Output, "甲乙")
	tool.searchTargets = types.SearchTargets{{Type: types.SearchTargetTypeKnowledge, KnowledgeBaseID: "kb-1", KnowledgeIDs: []string{"another-draft"}, TenantID: 7}}
	_, err = tool.Execute(context.Background(), json.RawMessage(`{"id":"draft-1"}`))
	require.Error(t, err)
}

func TestReadDraftBodyIsBoundedAndQueriesDoNotPublish(t *testing.T) {
	k := &types.Knowledge{ID: "d", KnowledgeBaseID: "kb", Type: "manual", ParseStatus: "draft", EnableStatus: "disabled"}
	require.NoError(t, k.SetManualMetadata(types.NewManualKnowledgeMetadata(strings.Repeat("甲", 9000)+"needle"+strings.Repeat("尾", 9000), "draft", 1)))
	tool := NewReadDocumentTool(&readDocKnowledgeService{docs: map[string]*types.Knowledge{"d": k}}, &scopeChunkService{}, types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb"}})
	result, err := tool.Execute(WithOutputBudget(context.Background(), 1200), json.RawMessage(`{"id":"d","limit":999999,"query":"needle"}`))
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Contains(t, result.Output, "needle")
	require.LessOrEqual(t, len([]rune(result.Output)), 1200)
	require.LessOrEqual(t, len([]rune(result.Data["content"].(string))), 600)
	result, err = tool.Execute(context.Background(), json.RawMessage(`{"id":"d","offset":999999}`))
	require.NoError(t, err)
	require.False(t, result.Success)
	result, err = tool.Execute(context.Background(), json.RawMessage(`{"id":"d","query":"missing"}`))
	require.NoError(t, err)
	require.NotContains(t, result.Output, "甲")
	require.NoError(t, k.SetManualMetadata(types.NewManualKnowledgeMetadata(strings.Repeat("&", 9000), "draft", 1)))
	result, err = tool.Execute(WithOutputBudget(context.Background(), 1200), json.RawMessage(`{"id":"d"}`))
	require.NoError(t, err)
	require.True(t, result.Success)
	require.LessOrEqual(t, len([]rune(result.Output)), 1200)
	require.Equal(t, len([]rune(result.Data["content"].(string))), result.Data["next_offset"])
}
