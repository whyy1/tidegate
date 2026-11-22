// Package importer parses external API specifications, HTTP archives, cURL
// commands, and .tidegate project packages, providing two-step dry-run preview
// diffs before applying changes to store projects.
package importer

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"

	"gopkg.in/yaml.v3"

	"tidegate/internal/curl"
	"tidegate/internal/store"
)

// Supported import formats.
const (
	FormatAuto     = "auto"
	FormatOpenAPI3 = "openapi3"
	FormatSwagger2 = "swagger2"
	FormatPostman  = "postman"
	FormatHAR      = "har"
	FormatCurl     = "curl"
	FormatTidegate = "tidegate"
	FormatJMeter   = "jmeter"
	FormatApifox   = "apifox"
)

// Conflict resolution strategies for Apply.
const (
	StrategyMerge     = "merge"
	StrategyOverwrite = "overwrite"
	StrategySkip      = "skip"
)

// Diff actions reported by Preview and Apply.
const (
	ActionCreate = "create"
	ActionUpdate = "update"
	ActionSkip   = "skip"
)

// PreviewInput configures Preview and Apply.
type PreviewInput struct {
	Format         string
	Filename       string
	Content        []byte
	Strategy       string
	TargetFolderID string
}

// ConversionReport summarizes recognized resources, applied counts, and any
// skipped or downgraded items from an import operation.
type ConversionReport struct {
	Format           string   `json:"format"`
	DetectedVersion  string   `json:"detected_version,omitempty"`
	APIsFound        int      `json:"apis_found"`
	EnvsFound        int      `json:"envs_found,omitempty"`
	FoldersFound     int      `json:"folders_found,omitempty"`
	ScenariosFound   int      `json:"scenarios_found,omitempty"`
	FilesFound       int      `json:"files_found,omitempty"`
	APIsCreated      int      `json:"apis_created,omitempty"`
	APIsUpdated      int      `json:"apis_updated,omitempty"`
	APIsSkipped      int      `json:"apis_skipped,omitempty"`
	FoldersCreated   int      `json:"folders_created,omitempty"`
	EnvsCreated      int      `json:"envs_created,omitempty"`
	ScenariosCreated int      `json:"scenarios_created,omitempty"`
	FilesImported    int      `json:"files_imported,omitempty"`
	Warnings         []string `json:"warnings,omitempty"`
	Skipped          []string `json:"skipped,omitempty"`
}

// ImportDiff describes the planned or executed action for one API entry.
type ImportDiff struct {
	Action     string `json:"action"` // create | update | skip
	FolderPath string `json:"folder_path,omitempty"`
	Method     string `json:"method"`
	URL        string `json:"url"`
	Name       string `json:"name"`
	ExistingID string `json:"existing_id,omitempty"`
}

// PreviewResult is returned by Preview without mutating the store.
type PreviewResult struct {
	Report *ConversionReport `json:"report"`
	Diffs  []ImportDiff      `json:"diffs"`
}

// ApplyResult is returned by Apply after writing imported items to the store.
type ApplyResult struct {
	Report *ConversionReport `json:"report"`
	Diffs  []ImportDiff      `json:"diffs"`
}

type parsedEnv struct {
	OldID string
	Env   store.Environment
}

type parsedFolder struct {
	OldID   string
	Path    []string
	Order   int64
	Headers store.KeyValueList
	Auth    *store.Auth
}

type parsedAPI struct {
	OldID      string
	FolderPath []string
	API        store.API
}

type parsedScenario struct {
	Scenario store.Scenario
}

type parsedFile struct {
	Name    string
	Content []byte
}

type parsedBundle struct {
	ProjectName string
	ProjectDesc string
	ProjectVars store.KeyValueList
	Envs        []parsedEnv
	Folders     []parsedFolder
	APIs        []parsedAPI
	Scenarios   []parsedScenario
	Files       []parsedFile
	Report      *ConversionReport
}

// DetectFormat inspects file magic bytes, extension, and top-level content keys
// to determine the import format.
func DetectFormat(content []byte, filename string) (string, error) {
	if len(content) >= 4 && content[0] == 'P' && content[1] == 'K' && content[2] == 0x03 && content[3] == 0x04 {
		return FormatTidegate, nil
	}
	trimmed := strings.TrimSpace(strings.TrimPrefix(string(content), "\xef\xbb\xbf"))
	if trimmed == "" {
		return "", errors.New("导入内容为空")
	}

	firstLine := trimmed
	if idx := strings.IndexByte(firstLine, '\n'); idx >= 0 {
		firstLine = strings.TrimSpace(firstLine[:idx])
	}
	for _, p := range []string{"PS> ", "PS ", "$ ", "> "} {
		if strings.HasPrefix(firstLine, p) {
			firstLine = strings.TrimSpace(firstLine[len(p):])
			break
		}
	}
	lowerFirst := strings.ToLower(firstLine)
	if strings.HasPrefix(lowerFirst, "curl ") || strings.HasPrefix(lowerFirst, "curl.exe ") ||
		lowerFirst == "curl" || lowerFirst == "curl.exe" {
		return FormatCurl, nil
	}

	if strings.HasPrefix(trimmed, "<") && strings.Contains(trimmed[:min(len(trimmed), 512)], "<jmeterTestPlan") {
		return FormatJMeter, nil
	}

	if strings.HasPrefix(trimmed, "{") {
		var top map[string]json.RawMessage
		if err := json.Unmarshal([]byte(trimmed), &top); err == nil {
			if _, ok := top["apifoxProject"]; ok {
				return FormatApifox, nil
			}
			if _, ok := top["apiCollection"]; ok {
				return FormatApifox, nil
			}
			if _, ok := top["openapi"]; ok {
				return FormatOpenAPI3, nil
			}
			if _, ok := top["swagger"]; ok {
				return FormatSwagger2, nil
			}
			if _, ok := top["log"]; ok {
				return FormatHAR, nil
			}
			if _, ok := top["info"]; ok {
				if _, hasItem := top["item"]; hasItem {
					return FormatPostman, nil
				}
			}
			if _, ok := top["_postman_variable_scope"]; ok {
				return FormatPostman, nil
			}
			if _, ok := top["values"]; ok {
				if _, ok := top["name"]; ok {
					return FormatPostman, nil
				}
			}
		}
	}

	// Fallback: inspect YAML top-level keys (also covers multi-document or YAML OpenAPI).
	var topYAML map[string]any
	if err := yaml.Unmarshal(content, &topYAML); err == nil && topYAML != nil {
		if _, ok := topYAML["openapi"]; ok {
			return FormatOpenAPI3, nil
		}
		if _, ok := topYAML["swagger"]; ok {
			return FormatSwagger2, nil
		}
	}

	ext := strings.ToLower(path.Ext(strings.ReplaceAll(filename, "\\", "/")))
	switch ext {
	case ".har":
		return FormatHAR, nil
	case ".jmx":
		return FormatJMeter, nil
	case ".tidegate", ".zip":
		return FormatTidegate, nil
	}

	return "", errors.New("无法识别导入内容的格式，目前支持 OpenAPI 3.x / Swagger 2.0、Postman Collection v2.1、HAR、JMeter (.jmx)、Apifox、cURL 与 .tidegate 包")
}

func parseBundle(in PreviewInput) (*parsedBundle, error) {
	if len(bytes.TrimSpace(in.Content)) == 0 {
		return nil, errors.New("导入内容不能为空")
	}
	format := strings.ToLower(strings.TrimSpace(in.Format))
	if format == "" || format == FormatAuto {
		var err error
		format, err = DetectFormat(in.Content, in.Filename)
		if err != nil {
			return nil, err
		}
	}

	var (
		b   *parsedBundle
		err error
	)
	switch format {
	case FormatOpenAPI3, FormatSwagger2:
		b, err = parseOpenAPIOrSwagger(in.Content, format)
	case FormatPostman:
		b, err = parsePostman(in.Content)
	case FormatHAR:
		b, err = parseHAR(in.Content)
	case FormatJMeter:
		b, err = parseJMeter(in.Content)
	case FormatApifox:
		b, err = parseApifox(in.Content)
	case FormatCurl:
		api, cerr := curl.Parse(string(in.Content))
		if cerr != nil {
			return nil, cerr
		}
		b = &parsedBundle{
			APIs: []parsedAPI{{API: *api}},
			Report: &ConversionReport{
				Format:    FormatCurl,
				APIsFound: 1,
			},
		}
	case FormatTidegate:
		b, err = parseArchive(in.Content)
	default:
		return nil, fmt.Errorf("不支持的导入格式 %q", format)
	}
	if err != nil {
		return nil, err
	}
	if b.Report == nil {
		b.Report = &ConversionReport{Format: format}
	}
	b.Report.APIsFound = len(b.APIs)
	b.Report.EnvsFound = len(b.Envs)
	b.Report.FoldersFound = len(b.Folders)
	b.Report.ScenariosFound = len(b.Scenarios)
	b.Report.FilesFound = len(b.Files)
	return b, nil
}

func normalizeStrategy(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return StrategyMerge, nil
	}
	switch s {
	case StrategyMerge, StrategyOverwrite, StrategySkip:
		return s, nil
	default:
		return "", fmt.Errorf("不支持的冲突策略 %q，可选 merge、overwrite 或 skip", s)
	}
}

// Preview parses the input and computes diffs against projectID without modifying
// any files on disk.
func Preview(st *store.Store, projectID string, in PreviewInput) (*PreviewResult, error) {
	strategy, err := normalizeStrategy(in.Strategy)
	if err != nil {
		return nil, err
	}
	bundle, err := parseBundle(in)
	if err != nil {
		return nil, err
	}
	idx, err := buildExistingIndex(st, projectID, in.TargetFolderID)
	if err != nil {
		return nil, err
	}

	diffs := computeDiffs(bundle, idx, strategy)
	return &PreviewResult{
		Report: bundle.Report,
		Diffs:  diffs,
	}, nil
}

// Apply imports the parsed resources into projectID using the chosen conflict
// strategy ("merge", "overwrite", or "skip"), preserving existing API IDs on
// updates so scenario references stay intact.
func Apply(st *store.Store, projectID string, in PreviewInput) (*ApplyResult, error) {
	strategy, err := normalizeStrategy(in.Strategy)
	if err != nil {
		return nil, err
	}
	bundle, err := parseBundle(in)
	if err != nil {
		return nil, err
	}
	idx, err := buildExistingIndex(st, projectID, in.TargetFolderID)
	if err != nil {
		return nil, err
	}

	report := bundle.Report

	// 1. Merge project variables if present (merge appends absent keys, overwrite replaces matching keys).
	if len(bundle.ProjectVars) > 0 {
		pr, err := st.GetProject(projectID)
		if err != nil {
			return nil, err
		}
		if merged, changed := mergeKeyValueLists(pr.Variables, bundle.ProjectVars, strategy); changed {
			pr.Variables = merged
			if _, err := st.UpdateProject(pr); err != nil {
				return nil, err
			}
		}
	}

	// 2. Import environments and map old IDs.
	envIDMap := make(map[string]string, len(bundle.Envs))
	for _, pe := range bundle.Envs {
		existing := idx.envByName[strings.ToLower(strings.TrimSpace(pe.Env.Name))]
		if existing != nil {
			envIDMap[pe.OldID] = existing.ID
			continue
		}
		created, err := st.CreateEnvironment(projectID, &pe.Env)
		if err != nil {
			report.Warnings = append(report.Warnings, fmt.Sprintf("创建环境 %q 失败：%v", pe.Env.Name, err))
			continue
		}
		idx.envByName[strings.ToLower(strings.TrimSpace(created.Name))] = created
		if pe.OldID != "" {
			envIDMap[pe.OldID] = created.ID
		}
		report.EnvsCreated++
	}

	// 3. Import data files before scenarios.
	for _, pf := range bundle.Files {
		if err := store.ValidateFileName(pf.Name); err != nil {
			report.Warnings = append(report.Warnings, fmt.Sprintf("跳过无效文件名 %q：%v", pf.Name, err))
			continue
		}
		if _, err := st.StatFile(projectID, pf.Name); err == nil {
			if strategy == StrategySkip || strategy == StrategyMerge {
				continue
			}
			cur, _ := st.StatFile(projectID, pf.Name)
			ver := ""
			if cur != nil {
				ver = cur.Version
			}
			if _, err := st.SaveFile(projectID, pf.Name, bytes.NewReader(pf.Content), ver); err != nil {
				report.Warnings = append(report.Warnings, fmt.Sprintf("覆盖数据文件 %q 失败：%v", pf.Name, err))
			} else {
				report.FilesImported++
			}
			continue
		}
		if _, err := st.SaveFile(projectID, pf.Name, bytes.NewReader(pf.Content), ""); err != nil {
			report.Warnings = append(report.Warnings, fmt.Sprintf("写入数据文件 %q 失败：%v", pf.Name, err))
			continue
		}
		report.FilesImported++
	}

	// Helper ensuring nested folder chains exist under idx.baseFolderPath.
	ensureFolder := func(segments []string, fld *parsedFolder) (string, error) {
		parentID := idx.baseFolderID
		curPath := idx.baseFolderPath
		for i, seg := range segments {
			name := strings.TrimSpace(seg)
			if name == "" {
				continue
			}
			if curPath == "" {
				curPath = name
			} else {
				curPath = curPath + "/" + name
			}
			key := strings.ToLower(curPath)
			if id, ok := idx.folderByPath[key]; ok {
				parentID = id
				continue
			}
			fReq := store.Folder{
				ParentID: parentID,
				Name:     name,
			}
			if i == len(segments)-1 && fld != nil {
				fReq.Headers = fld.Headers
				fReq.Auth = fld.Auth
			}
			created, err := st.CreateFolder(projectID, &fReq)
			if err != nil {
				return "", err
			}
			idx.folderByPath[key] = created.ID
			parentID = created.ID
			report.FoldersCreated++
		}
		return parentID, nil
	}

	// 4. Create declared folders (preserving metadata like inherited headers and auth).
	for i := range bundle.Folders {
		pf := &bundle.Folders[i]
		if _, err := ensureFolder(pf.Path, pf); err != nil {
			report.Warnings = append(report.Warnings, fmt.Sprintf("创建文件夹 %q 失败：%v", strings.Join(pf.Path, "/"), err))
		}
	}

	// 5. Create or update APIs preserving existing API ULIDs.
	apiIDMap := make(map[string]string, len(bundle.APIs))
	diffs := computeDiffs(bundle, idx, strategy)

	for i, pa := range bundle.APIs {
		d := diffs[i]
		if d.Action == ActionSkip {
			if pa.OldID != "" && d.ExistingID != "" {
				apiIDMap[pa.OldID] = d.ExistingID
			}
			report.APIsSkipped++
			continue
		}
		folderID, err := ensureFolder(pa.FolderPath, nil)
		if err != nil {
			report.Warnings = append(report.Warnings, fmt.Sprintf("创建目录失败（%s）：%v", pa.API.Name, err))
			report.APIsSkipped++
			diffs[i].Action = ActionSkip
			continue
		}

		apiCopy := pa.API
		apiCopy.FolderID = folderID
		if d.Action == ActionUpdate && d.ExistingID != "" {
			cur, err := st.GetAPI(projectID, d.ExistingID)
			if err != nil {
				report.Warnings = append(report.Warnings, fmt.Sprintf("读取已有接口 %q 失败：%v", d.ExistingID, err))
				report.APIsSkipped++
				continue
			}
			apiCopy.ID = cur.ID
			apiCopy.Version = cur.Version
			apiCopy.FolderID = cur.FolderID
			// In merge strategy, keep existing extractors/assertions unless incoming supplied them.
			if strategy == StrategyMerge {
				if len(apiCopy.Extract) == 0 {
					apiCopy.Extract = cur.Extract
				}
				if len(apiCopy.Assert) == 0 {
					apiCopy.Assert = cur.Assert
				}
			}
			updated, err := st.UpdateAPI(projectID, &apiCopy)
			if err != nil {
				report.Warnings = append(report.Warnings, fmt.Sprintf("更新接口 %q 失败：%v", apiCopy.Name, err))
				report.APIsSkipped++
				continue
			}
			if pa.OldID != "" {
				apiIDMap[pa.OldID] = updated.ID
			}
			report.APIsUpdated++
			continue
		}

		apiCopy.ID = ""
		apiCopy.Version = ""
		created, err := st.CreateAPI(projectID, &apiCopy)
		if err != nil {
			report.Warnings = append(report.Warnings, fmt.Sprintf("创建接口 %q 失败：%v", apiCopy.Name, err))
			report.APIsSkipped++
			continue
		}
		if pa.OldID != "" {
			apiIDMap[pa.OldID] = created.ID
		}
		idx.apiByKey[matchKey(fullRelPath(idx.baseFolderPath, pa.FolderPath), created.Request.Method, created.Request.URL)] = created.ID
		report.APIsCreated++
	}

	// 6. Import scenarios when present (e.g. .tidegate archives or .jmx thread groups).
	for _, ps := range bundle.Scenarios {
		sc := ps.Scenario
		sc.ID = ""
		sc.Version = ""
		if sc.EnvironmentID != "" {
			if mapped, ok := envIDMap[sc.EnvironmentID]; ok {
				sc.EnvironmentID = mapped
			} else {
				sc.EnvironmentID = ""
			}
		}
		sc.Steps = remapScenarioSteps(sc.Steps, apiIDMap)
		if len(sc.Steps) == 0 {
			continue
		}
		if _, err := st.CreateScenario(projectID, &sc); err != nil {
			report.Warnings = append(report.Warnings, fmt.Sprintf("导入场景 %q 失败：%v", sc.Name, err))
			continue
		}
		report.ScenariosCreated++
	}

	return &ApplyResult{
		Report: report,
		Diffs:  diffs,
	}, nil
}

func remapScenarioSteps(steps []store.ScenarioStep, apiIDMap map[string]string) []store.ScenarioStep {
	if len(steps) == 0 {
		return nil
	}
	out := make([]store.ScenarioStep, 0, len(steps))
	for _, step := range steps {
		stType := strings.ToLower(strings.TrimSpace(step.Type))
		switch stType {
		case "", "http":
			mapped, ok := apiIDMap[step.APIID]
			if !ok {
				continue
			}
			step.APIID = mapped
			out = append(out, step)
		case "group", "loop":
			step.Children = remapScenarioSteps(step.Children, apiIDMap)
			if len(step.Children) > 0 {
				out = append(out, step)
			}
		case "if":
			step.Children = remapScenarioSteps(step.Children, apiIDMap)
			step.Else = remapScenarioSteps(step.Else, apiIDMap)
			if len(step.Children) > 0 {
				out = append(out, step)
			}
		case "weighted":
			branches := make([]store.ScenarioWeightedBranch, 0, len(step.Branches))
			for _, br := range step.Branches {
				br.Steps = remapScenarioSteps(br.Steps, apiIDMap)
				if len(br.Steps) > 0 {
					branches = append(branches, br)
				}
			}
			if len(branches) > 0 {
				step.Branches = branches
				out = append(out, step)
			}
		}
	}
	return out
}

type existingIndex struct {
	baseFolderID   string
	baseFolderPath string
	folderByPath   map[string]string
	apiByKey       map[string]string
	envByName      map[string]*store.Environment
}

func buildExistingIndex(st *store.Store, projectID, targetFolderID string) (*existingIndex, error) {
	tree, err := st.Tree(projectID)
	if err != nil {
		return nil, err
	}
	envs, err := st.ListEnvironments(projectID)
	if err != nil {
		return nil, err
	}

	idx := &existingIndex{
		baseFolderID: targetFolderID,
		folderByPath: make(map[string]string),
		apiByKey:     make(map[string]string),
		envByName:    make(map[string]*store.Environment, len(envs)),
	}
	for _, e := range envs {
		idx.envByName[strings.ToLower(strings.TrimSpace(e.Name))] = e
	}

	var walk func(prefix string, nodes []*store.TreeNode)
	walk = func(prefix string, nodes []*store.TreeNode) {
		for _, n := range nodes {
			switch n.Type {
			case store.NodeFolder:
				cur := n.Name
				if prefix != "" {
					cur = prefix + "/" + n.Name
				}
				idx.folderByPath[strings.ToLower(cur)] = n.ID
				if targetFolderID != "" && n.ID == targetFolderID {
					idx.baseFolderPath = cur
				}
				walk(cur, n.Children)
			case store.NodeAPI:
				m := n.Method
				if m == "" {
					m = "GET"
				}
				idx.apiByKey[matchKey(prefix, m, n.URL)] = n.ID
			}
		}
	}
	walk("", tree.Children)

	if targetFolderID != "" && idx.baseFolderPath == "" {
		// Validate that the requested target folder actually exists.
		if _, err := st.GetFolder(projectID, targetFolderID); err != nil {
			return nil, err
		}
	}
	return idx, nil
}

func computeDiffs(b *parsedBundle, idx *existingIndex, strategy string) []ImportDiff {
	diffs := make([]ImportDiff, 0, len(b.APIs))
	for _, pa := range b.APIs {
		m := strings.ToUpper(strings.TrimSpace(pa.API.Request.Method))
		if m == "" {
			m = "GET"
		}
		fRel := fullRelPath(idx.baseFolderPath, pa.FolderPath)
		key := matchKey(fRel, m, pa.API.Request.URL)
		existingID := idx.apiByKey[key]
		action := ActionCreate
		if existingID != "" {
			if strategy == StrategySkip {
				action = ActionSkip
			} else {
				action = ActionUpdate
			}
		}
		diffs = append(diffs, ImportDiff{
			Action:     action,
			FolderPath: strings.Join(pa.FolderPath, "/"),
			Method:     m,
			URL:        pa.API.Request.URL,
			Name:       pa.API.Name,
			ExistingID: existingID,
		})
	}
	return diffs
}

func fullRelPath(base string, segs []string) string {
	var clean []string
	if base != "" {
		clean = append(clean, base)
	}
	for _, s := range segs {
		if t := strings.TrimSpace(s); t != "" {
			clean = append(clean, t)
		}
	}
	return strings.Join(clean, "/")
}

func matchKey(folderRel, method, rawURL string) string {
	return strings.ToLower(strings.Trim(folderRel, "/")) + "\x00" +
		strings.ToUpper(strings.TrimSpace(method)) + "\x00" +
		normalizeMatchPath(rawURL)
}

// normalizeMatchPath canonicalizes URLs for duplicate matching so "/users/:id"
// matches "/users/{id}" and relative paths ignore query strings or trailing slashes.
func normalizeMatchPath(raw string) string {
	s := strings.TrimSpace(raw)
	if idx := strings.IndexAny(s, "?#"); idx >= 0 {
		s = s[:idx]
	}
	if strings.HasPrefix(strings.ToLower(s), "http://") || strings.HasPrefix(strings.ToLower(s), "https://") {
		san := strings.NewReplacer("{{", "__", "}}", "__").Replace(s)
		if u, err := url.Parse(san); err == nil {
			s = u.Scheme + "://" + strings.ToLower(u.Host) + normalizePathSegments(u.Path)
			return s
		}
	}
	// Strip leading {{var}} host prefix only if formatted like {{host}}/path.
	if strings.HasPrefix(s, "{{") {
		if end := strings.Index(s, "}}"); end > 0 {
			rest := s[end+2:]
			if strings.HasPrefix(rest, "/") {
				return s[:end+2] + normalizePathSegments(rest)
			}
		}
	}
	return normalizePathSegments(s)
}

func normalizePathSegments(p string) string {
	parts := strings.Split(strings.Trim(p, "/"), "/")
	if len(parts) == 1 && parts[0] == "" {
		return "/"
	}
	for i, seg := range parts {
		if strings.HasPrefix(seg, ":") && len(seg) > 1 {
			parts[i] = "{}"
		} else if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") && !strings.HasPrefix(seg, "{{") {
			parts[i] = "{}"
		}
	}
	return "/" + strings.Join(parts, "/")
}

func mergeKeyValueLists(dst, src store.KeyValueList, strategy string) (store.KeyValueList, bool) {
	if len(src) == 0 {
		return dst, false
	}
	pos := make(map[string]int, len(dst))
	out := append(store.KeyValueList(nil), dst...)
	for i, kv := range out {
		pos[strings.TrimSpace(kv.Name)] = i
	}
	changed := false
	for _, kv := range src {
		name := strings.TrimSpace(kv.Name)
		if name == "" {
			continue
		}
		if idx, exists := pos[name]; exists {
			if strategy == StrategyOverwrite {
				out[idx] = kv
				changed = true
			}
			continue
		}
		pos[name] = len(out)
		out = append(out, kv)
		changed = true
	}
	return out, changed
}
