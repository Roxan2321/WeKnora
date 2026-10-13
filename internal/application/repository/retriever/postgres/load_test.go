package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/lib/pq"
	"github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/require"
	pgdriver "gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlog "gorm.io/gorm/logger"
)

// Opt-in synthetic retrieval load only; never point this at a business database.
func TestRDSRetrievalLoad(t *testing.T) {
	dsn := os.Getenv("WEKNORA_LOAD_TEST_DSN")
	if dsn == "" {
		t.Skip("set WEKNORA_LOAD_TEST_DSN for the isolated synthetic load database")
	}
	u, err := url.Parse(dsn)
	require.NoError(t, err)
	require.Equal(t, "/weknora_load_test", u.Path)
	require.Equal(t, "require", u.Query().Get("sslmode"))
	db, err := gorm.Open(pgdriver.Open(dsn), &gorm.Config{Logger: gormlog.Default.LogMode(gormlog.Silent)})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	defer pool.Close()
	pool.SetMaxOpenConns(48)
	pool.SetMaxIdleConns(8)
	logger.SetLogLevel(logger.LevelError)
	ctx := t.Context()
	var name string
	require.NoError(t, pool.QueryRowContext(ctx, "SELECT current_database()").Scan(&name))
	require.Equal(t, "weknora_load_test", name)
	var ssl bool
	require.NoError(t, pool.QueryRowContext(ctx, "SELECT ssl FROM pg_stat_ssl WHERE pid=pg_backend_pid()").Scan(&ssl))
	require.True(t, ssl)

	if os.Getenv("WEKNORA_LOAD_STAGE") == "seed" {
		var count int
		require.NoError(t, pool.QueryRowContext(ctx, "SELECT count(*) FROM embeddings").Scan(&count))
		require.Zero(t, count, "seed requires an empty isolated embeddings table")
		for _, index := range []string{"embeddings_embedding_idx_1024", "embeddings_textsearch_idx"} {
			require.NoError(t, db.Exec("DROP INDEX IF EXISTS "+index).Error)
		}
		rng := rand.New(rand.NewSource(20261013))
		words := strings.Fields("渠道 店铺 客户 订单 售后 回复 自动 项目 落地 代包 合单 仓库 库存 发货 招商 合肥 社群 活动 人事 考勤 招聘 流程 审核 系统 数据 报表 分析 记录 工作 总结 计划 进度 异常 退货 配送 需求 反馈 优化 运营 采购 商品 分类 价格 预算 销售 任务 周报 月报 查询 更新 同步 接口 权限 通知 成本 测试 验证 编号 负责人 状态 时间 效率 知识 文档 服务 客服")
		copyPool, err := sql.Open("postgres", dsn)
		require.NoError(t, err)
		defer copyPool.Close()
		tx, err := copyPool.BeginTx(ctx, nil)
		require.NoError(t, err)
		defer tx.Rollback()
		_, err = tx.Exec("CREATE TEMP TABLE load_seed (LIKE embeddings INCLUDING DEFAULTS) ON COMMIT DROP")
		require.NoError(t, err)
		copyStmt, err := tx.Prepare(pq.CopyIn("load_seed", "source_id", "source_type", "chunk_id", "knowledge_id", "knowledge_base_id", "tag_id", "content", "search_content", "dimension", "embedding", "is_enabled"))
		require.NoError(t, err)
		started := time.Now()
		for i := 0; i < 195143; i++ {
			kb := "kb-a"
			if i >= 131555 {
				kb = "kb-b"
			}
			if i >= 195121 {
				kb = "kb-c"
			}
			length := 249
			if i%100 >= 90 {
				length = 1158
			}
			if i%100 == 99 {
				length = 3099
			}
			var body strings.Builder
			for body.Len() < length {
				body.WriteString(words[rng.Intn(len(words))])
			}
			content := body.String()
			id := fmt.Sprintf("load-%06d", i)
			_, err = copyStmt.Exec(id, 0, id, fmt.Sprintf("doc-%d", i%169), kb, fmt.Sprintf("tag-%d", i%8), content, keywordText(content), 1024, nil, true)
			require.NoError(t, err)
			if i > 0 && i%25000 == 0 {
				fmt.Printf("LOAD seed rows=%d elapsed=%s\n", i, time.Since(started).Round(time.Second))
			}
		}
		_, err = copyStmt.Exec()
		require.NoError(t, err)
		require.NoError(t, copyStmt.Close())
		fmt.Println("LOAD synthetic text transferred; generating random vectors inside RDS")
		_, err = tx.ExecContext(ctx, `INSERT INTO embeddings (source_id,source_type,chunk_id,knowledge_id,knowledge_base_id,tag_id,content,search_content,dimension,embedding,is_enabled)
			SELECT source_id,source_type,chunk_id,knowledge_id,knowledge_base_id,tag_id,content,search_content,dimension,
			ARRAY(SELECT (random()*2-1)::real FROM generate_series(1,1024) WHERE length(seed.source_id)>0)::halfvec,is_enabled
			FROM load_seed AS seed`)
		require.NoError(t, err)
		require.NoError(t, tx.Commit())
		fmt.Println("LOAD seed committed; creating BM25 and HNSW indexes")
		require.NoError(t, db.Exec("CREATE INDEX embeddings_textsearch_idx ON embeddings USING bm25(search_content) WITH(text_config='simple')").Error)
		fmt.Println("LOAD BM25 ready; creating 1024-dimension HNSW")
		conn, err := pool.Conn(ctx)
		require.NoError(t, err)
		defer conn.Close()
		_, err = conn.ExecContext(ctx, "SET maintenance_work_mem='128MB'")
		require.NoError(t, err)
		_, err = conn.ExecContext(ctx, "CREATE INDEX embeddings_embedding_idx_1024 ON embeddings USING hnsw((embedding::halfvec(1024)) halfvec_cosine_ops) WITH(m=16,ef_construction=64) WHERE dimension=1024")
		require.NoError(t, err)
		require.NoError(t, db.Exec("ANALYZE embeddings").Error)
		fmt.Printf("LOAD seed ready rows=195143 elapsed=%s\n", time.Since(started).Round(time.Second))
		return
	}

	var rows int
	require.NoError(t, pool.QueryRowContext(ctx, "SELECT count(*) FROM embeddings WHERE source_id LIKE 'load-%'").Scan(&rows))
	require.Equal(t, 195143, rows)
	var valid int
	require.NoError(t, pool.QueryRowContext(ctx, "SELECT count(*) FROM pg_index WHERE indexrelid IN ('embeddings_textsearch_idx'::regclass,'embeddings_embedding_idx_1024'::regclass) AND indisvalid").Scan(&valid))
	require.Equal(t, 2, valid)
	repo := &pgRepository{db: db, textSearch: true}
	var vectors [][]float32
	for i := 0; i < 128; i++ {
		var v pgvector.HalfVector
		err := pool.QueryRowContext(ctx, "SELECT embedding FROM embeddings WHERE source_id=$1", fmt.Sprintf("load-%06d", i*1500)).Scan(&v)
		require.NoError(t, err)
		require.Len(t, v.Slice(), 1024)
		vectors = append(vectors, v.Slice())
	}
	require.NotEqual(t, vectors[0], vectors[1], "synthetic vectors must vary between rows")
	check, err := repo.VectorRetrieve(ctx, types.RetrieveParams{Embedding: vectors[0], TopK: 1, KnowledgeBaseIDs: []string{"kb-a"}})
	require.NoError(t, err)
	require.NotEmpty(t, check)
	require.NotEmpty(t, check[0].Results)
	require.Equal(t, "load-000000", check[0].Results[0].SourceID)
	queries := []string{"自动 售后", "渠道 项目", "订单 发货", "工作 总结", "库存 查询", "人事 流程", "社群 活动", "数据 同步"}
	runQuery := func(ctx context.Context, kind string, i int) error {
		params := types.RetrieveParams{Query: queries[i%len(queries)], Embedding: vectors[i%len(vectors)], TopK: 10, KnowledgeBaseIDs: []string{"kb-a"}}
		if i%5 == 0 {
			params.KnowledgeBaseIDs = []string{"kb-a", "kb-b"}
		}
		keyword := func() error { _, err := repo.KeywordsRetrieve(ctx, params); return err }
		vector := func() error { _, err := repo.VectorRetrieve(ctx, params); return err }
		switch kind {
		case "keyword":
			return keyword()
		case "vector":
			return vector()
		default:
			ch := make(chan error, 1)
			go func() { ch <- keyword() }()
			err := vector()
			other := <-ch
			if err != nil {
				return err
			}
			return other
		}
	}
	type result struct {
		Kind        string  `json:"kind"`
		Concurrency int     `json:"concurrency"`
		Requests    int     `json:"requests"`
		Errors      int     `json:"errors"`
		Seconds     float64 `json:"seconds"`
		QPS         float64 `json:"qps"`
		P50ms       float64 `json:"p50_ms"`
		P95ms       float64 `json:"p95_ms"`
		P99ms       float64 `json:"p99_ms"`
		Meanms      float64 `json:"mean_ms"`
		Writes      int     `json:"writes"`
		WriteErrors int     `json:"write_errors"`
	}
	var report []result
	runStage := func(kind string, concurrency int, duration time.Duration, writes bool) result {
		fmt.Printf("LOAD stage_start kind=%s concurrency=%d duration=%s\n", kind, concurrency, duration)
		started := time.Now()
		stageCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		var wg sync.WaitGroup
		var mu sync.Mutex
		var latencies []float64
		r := result{Kind: kind, Concurrency: concurrency}
		var firstError error
		for worker := 0; worker < concurrency; worker++ {
			wg.Add(1)
			go func(worker int) {
				defer wg.Done()
				for i := worker; time.Since(started) < duration && stageCtx.Err() == nil; i++ {
					mode := kind
					if kind == "mixed" || writes {
						mode = []string{"keyword", "keyword", "vector", "dual"}[i%4]
					}
					opCtx, opCancel := context.WithTimeout(stageCtx, 5*time.Second)
					begin := time.Now()
					err := runQuery(opCtx, mode, i)
					opCancel()
					ms := float64(time.Since(begin).Microseconds()) / 1000
					mu.Lock()
					latencies = append(latencies, ms)
					r.Requests++
					if err != nil {
						r.Errors++
						if firstError == nil {
							firstError = err
						}
						if r.Errors >= 5 && float64(r.Errors)/float64(r.Requests) > .05 {
							cancel()
						}
					}
					mu.Unlock()
				}
			}(worker)
		}
		if writes {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; time.Since(started) < duration && stageCtx.Err() == nil; i++ {
					id := fmt.Sprintf("writer-%d", i)
					opCtx, opCancel := context.WithTimeout(stageCtx, 5*time.Second)
					err := repo.Save(opCtx, &types.IndexInfo{SourceID: id, ChunkID: id, KnowledgeBaseID: "kb-a", Content: "渠道部门自动售后回复项目工作记录", IsEnabled: true}, map[string]any{"embedding": map[string][]float32{id: vectors[i%len(vectors)]}})
					opCancel()
					mu.Lock()
					if err == nil {
						r.Writes++
					} else {
						r.WriteErrors++
						if firstError == nil {
							firstError = err
						}
					}
					mu.Unlock()
					select {
					case <-stageCtx.Done():
						return
					case <-time.After(time.Second):
					}
				}
			}()
		}
		wg.Wait()
		r.Seconds = time.Since(started).Seconds()
		r.QPS = float64(r.Requests) / r.Seconds
		sort.Float64s(latencies)
		for _, ms := range latencies {
			r.Meanms += ms
		}
		if len(latencies) > 0 {
			r.Meanms /= float64(len(latencies))
		}
		if len(latencies) > 0 {
			r.P50ms = latencies[(len(latencies)-1)*50/100]
			r.P95ms = latencies[(len(latencies)-1)*95/100]
			r.P99ms = latencies[(len(latencies)-1)*99/100]
		}
		if firstError != nil {
			fmt.Printf("LOAD first_error stage=%s: %v\n", kind, firstError)
		}
		data, err := json.Marshal(r)
		require.NoError(t, err)
		fmt.Printf("LOAD_RESULT %s\n", data)
		report = append(report, r)
		data, err = json.MarshalIndent(report, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile("rds-load-results.json", data, 0600))
		return r
	}
	for _, kind := range []string{"keyword", "vector", "dual"} {
		r := runStage(kind, 1, 30*time.Second, false)
		if r.Errors > 0 {
			return
		}
	}
	lastSafe := 1
	for _, concurrency := range []int{1, 5, 10, 20} {
		r := runStage("mixed", concurrency, 180*time.Second, false)
		if r.Errors > 0 || r.P95ms > 2000 {
			fmt.Println("LOAD stopping concurrency ramp at latency/error guardrail")
			break
		}
		lastSafe = concurrency
	}
	runStage("mixed_with_writes", lastSafe, 180*time.Second, true)
}
