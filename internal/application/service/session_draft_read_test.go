package service

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

type draftReadService struct {
	interfaces.KnowledgeService
	rows       []*types.Knowledge
	reads      int
	listTenant uint64
}

func (f *draftReadService) GetKnowledgeByIDOnly(_ context.Context, id string) (*types.Knowledge, error) {
	f.reads++
	for _, k := range f.rows {
		if k.ID == id {
			return k, nil
		}
	}
	return nil, nil
}
func (f *draftReadService) GetKnowledgeBatch(_ context.Context, _ uint64, ids []string) ([]*types.Knowledge, error) {
	var out []*types.Knowledge
	for _, k := range f.rows {
		for _, id := range ids {
			if k.ID == id {
				out = append(out, k)
			}
		}
	}
	return out, nil
}
func (f *draftReadService) ListPagedKnowledgeByKnowledgeBaseID(ctx context.Context, kb string, p *types.Pagination, _ types.KnowledgeListFilter) (*types.PageResult, error) {
	f.listTenant, _ = ctx.Value(types.TenantIDContextKey).(uint64)
	var out []*types.Knowledge
	for _, k := range f.rows {
		if k.KnowledgeBaseID == kb {
			out = append(out, k)
		}
	}
	return types.NewPageResult(int64(len(out)), p, out), nil
}

func TestDraftTitleMentioned(t *testing.T) {
	title := "WeChat Log - 翁捷 - 2026-10-22 - 5f767544 - 01"
	require.True(t, draftTitleMentioned("读取翁捷2026-10-22的聊天记录", title))
	require.False(t, draftTitleMentioned("读取翁捷2026-10-21的聊天记录", title))
	require.False(t, draftTitleMentioned("这个知识库里有什么", title))
	require.False(t, draftTitleMentioned("请读取聊天记录", title))
	require.False(t, draftTitleMentioned("查找T504群", title))
	require.True(t, draftTitleMentioned("读取T504群", "WeChat Log - T504群 - 2026-10-22"))
}

func TestOrdinaryQADraftsFollowSharedAndPinnedScope(t *testing.T) {
	k := &types.Knowledge{ID: "draft", KnowledgeBaseID: "shared", TenantID: 7, Title: "WeChat Log - 翁捷 - 2026-10-22", Type: "manual", ParseStatus: "draft", EnableStatus: "disabled"}
	require.NoError(t, k.SetManualMetadata(types.NewManualKnowledgeMetadata("authorized-body", "draft", 1)))
	other := &types.Knowledge{ID: "other", KnowledgeBaseID: "other-kb", Type: "manual", ParseStatus: "draft", Title: "翁捷-other"}
	require.NoError(t, other.SetManualMetadata(types.NewManualKnowledgeMetadata("OUTSIDE-SECRET", "draft", 1)))
	f := &draftReadService{rows: []*types.Knowledge{k, other}}
	// A non-nil chunk service is required by the shared tool, but no method may run for drafts.
	s := &sessionService{knowledgeService: f, chunkService: struct{ interfaces.ChunkService }{}}
	cm := &types.ChatManage{PipelineRequest: types.PipelineRequest{Query: "这个知识库里有什么", SearchTargets: types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "shared", TenantID: 7}}}}
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(99))
	listing, body := s.buildKBDocumentContext(ctx, cm)
	require.Contains(t, listing, "翁捷")
	require.Contains(t, listing, "unpublished draft")
	require.Empty(t, body)
	require.Zero(t, f.reads)
	require.Equal(t, uint64(7), f.listTenant)
	cm.Query = "读取翁捷2026-10-22的聊天记录"
	_, body = s.buildKBDocumentContext(ctx, cm)
	require.Contains(t, body, "authorized-body")
	require.NotContains(t, body, "OUTSIDE-SECRET")
	cm.FallbackPrompt = "Query {{query}}; documents {{kb_documents}}"
	prompt, err := s.renderFallbackPrompt(ctx, cm)
	require.NoError(t, err)
	require.Contains(t, prompt, "authorized-body")
	require.Contains(t, prompt, "authorized draft excerpts")
	require.NotContains(t, prompt, "OUTSIDE-SECRET")
	cm.SearchTargets = types.SearchTargets{{Type: types.SearchTargetTypeKnowledge, KnowledgeBaseID: "shared", KnowledgeIDs: []string{"missing"}, TenantID: 7}}
	_, body = s.buildKBDocumentContext(ctx, cm)
	require.Empty(t, body)
	cm.SearchTargets = nil
	cm.KnowledgeBaseIDs = []string{"shared"}
	listing, body = s.buildKBDocumentContext(ctx, cm)
	require.Empty(t, listing)
	require.Empty(t, body)
	require.Equal(t, "draft", k.ParseStatus)
	require.Equal(t, "disabled", k.EnableStatus)
}
