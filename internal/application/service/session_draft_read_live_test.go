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
