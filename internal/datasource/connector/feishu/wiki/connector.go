package wiki

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/datasource"
	"github.com/Tencent/WeKnora/internal/datasource/connector/feishu/core"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
)

// Connector implements the datasource.Connector interface for Feishu and, with
// the same code, for Lark: the two clouds expose an identical wiki/docx/drive
// API surface. A core.Region picks the cloud — see region.go.
type Connector struct {
	region core.Region
}

// NewConnector creates a connector for the given region (RegionFeishu or RegionLark).
func NewConnector(region core.Region) *Connector {
	return &Connector{region: region}
}

// Feishu supports resumable streaming sync; the service prefers FetchStream over
// FetchAll/FetchIncremental when a connector implements StreamingConnector.
var _ datasource.StreamingConnector = (*Connector)(nil)

// Type returns the connector type identifier.
func (c *Connector) Type() string {
	return c.region.ConnectorType
}

// Validate verifies that the Feishu configuration is valid by testing connectivity.
func (c *Connector) Validate(ctx context.Context, config *types.DataSourceConfig) error {
	feishuConfig, err := core.ParseFeishuConfig(config, c.region)
	if err != nil {
		return err
	}

	client := core.NewClient(feishuConfig)
	if err := client.Ping(ctx); err != nil {
		return fmt.Errorf("feishu connection failed: %w", err)
	}

	return nil
}

// ListResources lists Feishu Wiki resources for selection, loading the tree
// lazily one level at a time to avoid traversing the entire wiki up front.
//
//   - parentID == ""        → list all accessible wiki spaces.
//   - parentID == spaceID   → list the top-level nodes of that space.
//   - parentID == "spaceID:nodeToken" → list the direct children of that node.
//
// Eagerly recursing the whole tree here used to time out for large wikis
// (Tencent/WeKnora#1672); the recursive walk now happens only at sync time.
func (c *Connector) ListResources(
	ctx context.Context, config *types.DataSourceConfig, parentID string,
) ([]types.Resource, error) {
	feishuConfig, err := core.ParseFeishuConfig(config, c.region)
	if err != nil {
		return nil, err
	}

	client := core.NewClient(feishuConfig)

	if parentID == "" {
		spaces, err := client.ListWikiSpaces(ctx)
		if err != nil {
			return nil, fmt.Errorf("list feishu wiki spaces: %w", err)
		}

		resources := make([]types.Resource, 0, len(spaces))
		for _, space := range spaces {
			resources = append(resources, types.Resource{
				ExternalID:  space.SpaceID,
				Name:        space.Name,
				Type:        "wiki_space",
				Description: space.Description,
				URL:         c.region.WikiURL(space.SpaceID),
				HasChildren: true,
				Metadata: map[string]interface{}{
					"visibility": space.Visibility,
					"space_id":   space.SpaceID,
				},
			})
		}
		return resources, nil
	}

	// Lazy load: list only the direct children of the given space / node.
	spaceID, nodeToken := parseWikiResourceID(parentID)
	nodes, err := client.ListWikiNodes(ctx, spaceID, nodeToken)
	if err != nil {
		return nil, fmt.Errorf("list feishu wiki nodes under %s: %w", parentID, err)
	}

	resources := make([]types.Resource, 0, len(nodes))
	for _, node := range nodes {
		resources = append(resources, c.wikiNodeToResource(spaceID, node))
	}
	return resources, nil
}

// ResolveResourceAncestors returns the resource IDs of every parent that has to
// be expanded so the lazily-loaded picker can reveal each given selection. For a
// selected node "spaceID:nodeToken" that is its space plus every intermediate
// node up the tree; the walk uses GetWikiNode (parent_node_token) and is O(depth)
// per selection, so it never re-traverses the whole wiki.
func (c *Connector) ResolveResourceAncestors(
	ctx context.Context, config *types.DataSourceConfig, resourceIDs []string,
) ([]string, error) {
	feishuConfig, err := core.ParseFeishuConfig(config, c.region)
	if err != nil {
		return nil, err
	}
	client := core.NewClient(feishuConfig)

	seen := make(map[string]bool)
	ancestors := make([]string, 0)
	add := func(id string) {
		if id != "" && !seen[id] {
			seen[id] = true
			ancestors = append(ancestors, id)
		}
	}

	for _, rid := range resourceIDs {
		spaceID, nodeToken := parseWikiResourceID(rid)
		if spaceID == "" || nodeToken == "" {
			// A space-level selection is already a top-level node in the picker;
			// there is nothing above it to reveal.
			continue
		}
		// The space's direct children must be loaded to reveal the top-level node.
		add(spaceID)

		// Walk up from the selection to the top, loading each intermediate
		// parent so the path down to the selection becomes visible.
		current := nodeToken
		for current != "" {
			node, err := client.GetWikiNode(ctx, spaceID, current)
			if err != nil {
				// Best-effort: a broken path just stays collapsed, the rest of
				// the selections are still revealed.
				logger.Warnf(ctx, "[Feishu] resolve ancestors: get node %s:%s: %v", spaceID, current, err)
				break
			}
			if node.ParentNodeID == "" {
				break
			}
			add(makeWikiNodeResourceID(spaceID, node.ParentNodeID))
			current = node.ParentNodeID
		}
	}

	return ancestors, nil
}

// FetchAll performs a full sync of all documents from the specified wiki spaces.
// Defensive fallback path - the service prefers FetchStream when the connector
// implements StreamingConnector.
func (c *Connector) FetchAll(ctx context.Context, config *types.DataSourceConfig, resourceIDs []string) ([]types.FetchedItem, error) {
	feishuConfig, err := core.ParseFeishuConfig(config, c.region)
	if err != nil {
		return nil, err
	}
	client := core.NewClient(feishuConfig)
	return core.FetchAllEngine(ctx, client, config, resourceIDs, newWikiOps(c.region))
}

// FetchIncremental performs an incremental sync by comparing node edit times
// against the previously recorded state. Defensive fallback path - the service
// prefers FetchStream. Routed through the same engine, so the #2136
// failure-doesn't-advance-cursor semantics apply here too (previously this path
// advanced the cursor before fetching, a latent #2136 bug).
func (c *Connector) FetchIncremental(ctx context.Context, config *types.DataSourceConfig, cursor *types.SyncCursor) ([]types.FetchedItem, *types.SyncCursor, error) {
	feishuConfig, err := core.ParseFeishuConfig(config, c.region)
	if err != nil {
		return nil, nil, err
	}
	client := core.NewClient(feishuConfig)
	ops := newWikiOps(c.region)
	if len(config.ResourceIDs) == 0 {
		return nil, nil, errors.New(ops.EmptyResourceIDsError())
	}
	return core.FetchIncrementalEngine(ctx, client, config, cursor, ops)
}

// FetchStream performs a resumable, memory-bounded sync. It unifies the full
// and incremental paths: with cursor == nil it fetches everything, and with a
// cursor it skips nodes whose recorded edit time is unchanged - the same
// mechanism that lets a sync which timed out mid-traversal resume from the last
// checkpoint instead of restarting (Tencent/WeKnora#2136).
//
// The per-node loop lives in the shared engine (engine.go); this shell only
// wires the wiki NodeOps adapter.
func (c *Connector) FetchStream(
	ctx context.Context, config *types.DataSourceConfig,
	cursor *types.SyncCursor, h datasource.StreamHandler,
) (*types.SyncCursor, error) {
	feishuConfig, err := core.ParseFeishuConfig(config, c.region)
	if err != nil {
		return nil, err
	}
	client := core.NewClient(feishuConfig)
	ops := newWikiOps(c.region)
	if len(config.ResourceIDs) == 0 {
		return nil, errors.New(ops.EmptyResourceIDsError())
	}
	return core.FetchStreamEngine(ctx, client, config, cursor, h, ops)
}

// wikiOps adapts the wiki Connector to the generic sync engine. It carries the
// region (for URL rendering) and encodes/decodes the wiki cursor wire format
// (core.FeishuCursor / space_node_times) so the engine can stay format-agnostic.
type wikiOps struct {
	region         core.Region
	recursiveState *core.RecursiveState
}

func newWikiOps(region core.Region) wikiOps {
	return wikiOps{
		region:         region,
		recursiveState: core.NewRecursiveState(),
	}
}

func (o wikiOps) List(ctx context.Context, client *core.Client, resourceID string) ([]core.WikiNode, error, error) {
	spaceID, nodeToken := parseWikiResourceID(resourceID)
	nodes, err := client.ListWikiNodesRecursiveFrom(ctx, spaceID, nodeToken)
	if err == nil {
		return nodes, nil, nil
	}
	var partial *core.PartialWikiNodeListError
	if errors.As(err, &partial) {
		// Partial listing: nodes are still usable; the failed sub-trees are
		// surfaced via ListFailureItems, and the sync continues.
		return nodes, err, nil
	}
	return nodes, nil, err
}

func (o wikiOps) Token(n core.WikiNode) string   { return n.NodeToken }
func (o wikiOps) Title(n core.WikiNode) string   { return n.Title }
func (o wikiOps) ObjType(n core.WikiNode) string { return n.ObjType }

// EditTime is the change-detection timestamp: ObjEditTime (document content)
// with a NodeEditTime fallback for nodes that lack obj_edit_time. This drives
// the cursor comparison, NOT FetchedItem.UpdatedAt (which uses NodeEditTime).
func (o wikiOps) EditTime(n core.WikiNode) string {
	if n.ObjEditTime != "" {
		return n.ObjEditTime
	}
	return n.NodeEditTime
}

func (o wikiOps) Fetch(ctx context.Context, client *core.Client, n core.WikiNode, resourceID string, multimodal bool) ([]*types.FetchedItem, error) {
	spaceID, _ := parseWikiResourceID(resourceID)

	items, err := fetchNodeContent(
		ctx,
		client,
		n,
		spaceID,
		resourceID,
		multimodal,
		o.region,
	)
	if err != nil {
		return nil, err
	}

	if !core.RecursiveLinksEnabled() {
		return items, nil
	}

	if n.ObjType != "bitable" && n.ObjType != "sheet" {
		return items, nil
	}

	container := core.LinkedResource{
		Type:  n.ObjType,
		Token: n.ObjToken,
		URL:   o.region.WikiURL(n.NodeToken),
	}

	children, derr := core.DiscoverDirectLinkedResources(
		ctx,
		client,
		container,
	)
	if derr != nil {
		if o.recursiveState != nil {
			o.recursiveState.MarkIncomplete()
		}

		logger.Warnf(
			ctx,
			"[FeishuRecursive] discovery failed for wiki node=%s type=%s: %v",
			n.NodeToken,
			n.ObjType,
			derr,
		)

		items = append(items, &types.FetchedItem{
			ExternalID:       n.NodeToken + "#recursive-discovery-error",
			Title:            n.Title,
			SourceResourceID: resourceID,
			Metadata: core.FeishuErrorItemMeta(
				derr,
				map[string]string{
					"channel":       types.ChannelFeishu,
					"recursive":     "true",
					"node_token":    n.NodeToken,
					"obj_token":     n.ObjToken,
					"obj_type":      n.ObjType,
					"failure_stage": "recursive_discovery",
				},
			),
		})

		return items, nil
	}

	if len(children) == 0 {
		return items, nil
	}

	recursiveItems, rerr := core.FetchRecursiveLinkedResources(
		ctx,
		client,
		children,
		core.RecursiveFetchOptions{
			SourceResourceID: resourceID,
			ParentExternalID: n.NodeToken,
			Channel:          types.ChannelFeishu,
			Multimodal:       multimodal,
			MaxDepth:         core.RecursiveMaxDepth(),
			State:            o.recursiveState,
			SeedVisited: []core.LinkedResource{
				container,
			},
		},
	)
	if rerr != nil {
		return nil, rerr
	}

	items = append(items, recursiveItems...)

	logger.Infof(
		ctx,
		"[FeishuRecursive] wiki node=%s type=%s discovered=%d emitted=%d",
		n.NodeToken,
		n.ObjType,
		len(children),
		len(recursiveItems),
	)

	return items, nil
}

// RefreshUnchanged 在父 Wiki 节点本身未变化时，仅扫描它引用的
// Bitable/Sheet 子资源。父资源本身不会再次 Emit，因此不会因为递归检查
// 导致一级表每次增量同步都被删除重建。
func (o wikiOps) RefreshUnchanged(
	ctx context.Context,
	client *core.Client,
	n core.WikiNode,
	resourceID string,
	multimodal bool,
) ([]*types.FetchedItem, error) {
	if !core.RecursiveLinksEnabled() {
		return nil, nil
	}

	if n.ObjType != "bitable" && n.ObjType != "sheet" {
		return nil, nil
	}

	container := core.LinkedResource{
		Type:  n.ObjType,
		Token: n.ObjToken,
		URL:   o.region.WikiURL(n.NodeToken),
	}

	children, err := core.DiscoverDirectLinkedResources(
		ctx,
		client,
		container,
	)
	if err != nil {
		if o.recursiveState != nil {
			o.recursiveState.MarkIncomplete()
		}

		logger.Warnf(
			ctx,
			"[FeishuRecursive] unchanged parent discovery failed node=%s type=%s: %v",
			n.NodeToken,
			n.ObjType,
			err,
		)

		return []*types.FetchedItem{
			{
				ExternalID:       n.NodeToken + "#recursive-discovery-error",
				Title:            n.Title,
				SourceResourceID: resourceID,
				Metadata: core.FeishuErrorItemMeta(
					err,
					map[string]string{
						"channel":       types.ChannelFeishu,
						"recursive":     "true",
						"node_token":    n.NodeToken,
						"obj_token":     n.ObjToken,
						"obj_type":      n.ObjType,
						"failure_stage": "recursive_discovery",
					},
				),
			},
		}, nil
	}

	if len(children) == 0 {
		return nil, nil
	}

	items, err := core.FetchRecursiveLinkedResources(
		ctx,
		client,
		children,
		core.RecursiveFetchOptions{
			SourceResourceID: resourceID,
			ParentExternalID: n.NodeToken,
			Channel:          types.ChannelFeishu,
			Multimodal:       multimodal,
			MaxDepth:         core.RecursiveMaxDepth(),
			State:            o.recursiveState,
			SeedVisited: []core.LinkedResource{
				container,
			},
		},
	)
	if err != nil {
		return nil, err
	}

	logger.Infof(
		ctx,
		"[FeishuRecursive] unchanged wiki parent node=%s type=%s discovered=%d emitted=%d",
		n.NodeToken,
		n.ObjType,
		len(children),
		len(items),
	)

	return items, nil
}

func (o wikiOps) ListFailureItems(resourceID string, partial error) []types.FetchedItem {
	spaceID, _ := parseWikiResourceID(resourceID)
	var pe *core.PartialWikiNodeListError
	if errors.As(partial, &pe) {
		return appendWikiNodeListFailureItems(nil, spaceID, resourceID, pe.Failures)
	}
	return nil
}

func (o wikiOps) RecursiveSyncState() *core.RecursiveState {
	return o.recursiveState
}

func (o wikiOps) ResourceNoun() string { return "nodes" }
func (o wikiOps) EmptyResourceIDsError() string {
	return "no resource IDs (wiki space IDs or wiki node IDs) configured"
}
func (o wikiOps) LogTag() string { return "[Feishu]" }

func (o wikiOps) DecodeCursorTimes(m map[string]interface{}) map[string]map[string]string {
	var prev core.FeishuCursor
	b, _ := json.Marshal(m)
	_ = json.Unmarshal(b, &prev)

	if o.recursiveState != nil {
		o.recursiveState.LoadPrevious(prev.RecursiveExternalIDs)
	}

	return prev.SpaceNodeTimes
}

func (o wikiOps) EncodeCursor(times map[string]map[string]string, lastSync time.Time) *types.SyncCursor {
	var recursiveIDs []string
	if o.recursiveState != nil {
		recursiveIDs = o.recursiveState.CursorIDs()
	}

	fc := core.FeishuCursor{
		LastSyncTime:         lastSync,
		SpaceNodeTimes:       times,
		RecursiveExternalIDs: recursiveIDs,
	}

	m := make(map[string]interface{})
	b, _ := json.Marshal(fc)
	_ = json.Unmarshal(b, &m)
	return &types.SyncCursor{LastSyncTime: lastSync, ConnectorCursor: m}
}

func appendWikiNodeListFailureItems(items []types.FetchedItem, spaceID string, resourceID string, failures []core.WikiNodeListFailure) []types.FetchedItem {
	for _, failure := range failures {
		node := failure.Node
		title := node.Title
		if title == "" {
			title = node.NodeToken
		}
		items = append(items, types.FetchedItem{
			ExternalID:       node.NodeToken,
			Title:            title,
			SourceResourceID: resourceID,
			Metadata: core.FeishuErrorItemMeta(failure.Err, map[string]string{
				"channel":       types.ChannelFeishu,
				"node_token":    node.NodeToken,
				"space_id":      spaceID,
				"failure_stage": "list_children",
			}),
		})
	}
	return items
}

// fetchNodeContent fetches the content of a single wiki node and converts it to a
// slice of FetchedItems. For docx nodes it fans out into a main Markdown document
// plus optional attachment sub-items. Dispatches to different retrieval strategies
// based on obj_type:
//   - docx       → blocks API (Markdown) with export fallback; may return attachments
//   - doc/sheet/bitable → export API → binary file
//   - file       → drive download → original file (PDF/Word/image/etc.)
//   - mindnote   → Skip (no API)
//   - slides     → Skip (no API)
func fetchNodeContent(ctx context.Context, client *core.Client, node core.WikiNode, spaceID string, resourceID string, multimodalEnabled bool, region core.Region) ([]*types.FetchedItem, error) {
	if !core.IsSupportedDocType(node.ObjType) {
		return nil, nil
	}

	editTime := core.ParseFeishuTimestamp(node.NodeEditTime)
	baseMeta := map[string]string{
		"obj_token":  node.ObjToken,
		"obj_type":   node.ObjType,
		"node_token": node.NodeToken,
		"space_id":   spaceID,
		"creator":    node.Creator,
		"owner":      node.Owner,
		"channel":    types.ChannelFeishu,
	}

	switch node.ObjType {
	case "docx":
		return core.FetchDocxWithBlocks(ctx, client, core.DocxFetchInput{
			DocToken:          node.NodeToken,
			ObjToken:          node.ObjToken,
			Title:             node.Title,
			URL:               region.WikiURL(node.NodeToken),
			ResourceID:        resourceID,
			EditTime:          editTime,
			BaseMeta:          baseMeta,
			MultimodalEnabled: multimodalEnabled,
		})
	case "doc", "sheet", "bitable":
		item, err := fetchViaExport(ctx, client, node, resourceID, editTime, baseMeta, region)
		if err != nil {
			return nil, err
		}
		return []*types.FetchedItem{item}, nil
	case "file":
		item, err := fetchDriveFile(ctx, client, node, resourceID, editTime, baseMeta, region)
		if err != nil {
			return nil, err
		}
		return []*types.FetchedItem{item}, nil
	default:
		return nil, nil
	}
}

// fetchViaExport exports a doc/sheet/bitable node via the async export API and
// returns a single FetchedItem containing the exported binary.
func fetchViaExport(ctx context.Context, client *core.Client, node core.WikiNode, resourceID string, editTime time.Time, baseMeta map[string]string, region core.Region) (*types.FetchedItem, error) {
	// Export as a file via the async export API
	data, fileName, err := client.ExportAndDownload(ctx, node.ObjToken, node.ObjType)
	if err != nil {
		return nil, fmt.Errorf("export %s (%s): %w", node.Title, node.ObjType, err)
	}

	// Ensure a reasonable file name with correct extension
	ext := core.ExportFileExtToSuffix[core.ObjTypeToExportFileExtension[node.ObjType]]
	if fileName == "" {
		fileName = core.SanitizeFileName(node.Title) + ext
	} else if !strings.HasSuffix(strings.ToLower(fileName), ext) {
		// Feishu often returns the doc title without extension - append it
		fileName = core.SanitizeFileName(fileName) + ext
	}

	return &types.FetchedItem{
		ExternalID:       node.NodeToken,
		Title:            node.Title,
		Content:          data,
		ContentType:      "application/octet-stream",
		FileName:         fileName,
		URL:              region.WikiURL(node.NodeToken),
		UpdatedAt:        editTime,
		SourceResourceID: resourceID,
		Metadata:         baseMeta,
	}, nil
}

// fetchDriveFile downloads an original uploaded file from Drive and returns a
// single FetchedItem containing the raw bytes.
func fetchDriveFile(ctx context.Context, client *core.Client, node core.WikiNode, resourceID string, editTime time.Time, baseMeta map[string]string, region core.Region) (*types.FetchedItem, error) {
	// Download the original uploaded file from Drive
	data, err := client.DownloadDriveFile(ctx, node.ObjToken)
	if err != nil {
		return nil, fmt.Errorf("download file %s (%s): %w", node.Title, node.ObjToken, err)
	}

	// Use the node title as file name; it usually preserves the original extension
	fileName := node.Title
	if fileName == "" {
		fileName = node.ObjToken
	}

	return &types.FetchedItem{
		ExternalID:       node.NodeToken,
		Title:            node.Title,
		Content:          data,
		ContentType:      "application/octet-stream",
		FileName:         fileName,
		URL:              region.WikiURL(node.NodeToken),
		UpdatedAt:        editTime,
		SourceResourceID: resourceID,
		Metadata:         baseMeta,
	}, nil
}

// --- Helper functions ---

func makeWikiNodeResourceID(spaceID, nodeToken string) string {
	return spaceID + core.FeishuWikiNodeResourceSeparator + nodeToken
}

func parseWikiResourceID(resourceID string) (spaceID string, nodeToken string) {
	spaceID, nodeToken, _ = strings.Cut(resourceID, core.FeishuWikiNodeResourceSeparator)
	return spaceID, nodeToken
}

func (c *Connector) wikiNodeToResource(spaceID string, node core.WikiNode) types.Resource {
	parentID := spaceID
	if node.ParentNodeID != "" {
		parentID = makeWikiNodeResourceID(spaceID, node.ParentNodeID)
	}

	name := node.Title
	if name == "" {
		name = node.NodeToken
	}

	modifiedAt := core.ParseFeishuTimestamp(node.ObjEditTime)
	if modifiedAt.IsZero() {
		modifiedAt = core.ParseFeishuTimestamp(node.NodeEditTime)
	}

	return types.Resource{
		ExternalID:  makeWikiNodeResourceID(spaceID, node.NodeToken),
		Name:        name,
		Type:        "wiki_node",
		URL:         c.region.WikiURL(node.NodeToken),
		ParentID:    parentID,
		HasChildren: node.HasChild,
		ModifiedAt:  modifiedAt,
		Metadata: map[string]interface{}{
			"space_id":   spaceID,
			"node_token": node.NodeToken,
			"obj_token":  node.ObjToken,
			"obj_type":   node.ObjType,
		},
	}
}
