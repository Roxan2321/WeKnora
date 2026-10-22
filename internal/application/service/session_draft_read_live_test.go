package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/application/repository"
	chatpipeline "github.com/Tencent/WeKnora/internal/application/service/chat_pipeline"
	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	pgdriver "gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlog "gorm.io/gorm/logger"
)

func TestLiveDraftReadOnlyNoIndex(t *testing.T) {
	if os.Getenv("WEKNORA_DRAFT_READ_LIVE") != "1" {
		t.Skip("explicit live read-only check required")
	}
	if os.Getenv("PGDATABASE") != "weknora" {
		t.Fatal("unexpected database")
	}
	db, err := gorm.Open(pgdriver.Open(""), &gorm.Config{Logger: gormlog.Default.LogMode(gormlog.Silent)})
	if err != nil {
		t.Fatal("connect failed")
	}
	tx := db.Begin(&sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	var readOnly string
	tx.Raw("SHOW transaction_read_only").Scan(&readOnly)
	if readOnly != "on" {
		t.Fatal("read-only transaction required")
	}
	const kb = "d0904628-3bb8-45c2-b572-e9c2179a58e7"
	var k types.Knowledge
	if err := tx.Where("knowledge_base_id=? AND type='manual' AND parse_status='draft'", kb).Order("updated_at DESC").First(&k).Error; err != nil {
		t.Fatal("no draft fixture available")
	}
	snapshot := func() string {
		var digest string
		err := tx.Raw(`SELECT md5(COALESCE(string_agg(id||COALESCE(metadata::text,'')||parse_status||enable_status,',' ORDER BY id),'')) FROM knowledges WHERE knowledge_base_id=?`, kb).Scan(&digest).Error
		if err != nil {
			t.Fatal(err)
		}
		return digest
	}
	before := snapshot()
	ks := &knowledgeService{repo: repository.NewKnowledgeRepository(tx)}
	cs := struct{ interfaces.ChunkService }{}
	targets := types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: kb, TenantID: k.TenantID}}
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, k.TenantID+1)
	listArgs, _ := json.Marshal(tools.ListDocumentsInput{KnowledgeBaseID: kb, Keyword: "翁捷", Page: 1, PageSize: 10})
	listed, listErr := tools.NewListDocumentsTool(ks, targets).Execute(ctx, listArgs)
	if listErr != nil || !listed.Success || len(listed.Data["documents"].([]map[string]interface{})) == 0 {
		t.Fatal("live title filter failed")
	}
	s := &sessionService{knowledgeService: ks, chunkService: cs}
	cm := &types.ChatManage{PipelineRequest: types.PipelineRequest{Query: k.Title, SearchTargets: targets}}
	listing, body := s.buildKBDocumentContext(ctx, cm)
	if !strings.Contains(listing, k.Title) || !strings.Contains(body, "<draft_document") {
		t.Fatal("title lookup or direct draft read failed")
	}
	for _, query := range []string{"1", "这篇", "这篇草稿里有", "读取前五条草稿中圆通会员福利官-可乐发送的消息"} {
		quick := cm.Clone()
		quick.Query = query
		quick.KnowledgeIDs = []string{k.ID}
		quick.SearchTargets = types.SearchTargets{{Type: types.SearchTargetTypeKnowledge, KnowledgeBaseID: kb, KnowledgeIDs: []string{k.ID}, TenantID: k.TenantID}}
		quick.ChatModelID = "capture"
		quick.SummaryConfig.ContextTemplate = "Reference materials (source data):\n{{contexts}}\nQuestion: {{query}}"
		s.attachSelectedDraftContext(ctx, quick)
		if strings.Contains(query, "可乐") {
			if strings.Count(quick.DirectDraftContext, "- Sender: 圆通会员福利官-可乐\n") != 5 || strings.Contains(quick.DirectDraftContext, "- Sender: 腾讯新闻") || strings.Contains(quick.DirectDraftContext, "- Sender: 圆通商贸福利官-可乐") {
				header, _, _ := strings.Cut(quick.DirectDraftContext, "\n")
				t.Fatalf("full-text exact contact selection failed: requested sender count=%d; %s", strings.Count(quick.DirectDraftContext, "- Sender: 圆通会员福利官-可乐\n"), header)
			}
			meta, err := k.ManualMetadata()
			if err != nil {
				t.Fatal(err)
			}
			verified := 0
			for _, block := range strings.Split(meta.Content, "\n## ") {
				header, _, _ := strings.Cut(block, "~~~")
				if !strings.Contains(header, "- Sender: 圆通会员福利官-可乐\n") {
					continue
				}
				for _, line := range strings.Split(header, "\n") {
					if strings.HasPrefix(line, "- Message reference: ") && !strings.Contains(quick.DirectDraftContext, line) {
						t.Fatal("selected messages differ from the first five original message references")
					}
				}
				verified++
				if verified == 5 {
					break
				}
			}
			if verified != 5 {
				t.Fatal("live fixture has fewer than five messages from the requested sender")
			}
			t.Log("full-text exact contact read returned five messages from the requested sender, excluding Tencent News and the similarly named other contact")
		}
		quick = quick.Clone()
		quick.Intent = types.IntentFollowUp
		quick.EventBus = event.NewEventBus().AsEventBusInterface()
		start := strings.Index(quick.DirectDraftContext, ">\n") + 2
		end := strings.Index(quick.DirectDraftContext, "\n</draft_document>")
		if start < 2 || end <= start {
			t.Fatal("selected draft evidence missing")
		}
		sample := []rune(quick.DirectDraftContext[start:end])
		if len(sample) > 24 {
			sample = sample[:24]
		}
		manager := chatpipeline.NewEventManager()
		into := chatpipeline.NewPluginIntoChatMessage(manager, nil, nil)
		model := &captureChatModel{}
		completion := chatpipeline.NewPluginChatCompletionStream(manager, &stubModelService{chatModel: model})
		if err := into.OnEvent(ctx, types.INTO_CHAT_MESSAGE, quick, func() *chatpipeline.PluginError {
			return completion.OnEvent(ctx, types.CHAT_COMPLETION_STREAM, quick, func() *chatpipeline.PluginError { return nil })
		}); err != nil {
			t.Fatal("quick QA formatting failed")
		}
		if len(model.lastMessages) == 0 || !strings.Contains(model.lastMessages[len(model.lastMessages)-1].Content, string(sample)) || !strings.Contains(model.lastMessages[len(model.lastMessages)-1].Content, quick.DirectDraftContext) {
			t.Fatal("draft text did not reach final model request")
		}
	}
	t.Log("selected-file quick QA with no-search intent passed for 1 / this document / draft follow-up; final model input contains real draft text; mocked model only")
	cm.Query = "这个知识库里有什么"
	_, body = s.buildKBDocumentContext(ctx, cm)
	if body != "" {
		t.Fatal("browse query must not fetch draft bodies")
	}
	reader := tools.NewReadDocumentTool(ks, cs, targets)
	args, _ := json.Marshal(tools.ReadDocumentInput{ID: k.ID, Offset: 0, Limit: 20})
	result, err := reader.Execute(ctx, args)
	if err != nil || !result.Success {
		t.Fatal("draft read failed")
	}
	if len([]rune(result.Data["content"].(string))) > 20 {
		t.Fatal("page exceeded limit")
	}
	unauthorized := tools.NewReadDocumentTool(ks, cs, types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "not-authorized", TenantID: k.TenantID}})
	if denied, err := unauthorized.Execute(ctx, args); err == nil || denied.Success {
		t.Fatal("scope bypass")
	}
	tagDenied := tools.NewReadDocumentTool(ks, cs, types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: kb, TenantID: k.TenantID, ScopeTagIDs: []string{"draft-test-not-granted"}}})
	if denied, err := tagDenied.Execute(ctx, args); err == nil || denied.Success {
		t.Fatal("tag scope bypass")
	}
	if snapshot() != before {
		t.Fatal("draft data changed")
	}
	for _, table := range []string{"chunks", "embeddings"} {
		var count int64
		if err := tx.Table(table).Where("knowledge_base_id=?", kb).Count(&count).Error; err != nil || count != 0 {
			t.Fatal("draft unexpectedly indexed")
		}
	}
	t.Log("live draft title lookup, direct paged read, shared-owner scope, outside-scope denial and unchanged draft/disabled/zero-index state passed; no model calls")
}
