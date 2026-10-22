package service

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/types"
)

var draftTitleDate = regexp.MustCompile(`\d{4}-\d{2}-\d{2}`)

func draftTitleMentioned(query, title string) bool {
	query, title = strings.ToLower(query), strings.ToLower(title)
	if title == "" {
		return false
	}
	if strings.Contains(query, title) {
		return true
	}
	dates := draftTitleDate.FindAllString(query, -1)
	if len(dates) > 0 && draftTitleDate.MatchString(title) {
		matched := false
		for _, date := range dates {
			matched = matched || strings.Contains(title, date)
		}
		if !matched {
			return false
		}
	}
	for _, term := range strings.FieldsFunc(title, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsPunct(r) }) {
		if len([]rune(term)) < 2 {
			continue
		}
		switch term {
		case "wechat", "log", "chat", "微信", "微信聊天记录", "聊天记录", "记录", "文档", "草稿":
			continue
		}
		if strings.IndexFunc(term, unicode.IsLetter) < 0 {
			continue
		}
		if strings.Contains(query, term) {
			return true
		}
	}
	return false
}

// The same scoped list/read tools serve ordinary QA and agent QA; no index writes.
func (s *sessionService) buildKBDocumentContext(ctx context.Context, cm *types.ChatManage) (string, string) {
	if len(cm.SearchTargets) == 0 || s.knowledgeService == nil {
		return "", ""
	}
	ids := map[string]bool{}
	for _, target := range cm.SearchTargets {
		if target != nil {
			ids[target.KnowledgeBaseID] = true
		}
	}
	ordered := make([]string, 0, len(ids))
	for id := range ids {
		ordered = append(ordered, id)
	}
	sort.Strings(ordered)
	list := tools.NewListDocumentsTool(s.knowledgeService, cm.SearchTargets)
	reader := tools.NewReadDocumentTool(s.knowledgeService, s.chunkService, cm.SearchTargets)
	var listing, bodies strings.Builder
	listed, read, scanned := 0, 0, 0
	for _, id := range ordered {
		if scanned >= 1000 {
			break
		}
		// Bound fallback work; agents can continue paging the same list tool.
		for page := 1; page <= 10; page++ {
			rows, total, err := list.ListScoped(ctx, id, "", page, 100)
			if err != nil || len(rows) == 0 {
				break
			}
			for _, k := range rows {
				if scanned >= 1000 {
					break
				}
				scanned++
				if k == nil {
					continue
				}
				draft := k.IsManual() && k.ParseStatus == types.ManualKnowledgeStatusDraft
				if !draft && k.EnableStatus != "enabled" {
					continue
				}
				title := k.Title
				if title == "" {
					title = k.FileName
				}
				if title == "" {
					continue
				}
				if listed < 50 {
					status := ""
					if draft {
						status = " [unpublished draft, not indexed]"
					}
					fmt.Fprintf(&listing, "- %s%s", title, status)
					if k.FileType != "" {
						fmt.Fprintf(&listing, " (%s)", k.FileType)
					}
					if !draft && k.Description != "" {
						description := []rune(k.Description)
						if len(description) > 100 {
							description = append(description[:100], []rune("...")...)
						}
						fmt.Fprintf(&listing, ": %s", string(description))
					}
					listing.WriteString("\n")
					listed++
				}
				if !draft || read >= 3 {
					continue
				}
				mentioned := draftTitleMentioned(cm.Query, title)
				for _, target := range cm.SearchTargets {
					if target == nil || target.Type != types.SearchTargetTypeKnowledge || target.KnowledgeBaseID != id {
						continue
					}
					for _, kid := range target.KnowledgeIDs {
						mentioned = mentioned || kid == k.ID
					}
				}
				if !mentioned {
					continue
				}
				args, _ := json.Marshal(tools.ReadDocumentInput{ID: k.ID, Limit: 4000})
				result, err := reader.Execute(tools.WithOutputBudget(ctx, 6000), args)
				if err == nil && result != nil && result.Success {
					bodies.WriteString(result.Output)
					bodies.WriteString("\n")
					read++
				}
			}
			if int64(page*100) >= total || scanned >= 1000 {
				break
			}
		}
	}
	if listed >= 50 {
		listing.WriteString("... (showing first 50 authorized titles; use list_documents to continue)\n")
	}
	return listing.String(), bodies.String()
}
