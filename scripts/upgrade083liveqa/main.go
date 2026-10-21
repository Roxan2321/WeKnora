package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

func fail(err error) {
	if err != nil {
		panic(err)
	}
}
func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if _, err := os.Stat("/var/www/weknora-maintenance/enabled"); err != nil {
		panic("maintenance required")
	}
	pid, err := exec.Command("systemctl", "show", "-p", "MainPID", "--value", "weknora.service").Output()
	fail(err)
	active, err := os.ReadFile("/proc/" + strings.TrimSpace(string(pid)) + "/environ")
	fail(err)
	if !bytes.Contains(active, []byte("DB_HOST=pgm-uf6g5my29491x4fx3o.pg.rds.aliyuncs.com\x00")) || !bytes.Contains(active, []byte("DB_NAME=weknora\x00")) {
		panic("backend is not connected to the intended RDS")
	}
	for _, expected := range []string{"AUTO_MIGRATE=false", "DOCREADER_ADDR=127.0.0.1:50052", "KB_MODEL_POLICY=company", "POSTGRES_SEARCH_EXTENSION=pg_textsearch", "DB_SSLMODE=verify-full", "FEISHU_RECURSIVE_LINKS=true", "FEISHU_RECURSIVE_MAX_DEPTH=3"} {
		if !bytes.Contains(active, []byte(expected+"\x00")) {
			panic("runtime setting missing: " + expected)
		}
	}
	u := url.URL{Scheme: "postgres", Host: "pgm-uf6g5my29491x4fx3o.pg.rds.aliyuncs.com:5432", Path: "/weknora", User: url.UserPassword("zrx", os.Getenv("TARGET_DB_PASSWORD"))}
	u.RawQuery = "sslmode=verify-full&sslrootcert=" + url.QueryEscape("/opt/weknora/certs/ApsaraDB-CA-Chain.pem")
	db, err := pgx.Connect(ctx, u.String())
	fail(err)
	defer db.Close(ctx)
	var kbID, embeddingModel string
	var tenant int64
	fail(db.QueryRow(ctx, `SELECT k.id,k.tenant_id,k.embedding_model_id FROM knowledge_bases k JOIN embeddings e ON e.knowledge_base_id=k.id WHERE k.deleted_at IS NULL AND e.embedding IS NOT NULL AND e.dimension=1024 AND e.is_enabled=true GROUP BY k.id,k.tenant_id,k.embedding_model_id ORDER BY count(*) DESC LIMIT 1`).Scan(&kbID, &tenant, &embeddingModel))
	raw := make([]byte, 16)
	_, err = rand.Read(raw)
	fail(err)
	nonce := hex.EncodeToString(raw)
	uid := nonce[:8] + "-" + nonce[8:12] + "-" + nonce[12:16] + "-" + nonce[16:20] + "-" + nonce[20:]
	password := "MigrationQA-" + nonce
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	fail(err)
	email := "migration-qa-" + nonce + "@example.invalid"
	name := "migration_qa_" + nonce
	_, err = db.Exec(ctx, `INSERT INTO users(id,username,email,password_hash,tenant_id,is_active,can_access_all_tenants,is_system_admin,preferences,created_at,updated_at) VALUES($1,$2,$3,$4,$5,true,false,false,'{}',now(),now());`, uid, name, email, string(hash), tenant)
	fail(err)
	var createdKB string
	defer func() {
		clean := context.Background()
		_, err := db.Exec(clean, "DELETE FROM knowledge_bases WHERE creator_id=$1 AND name=$2", uid, name)
		fail(err)
		_, err = db.Exec(clean, "DELETE FROM auth_tokens WHERE user_id=$1", uid)
		fail(err)
		_, err = db.Exec(clean, "DELETE FROM audit_logs WHERE user_id=$1", uid)
		if err != nil {
			fmt.Println("QA audit cleanup skipped: schema field differs")
		}
		_, err = db.Exec(clean, "DELETE FROM tenant_members WHERE user_id=$1", uid)
		fail(err)
		_, err = db.Exec(clean, "DELETE FROM users WHERE id=$1 AND email=$2", uid, email)
		fail(err)
		fmt.Println("QA synthetic user and empty knowledge base cleaned")
	}()
	_, err = db.Exec(ctx, `INSERT INTO tenant_members(user_id,tenant_id,role,status,joined_at,created_at,updated_at) VALUES($1,$2,'contributor','active',now(),now(),now())`, uid, tenant)
	fail(err)
	client := &http.Client{Timeout: 15 * time.Second}
	expectedStatus := 0
	request := func(method, path, token string, payload any) map[string]any {
		var body io.Reader
		if payload != nil {
			b, err := json.Marshal(payload)
			fail(err)
			body = bytes.NewReader(b)
		}
		r, err := http.NewRequestWithContext(ctx, method, "http://127.0.0.1:8080/api/v1"+path, body)
		fail(err)
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		response, err := client.Do(r)
		fail(err)
		defer response.Body.Close()
		var result map[string]any
		fail(json.NewDecoder(response.Body).Decode(&result))
		if expectedStatus != 0 && response.StatusCode != expectedStatus || expectedStatus == 0 && (response.StatusCode < 200 || response.StatusCode >= 300) {
			panic(fmt.Sprintf("QA %s %s failed status=%d", method, path, response.StatusCode))
		}
		fmt.Printf("QA_API %s %s status=%d\n", method, path, response.StatusCode)
		return result
	}
	login := request("POST", "/auth/login", "", map[string]any{"email": email, "password": password})
	token, ok := login["token"].(string)
	if !ok || token == "" {
		panic("login token missing")
	}
	request("GET", "/auth/me", token, nil)
	info := request("GET", "/system/info", token, nil)
	if body, ok := info["data"].(map[string]any); !ok || body["version"] != "v0.8.3-company" {
		panic("wrong served version")
	}
	request("GET", "/knowledge-bases", token, nil)
	request("GET", "/knowledge-bases/"+kbID, token, nil)
	request("GET", "/organizations", token, nil)
	created := request("POST", "/knowledge-bases", token, map[string]any{"name": name, "type": "document", "embedding_model_id": embeddingModel})
	data, ok := created["data"].(map[string]any)
	if !ok {
		panic("KB create response missing data")
	}
	createdKB, ok = data["id"].(string)
	if !ok || createdKB == "" {
		panic("KB create ID missing")
	}
	request("GET", "/knowledge-bases/"+createdKB, token, nil)
	var bindingsOK, questionsOff bool
	fail(db.QueryRow(ctx, `SELECT embedding_model_id='builtin-company-embedding' AND summary_model_id='builtin-company-summary' AND vlm_config->>'model_id'='builtin-company-vision', COALESCE((question_generation_config->>'enabled')::boolean,false)=false FROM knowledge_bases WHERE id=$1`, createdKB).Scan(&bindingsOK, &questionsOff))
	if !bindingsOK || !questionsOff {
		panic("company defaults not preserved")
	}
	expectedStatus = 403
	request("PUT", "/knowledge-bases/"+createdKB, token, map[string]any{"name": name, "vlm_config": map[string]any{"enabled": true, "model_id": "not-company"}})
	expectedStatus = 0
	request("PUT", "/knowledge-bases/"+createdKB, token, map[string]any{"name": name, "config": map[string]any{}})
	fmt.Println("QA company bindings, question defaults, override denial and nullable config passed")
	matched := false
	for _, word := range []string{"仓", "客户", "部门", "产品", "项目", "人事"} {
		result := request("POST", "/knowledge-bases/"+kbID+"/hybrid-search", token, map[string]any{"query_text": word, "disable_vector_match": true, "match_count": 3})
		if rows, ok := result["data"].([]any); ok && len(rows) > 0 {
			matched = true
			fmt.Printf("QA keyword retrieval positive matches=%d\n", len(rows))
			break
		}
	}
	if !matched {
		panic("no keyword matches across sample terms")
	}
	var vectorText string
	fail(db.QueryRow(ctx, "SELECT embedding::text FROM embeddings WHERE knowledge_base_id=$1 AND dimension=1024 AND is_enabled=true LIMIT 1", kbID).Scan(&vectorText))
	var vector []float32
	fail(json.Unmarshal([]byte(vectorText), &vector))
	if len(vector) != 1024 {
		panic("wrong vector dimension")
	}
	result := request("POST", "/knowledge-bases/"+kbID+"/hybrid-search", token, map[string]any{"query_text": "", "query_embedding": vector, "disable_keywords_match": true, "disable_vector_match": false, "match_count": 3})
	if rows, ok := result["data"].([]any); !ok || len(rows) == 0 {
		panic("vector retrieval empty")
	}
	fmt.Println("QA login, authenticated reads, knowledge-base write, shared spaces, keywords and precomputed vector retrieval passed; no model called")
}
