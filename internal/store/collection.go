package store

import (
	"fmt"
	"strings"
	"time"

	"tidegate/internal/scenario"
)

// CollectionTarget describes one ordered API step collected for a batch functional run.
type CollectionTarget struct {
	StepID     string `json:"step_id"`
	APIID      string `json:"api_id"`
	APIName    string `json:"api_name"`
	FolderPath string `json:"folder_path"`
	Protocol   string `json:"protocol"`
	Method     string `json:"method"`
}

// CollectionPlan holds the ordered targets and transient scenario used by batch test runners.
type CollectionPlan struct {
	ScopeName string
	FolderID  string
	Targets   []CollectionTarget
	Scenario  *Scenario
}

// BuildCollectionTestPlan walks the project folder tree in DFS order and constructs
// a transient scenario covering all APIs under folderRef (matching ID, slash path,
// or exact name) or explicit apiIDs.
func (s *Store) BuildCollectionTestPlan(projectID, folderRef string, apiIDs []string) (*CollectionPlan, error) {
	pr, err := s.GetProject(projectID)
	if err != nil {
		return nil, err
	}
	tree, err := s.Tree(projectID)
	if err != nil {
		return nil, err
	}

	filterIDs := make(map[string]struct{}, len(apiIDs))
	orderedIDs := make([]string, 0, len(apiIDs))
	for _, raw := range apiIDs {
		id := strings.TrimSpace(raw)
		if id == "" {
			continue
		}
		if _, seen := filterIDs[id]; !seen {
			filterIDs[id] = struct{}{}
			orderedIDs = append(orderedIDs, id)
		}
	}

	rootNodes := tree.Children
	scopeName := pr.Name
	resolvedFolderID := ""
	basePrefix := ""

	folderRef = strings.TrimSpace(folderRef)
	if folderRef != "" {
		found, folderPath, ok := findFolderInTree(tree.Children, folderRef, "")
		if !ok {
			return nil, &NotFoundError{Kind: KindFolder, ID: folderRef}
		}
		rootNodes = found.Children
		scopeName = found.Name
		resolvedFolderID = found.ID
		basePrefix = folderPath
	}

	type rawNode struct {
		node       *TreeNode
		folderPath string
	}
	var collected []rawNode
	byID := make(map[string]rawNode)

	var walk func(nodes []*TreeNode, prefix string)
	walk = func(nodes []*TreeNode, prefix string) {
		for _, n := range nodes {
			if n == nil {
				continue
			}
			switch n.Type {
			case "folder":
				nextPrefix := n.Name
				if prefix != "" {
					nextPrefix = prefix + "/" + n.Name
				}
				walk(n.Children, nextPrefix)
			case "api":
				rn := rawNode{node: n, folderPath: prefix}
				byID[n.ID] = rn
				if len(filterIDs) == 0 {
					collected = append(collected, rn)
				}
			}
		}
	}
	walk(rootNodes, basePrefix)

	if len(filterIDs) > 0 {
		for _, id := range orderedIDs {
			rn, ok := byID[id]
			if !ok {
				return nil, invalid("api_ids", "接口 %q 不在当前项目或所选目录中", id)
			}
			collected = append(collected, rn)
		}
		if folderRef == "" {
			scopeName = fmt.Sprintf("%s (%d 个接口)", pr.Name, len(collected))
		}
	}

	if len(collected) == 0 {
		return nil, invalid("apis", "所选范围没有可执行的接口")
	}

	targets := make([]CollectionTarget, 0, len(collected))
	steps := make([]ScenarioStep, 0, len(collected))
	for i, item := range collected {
		apiObj, err := s.GetAPI(projectID, item.node.ID)
		if err != nil {
			return nil, err
		}
		proto := strings.ToLower(strings.TrimSpace(apiObj.Request.Protocol))
		if proto == "" {
			proto = "http"
		}
		method := strings.ToUpper(strings.TrimSpace(apiObj.Request.Method))
		if method == "" {
			method = "GET"
		}
		stepID := fmt.Sprintf("step_%d", i+1)
		targets = append(targets, CollectionTarget{
			StepID:     stepID,
			APIID:      apiObj.ID,
			APIName:    apiObj.Name,
			FolderPath: item.folderPath,
			Protocol:   proto,
			Method:     method,
		})
		steps = append(steps, ScenarioStep{
			ID:    stepID,
			Name:  apiObj.Name,
			Type:  proto,
			APIID: apiObj.ID,
		})
	}

	return &CollectionPlan{
		ScopeName: scopeName,
		FolderID:  resolvedFolderID,
		Targets:   targets,
		Scenario: &Scenario{
			Name:  "批量测试：" + scopeName,
			Steps: steps,
			Load: scenario.Load{
				Mode: "vus",
				Stages: []scenario.Stage{{
					Duration: scenario.Duration(time.Second),
					Target:   1,
				}},
				MaxRequests: int64(len(steps)),
			},
		},
	}, nil
}

func findFolderInTree(nodes []*TreeNode, ref, prefix string) (*TreeNode, string, bool) {
	var firstNameMatch *TreeNode
	var firstNamePath string
	for _, n := range nodes {
		if n == nil || n.Type != "folder" {
			continue
		}
		path := n.Name
		if prefix != "" {
			path = prefix + "/" + n.Name
		}
		if n.ID == ref || path == ref {
			return n, path, true
		}
		if firstNameMatch == nil && strings.EqualFold(n.Name, ref) {
			firstNameMatch = n
			firstNamePath = path
		}
		if child, childPath, ok := findFolderInTree(n.Children, ref, path); ok {
			return child, childPath, true
		}
	}
	if firstNameMatch != nil {
		return firstNameMatch, firstNamePath, true
	}
	return nil, "", false
}
