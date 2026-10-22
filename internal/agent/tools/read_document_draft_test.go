package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestDraftConversationScansPastPrefixWithoutPublishing(t *testing.T) {
	block := func(sender, group, body string) string {
		return fmt.Sprintf("## 11:00:00 +08:00 - Incoming\n\n- Conversation: %s\n- Sender: %s\n- Type: 普通文本\n\n~~~text\n%s\n~~~\n\n", group, sender, body)
	}
	content := block("腾讯新闻", "腾讯新闻", strings.Repeat("新闻", 10000))
	content += block("群系统", "T504", "可乐邀请其他人，不能当成可乐发言")
	for i := 1; i <= 6; i++ {
		content += block("圆通会员福利官-可乐", "T504", fmt.Sprintf("COLA_MESSAGE_%d", i))
		content += block("圆通商贸福利官-可乐", "T527", "OTHER_CONTACT")
	}
	content += block("圆通会员福利官-可乐", "另一个私聊", "LATER_PRIVATE_CHAT")
	content += block("圆通商贸福利官-可乐", "圆通商贸福利官-可乐", "OTHER_PRIVATE_CHAT")
	k := &types.Knowledge{ID: "d", KnowledgeBaseID: "kb", Type: "manual", ParseStatus: "draft", EnableStatus: "disabled"}
	require.NoError(t, k.SetManualMetadata(types.NewManualKnowledgeMetadata(content, "draft", 1)))
	original := string(k.Metadata)
	tool := NewReadDocumentTool(&readDocKnowledgeService{docs: map[string]*types.Knowledge{"d": k}}, &scopeChunkService{}, types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb"}})
	args, _ := json.Marshal(ReadDocumentInput{ID: "d", DraftQuestion: "读取前五条草稿中圆通会员福利官-可乐发送的消息", Limit: 4000})
	r, err := tool.Execute(WithOutputBudget(context.Background(), 6000), args)
	require.NoError(t, err)
	require.True(t, r.Success)
	require.Equal(t, 7, r.Data["matched_messages"])
	require.Equal(t, 5, r.Data["returned_messages"])
	for i := 1; i <= 5; i++ {
		require.Contains(t, r.Output, fmt.Sprintf("COLA_MESSAGE_%d", i))
	}
	require.NotContains(t, r.Output, "COLA_MESSAGE_6")
	require.NotContains(t, r.Output, "OTHER_CONTACT")
	require.NotContains(t, r.Output, "腾讯新闻")
	require.NotContains(t, r.Output, "不能当成可乐发言")
	require.Equal(t, original, string(k.Metadata))
	require.Equal(t, "draft", k.ParseStatus)
	require.Equal(t, "disabled", k.EnableStatus)
	args, _ = json.Marshal(ReadDocumentInput{ID: "d", DraftQuestion: "读取前五条圆通会员福利官-可乐发送的消息", Offset: r.Data["next_offset"].(int)})
	r, err = tool.Execute(context.Background(), args)
	require.NoError(t, err)
	require.Equal(t, 2, r.Data["returned_messages"])
	require.Contains(t, r.Output, "COLA_MESSAGE_6")
	require.Contains(t, r.Output, "LATER_PRIVATE_CHAT")
	require.NotContains(t, r.Output, "COLA_MESSAGE_1")
	args, _ = json.Marshal(ReadDocumentInput{ID: "d", Query: "可乐"})
	r, err = tool.Execute(context.Background(), args)
	require.NoError(t, err)
	require.Equal(t, 14, r.Data["matched_messages"])
	args, _ = json.Marshal(ReadDocumentInput{ID: "d", DraftQuestion: "读取T504群可乐发送的消息"})
	r, err = tool.Execute(context.Background(), args)
	require.NoError(t, err)
	require.Equal(t, 6, r.Data["matched_messages"])
	require.NotContains(t, r.Output, "OTHER_CONTACT")
	require.NotContains(t, r.Output, "不能当成可乐发言")
	args, _ = json.Marshal(ReadDocumentInput{ID: "d", DraftQuestion: "T999群可乐发送的消息"})
	r, err = tool.Execute(context.Background(), args)
	require.NoError(t, err)
	require.Equal(t, 0, r.Data["matched_messages"])
	require.Empty(t, r.Data["content"])
	tool.searchTargets = types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "other"}}
	r, err = tool.Execute(context.Background(), args)
	require.Error(t, err)
	require.False(t, r.Success)
}

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
