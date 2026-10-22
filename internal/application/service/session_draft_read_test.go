package service

import (
	"context"
	"strings"
	"testing"

	chatpipeline "github.com/Tencent/WeKnora/internal/application/service/chat_pipeline"
	"github.com/Tencent/WeKnora/internal/event"
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

func TestSelectedDraftReachesQuickQAModelWhenIntentSkipsRetrieval(t *testing.T) {
	for _, query := range []string{"1", "这篇", "这篇草稿里有", "读取用户甲的草稿", "读取前五条圆通会员福利官-可乐发送的消息"} {
		t.Run(query, func(t *testing.T) {
			k := &types.Knowledge{ID: "selected", KnowledgeBaseID: "shared", TenantID: 7, Type: "manual", ParseStatus: "draft", EnableStatus: "disabled", Title: "WeChat Log - 用户甲"}
			require.NoError(t, k.SetManualMetadata(types.NewManualKnowledgeMetadata("DRAFT_BODY_REACHED_MODEL", "draft", 1)))
			if strings.Contains(query, "可乐") {
				content := "## 10:00:00 +08:00 - Incoming\n\n- Conversation: 腾讯新闻\n- Sender: 腾讯新闻\n\n~~~text\n" + strings.Repeat("未选中的新闻", 4000) + "\n~~~\n\n## 11:00:00 +08:00 - Incoming\n\n- Conversation: T504\n- Sender: 圆通会员福利官-可乐\n\n~~~text\nDRAFT_BODY_REACHED_MODEL\n~~~\n"
				require.NoError(t, k.SetManualMetadata(types.NewManualKnowledgeMetadata(content, "draft", 1)))
			}
			f := &draftReadService{rows: []*types.Knowledge{k}}
			s := &sessionService{knowledgeService: f, chunkService: struct{ interfaces.ChunkService }{}}
			cm := &types.ChatManage{PipelineRequest: types.PipelineRequest{Query: query, ChatModelID: "capture", KnowledgeIDs: []string{k.ID}, SearchTargets: types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "shared", TenantID: 7}}, SummaryConfig: types.SummaryConfig{Prompt: "Use supplied reference materials.", ContextTemplate: "Reference materials:\n{{contexts}}\nQuestion: {{query}}"}}}
			s.attachSelectedDraftContext(context.Background(), cm)
			if query == "读取用户甲的草稿" {
				cm.KnowledgeIDs = nil
				s.attachSelectedDraftContext(context.Background(), cm)
			}
			cm = cm.Clone()
			cm.Intent = types.IntentFollowUp
			cm.EventBus = event.NewEventBus().AsEventBusInterface()
			require.False(t, cm.NeedsRetrieval())
			manager := chatpipeline.NewEventManager()
			into := chatpipeline.NewPluginIntoChatMessage(manager, nil, nil)
			model := &captureChatModel{}
			completion := chatpipeline.NewPluginChatCompletionStream(manager, &stubModelService{chatModel: model})
			err := into.OnEvent(context.Background(), types.INTO_CHAT_MESSAGE, cm, func() *chatpipeline.PluginError {
				return completion.OnEvent(context.Background(), types.CHAT_COMPLETION_STREAM, cm, func() *chatpipeline.PluginError { return nil })
			})
			require.Nil(t, err)
			require.NotEmpty(t, model.lastMessages)
			require.Contains(t, model.lastMessages[len(model.lastMessages)-1].Content, "DRAFT_BODY_REACHED_MODEL")
			require.Contains(t, cm.RenderedContexts, "DRAFT_BODY_REACHED_MODEL")
			if strings.Contains(query, "可乐") {
				require.NotContains(t, cm.RenderedContexts, "未选中的新闻")
			}
			require.Equal(t, "draft", k.ParseStatus)
			require.Equal(t, "disabled", k.EnableStatus)
		})
	}
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
