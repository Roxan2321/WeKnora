package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCompanyRecursiveDepthAndFailurePreservation(t *testing.T) {
	denied := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := map[string]any{"code": 0}
		switch {
		case strings.Contains(r.URL.Path, "/auth/"):
			response["tenant_access_token"] = "fixture-only"
			response["expire"] = 7200
		case strings.HasSuffix(r.URL.Path, "/tables"):
			if denied {
				response["code"] = 99991672
				response["msg"] = "permission denied"
			} else {
				response["data"] = map[string]any{"items": []map[string]string{{"table_id": "table-a"}}, "has_more": false}
			}
		case strings.HasSuffix(r.URL.Path, "/records/search"):
			target := "base-b"
			if strings.Contains(r.URL.Path, "base-b") {
				target = "base-c"
			}
			if strings.Contains(r.URL.Path, "base-c") {
				target = "base-a"
			}
			response["data"] = map[string]any{"items": []any{map[string]any{"fields": map[string]any{
				"link": "https://example.feishu.cn/base/" + target,
			}}}, "has_more": false}
		default:
			response["code"] = 106999
			response["msg"] = "fixture export unavailable"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()
	client := &Client{baseURL: server.URL, appID: "fixture", appSecret: "fixture", httpClient: server.Client()}
	root := LinkedResource{Type: "bitable", Token: "base-a", URL: "https://example.feishu.cn/base/base-a"}
	for _, depth := range []int{1, 2, 3} {
		state := NewRecursiveState()
		_, err := FetchRecursiveLinkedResources(context.Background(), client, []LinkedResource{root}, RecursiveFetchOptions{MaxDepth: depth, State: state})
		require.NoError(t, err)
		require.Len(t, state.Present, depth)
	}
	denied = true
	state := NewRecursiveState()
	state.LoadPrevious([]string{"recursive:docx:keep-me"})
	_, err := FetchRecursiveLinkedResources(context.Background(), client, []LinkedResource{root}, RecursiveFetchOptions{MaxDepth: 3, State: state})
	require.NoError(t, err)
	require.True(t, state.Incomplete)
	state.PreservePreviousAndPresent()
	require.Contains(t, state.CursorIDs(), "recursive:docx:keep-me")
}
