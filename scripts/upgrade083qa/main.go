// Run only in the named disposable database; never starts workers or model calls.
package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"time"

	pgrepo "github.com/Tencent/WeKnora/internal/application/repository/retriever/postgres"
	"github.com/Tencent/WeKnora/internal/database"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/jackc/pgx/v5"
	pgdriver "gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func must(err error) {
	if err != nil {
		panic(err)
	}
}

func main() {
	name := os.Getenv("PGDATABASE")
	production := name == "weknora" && os.Getenv("WEKNORA_083_PRODUCTION_MIGRATE") == "confirmed" &&
		os.Getenv("PGHOST") == "pgm-uf6g5my29491x4fx3o.pg.rds.aliyuncs.com"
	if !production && name != "weknora_083_upgrade_test_20261021" && name != "weknora_083_fresh_test_20261021" {
		panic("disposable database required")
	}
	u := url.URL{Scheme: "postgres", Host: os.Getenv("PGHOST") + ":5432", Path: "/" + name,
		User: url.UserPassword(os.Getenv("PGUSER"), os.Getenv("PGPASSWORD"))}
	u.RawQuery = url.Values{"sslmode": {"verify-full"}, "sslrootcert": {os.Getenv("PGSSLROOTCERT")},
		"options": {"-c app.skip_embedding=false -c app.postgres_search_extension=pg_textsearch"}}.Encode()
	dsn := u.String()
	os.Setenv("POSTGRES_SEARCH_EXTENSION", "pg_textsearch")
	must(database.RunMigrations(dsn))
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	c, err := pgx.Connect(ctx, dsn)
	must(err)
	defer c.Close(ctx)
	var version int
	var dirty bool
	var catalog int
	must(c.QueryRow(ctx, "SELECT version,dirty FROM schema_migrations").Scan(&version, &dirty))
	must(c.QueryRow(ctx, "SELECT count(*) FROM model_catalog_configs WHERE id=1").Scan(&catalog))
	if version != 119 || dirty || catalog != 1 {
		panic("migration state invalid")
	}
	if production {
		fmt.Println("PRODUCTION_MIGRATION_OK version=119 dirty=false catalog=1")
		return
	}
	db, err := gorm.Open(pgdriver.Open(dsn), &gorm.Config{})
	must(err)
	r := pgrepo.NewPostgresRetrieveEngineRepository(db)
	vector := make([]float32, 1024)
	vector[0] = 1
	row := &types.IndexInfo{SourceID: "upgrade083-probe", ChunkID: "upgrade083-probe", KnowledgeID: "upgrade083-probe",
		KnowledgeBaseID: "upgrade083-probe", Content: "migrationoldtoken", IsEnabled: true}
	params := map[string]any{"embedding": map[string][]float32{row.SourceID: vector}}
	must(r.Save(ctx, row, params))
	row.Content = "migrationnewtoken"
	must(r.Save(ctx, row, params))
	result, err := r.Retrieve(ctx, types.RetrieveParams{RetrieverType: types.KeywordsRetrieverType, Query: "migrationnewtoken", TopK: 3,
		KnowledgeBaseIDs: []string{row.KnowledgeBaseID}})
	must(err)
	if len(result) != 1 || len(result[0].Results) != 1 || result[0].Results[0].Content != row.Content {
		panic("updated keyword not retrieved")
	}
	old, err := r.Retrieve(ctx, types.RetrieveParams{RetrieverType: types.KeywordsRetrieverType, Query: "migrationoldtoken", TopK: 3,
		KnowledgeBaseIDs: []string{row.KnowledgeBaseID}})
	must(err)
	if len(old) != 1 || len(old[0].Results) != 0 {
		panic("stale keyword retained")
	}
	result, err = r.Retrieve(ctx, types.RetrieveParams{RetrieverType: types.VectorRetrieverType, Embedding: vector, TopK: 3,
		KnowledgeBaseIDs: []string{row.KnowledgeBaseID}})
	must(err)
	if len(result) != 1 || len(result[0].Results) != 1 {
		panic("vector retrieval failed")
	}
	row.IsEnabled = false
	must(r.BatchSave(ctx, []*types.IndexInfo{row, row}, params))
	result, err = r.Retrieve(ctx, types.RetrieveParams{RetrieverType: types.KeywordsRetrieverType, Query: "migrationnewtoken", TopK: 3,
		KnowledgeBaseIDs: []string{row.KnowledgeBaseID}})
	must(err)
	if len(result) != 1 || len(result[0].Results) != 0 {
		panic("disabled chunk returned")
	}
	must(r.DeleteByChunkIDList(ctx, []string{row.ChunkID}, 1024, "document"))
	fmt.Printf("MIGRATION_OK database=%s version=%d dirty=%v catalog=%d\n", name, version, dirty, catalog)
	fmt.Println("WRITE_UPDATE_KEYWORD_VECTOR_DISABLE_DELETE_OK no_model_calls=true")
}
