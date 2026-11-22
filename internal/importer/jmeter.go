package importer

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"tidegate/internal/scenario"
	"tidegate/internal/store"
)

type jmxNode struct {
	Tag      string
	Attrs    map[string]string
	Text     string
	Children []*jmxNode
}

func (n *jmxNode) attr(k string) string {
	if n == nil {
		return ""
	}
	return n.Attrs[k]
}

func (n *jmxNode) prop(name string) string {
	if n == nil {
		return ""
	}
	for _, c := range n.Children {
		if c.Attrs["name"] == name {
			switch c.Tag {
			case "stringProp", "intProp", "longProp", "boolProp", "floatProp", "doubleProp":
				return strings.TrimSpace(c.Text)
			}
		}
	}
	return ""
}

func (n *jmxNode) childNamed(tag, nameAttr string) *jmxNode {
	if n == nil {
		return nil
	}
	for _, c := range n.Children {
		if (tag == "" || c.Tag == tag) && (nameAttr == "" || c.Attrs["name"] == nameAttr) {
			return c
		}
	}
	return nil
}

func (n *jmxNode) allCollections() []*jmxNode {
	var out []*jmxNode
	if n == nil {
		return nil
	}
	for _, c := range n.Children {
		if c.Tag == "collectionProp" {
			out = append(out, c)
		} else if c.Tag == "elementProp" {
			for _, gc := range c.Children {
				if gc.Tag == "collectionProp" {
					out = append(out, gc)
				}
			}
		}
	}
	return out
}

func (n *jmxNode) enabled() bool {
	v := strings.ToLower(strings.TrimSpace(n.attr("enabled")))
	return v != "false"
}

func parseJMXDOM(content []byte) (*jmxNode, error) {
	dec := xml.NewDecoder(bytes.NewReader(bytes.TrimPrefix(content, []byte("\xef\xbb\xbf"))))
	dec.Strict = true
	dec.Entity = map[string]string{}
	var stack []*jmxNode
	var root *jmxNode

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("解析 JMeter XML 失败：%w", err)
		}
		switch t := tok.(type) {
		case xml.Directive:
			if strings.Contains(strings.ToUpper(string(t)), "ENTITY") {
				return nil, errors.New("安全拒绝：JMX 文件不允许包含 XML ENTITY 声明")
			}
		case xml.StartElement:
			attrs := make(map[string]string, len(t.Attr))
			for _, a := range t.Attr {
				attrs[a.Name.Local] = a.Value
			}
			node := &jmxNode{Tag: t.Name.Local, Attrs: attrs}
			if len(stack) > 0 {
				parent := stack[len(stack)-1]
				parent.Children = append(parent.Children, node)
			} else {
				root = node
			}
			stack = append(stack, node)
		case xml.CharData:
			if len(stack) > 0 {
				stack[len(stack)-1].Text += string(t)
			}
		case xml.EndElement:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}
	if root == nil {
		return nil, errors.New("JMX 文件为空或缺少根节点")
	}
	return root, nil
}

type jmxTreePair struct {
	elem *jmxNode
	sub  *jmxNode // matching <hashTree> child container
}

func pairedChildren(hashTree *jmxNode) []jmxTreePair {
	if hashTree == nil {
		return nil
	}
	var out []jmxTreePair
	ch := hashTree.Children
	for i := 0; i < len(ch); i++ {
		if ch[i].Tag == "hashTree" {
			continue
		}
		pair := jmxTreePair{elem: ch[i]}
		if i+1 < len(ch) && ch[i+1].Tag == "hashTree" {
			pair.sub = ch[i+1]
			i++
		}
		out = append(out, pair)
	}
	return out
}

type jmxDefaults struct {
	Scheme string
	Host   string
	Port   string
	Path   string
}

func (d jmxDefaults) baseURL() string {
	if d.Host == "" {
		return ""
	}
	scheme := d.Scheme
	if scheme == "" {
		scheme = "http"
	}
	authority := d.Host
	if d.Port != "" && !strings.Contains(authority, ":") {
		authority += ":" + d.Port
	}
	basePath := strings.TrimRight(d.Path, "/")
	if basePath != "" && !strings.HasPrefix(basePath, "/") {
		basePath = "/" + basePath
	}
	return scheme + "://" + authority + basePath
}

type jmxParser struct {
	report      *ConversionReport
	projectVars store.KeyValueList
	seenProjVar map[string]bool
	envs        []parsedEnv
	folders     []parsedFolder
	apis        []parsedAPI
	scenarios   []parsedScenario
	files       []parsedFile
	seenFiles   map[string]bool
	apiSeq      int
	stepSeq     int
	envCreated  bool
}

type jmxScope struct {
	defaults jmxDefaults
	headers  store.KeyValueList
	csvCols  map[string]string // colName -> datasetAlias.colName
	csvData  []scenario.DataSource
	seenCSV  map[string]bool
	folder   []string
}

func (s *jmxScope) clone() *jmxScope {
	cp := &jmxScope{
		defaults: s.defaults,
		headers:  append(store.KeyValueList(nil), s.headers...),
		csvCols:  make(map[string]string, len(s.csvCols)),
		csvData:  append([]scenario.DataSource(nil), s.csvData...),
		seenCSV:  make(map[string]bool, len(s.seenCSV)),
		folder:   append([]string(nil), s.folder...),
	}
	for k, v := range s.csvCols {
		cp.csvCols[k] = v
	}
	for k, v := range s.seenCSV {
		cp.seenCSV[k] = v
	}
	return cp
}

var jmxVarRe = regexp.MustCompile(`\$\{([A-Za-z0-9_.-]+)\}`)

func convertJMeterVars(raw string, csvCols map[string]string) string {
	if raw == "" {
		return ""
	}
	return jmxVarRe.ReplaceAllStringFunc(raw, func(match string) string {
		key := match[2 : len(match)-1]
		if mapped, ok := csvCols[key]; ok {
			return "{{" + mapped + "}}"
		}
		return "{{" + key + "}}"
	})
}

func parseJMeter(content []byte) (*parsedBundle, error) {
	dom, err := parseJMXDOM(content)
	if err != nil {
		return nil, err
	}
	root := dom
	if root.Tag == "jmeterTestPlan" {
		root = dom.childNamed("hashTree", "")
	}
	if root == nil {
		return nil, errors.New("无法定位 jmeterTestPlan 内的 hashTree 节点")
	}

	p := &jmxParser{
		report: &ConversionReport{
			Format:          FormatJMeter,
			DetectedVersion: dom.attr("properties"),
		},
		seenProjVar: map[string]bool{},
		seenFiles:   map[string]bool{},
	}

	baseScope := &jmxScope{
		csvCols: map[string]string{},
		seenCSV: map[string]bool{},
	}

	for _, pair := range pairedChildren(root) {
		if !pair.elem.enabled() {
			continue
		}
		if pair.elem.Tag == "TestPlan" {
			p.collectConfig(pair.elem, baseScope, &p.projectVars)
			p.walkTestPlanTree(pair.sub, baseScope)
		} else {
			p.walkNodePair(pair, baseScope, nil)
		}
	}

	if len(p.apis) == 0 && len(p.scenarios) == 0 {
		return nil, errors.New("JMX 文件中未找到有效的 HTTP Sampler 请求")
	}
	return &parsedBundle{
		ProjectVars: p.projectVars,
		Envs:        p.envs,
		Folders:     p.folders,
		APIs:        p.apis,
		Scenarios:   p.scenarios,
		Files:       p.files,
		Report:      p.report,
	}, nil
}

func (p *jmxParser) walkTestPlanTree(ht *jmxNode, scope *jmxScope) {
	pairs := pairedChildren(ht)
	// First pass: plan-wide config elements (User Defined Variables, HTTP Defaults, Header Manager, CSV Data Set).
	for _, pair := range pairs {
		if !pair.elem.enabled() {
			continue
		}
		if isJMeterConfigTag(pair.elem.Tag) {
			p.collectConfig(pair.elem, scope, &p.projectVars)
		}
	}
	// Second pass: executable groups & controllers.
	var looseSteps []store.ScenarioStep
	for _, pair := range pairs {
		if !pair.elem.enabled() || isJMeterConfigTag(pair.elem.Tag) {
			continue
		}
		if pair.elem.Tag == "ThreadGroup" || pair.elem.Tag == "SetupThreadGroup" || pair.elem.Tag == "PostThreadGroup" {
			p.parseThreadGroup(pair, scope.clone())
			continue
		}
		steps := p.walkNodePair(pair, scope, nil)
		looseSteps = append(looseSteps, steps...)
	}
	if len(looseSteps) > 0 && len(p.scenarios) == 0 {
		p.scenarios = append(p.scenarios, parsedScenario{
			Scenario: store.Scenario{
				Name:          "JMX 导入场景",
				EnvironmentID: p.defaultEnvOldID(),
				Data:          scope.csvData,
				Steps:         looseSteps,
				Load: scenario.Load{
					Mode:        "vus",
					Stages:      []scenario.Stage{{Duration: scenario.Duration(10 * time.Second), Target: 1}},
					MaxRequests: int64(countLeafScenarioSteps(looseSteps)),
				},
			},
		})
	}
}

func (p *jmxParser) defaultEnvOldID() string {
	if p.envCreated && len(p.envs) > 0 {
		return p.envs[0].OldID
	}
	return ""
}

func isJMeterConfigTag(tag string) bool {
	switch tag {
	case "Arguments", "ConfigTestElement", "HeaderManager", "CSVDataSet", "CookieManager", "CacheManager":
		return true
	}
	return false
}

func (p *jmxParser) collectConfig(el *jmxNode, scope *jmxScope, projVars *store.KeyValueList) {
	switch el.Tag {
	case "TestPlan":
		if udv := el.childNamed("elementProp", "TestPlan.user_defined_variables"); udv != nil {
			p.extractArguments(udv, scope, projVars)
		}
	case "Arguments":
		p.extractArguments(el, scope, projVars)
	case "ConfigTestElement":
		if strings.Contains(el.attr("guiclass"), "HttpDefaults") || el.prop("HTTPSampler.domain") != "" || el.prop("HTTPSampler.protocol") != "" {
			if s := el.prop("HTTPSampler.protocol"); s != "" {
				scope.defaults.Scheme = s
			}
			if h := el.prop("HTTPSampler.domain"); h != "" {
				scope.defaults.Host = convertJMeterVars(h, scope.csvCols)
			}
			if pt := el.prop("HTTPSampler.port"); pt != "" {
				scope.defaults.Port = pt
			}
			if bp := el.prop("HTTPSampler.path"); bp != "" {
				scope.defaults.Path = convertJMeterVars(bp, scope.csvCols)
			}
			if base := scope.defaults.baseURL(); base != "" && !p.envCreated {
				p.envCreated = true
				p.envs = append(p.envs, parsedEnv{
					OldID: "jmx-default-env",
					Env: store.Environment{
						Name:    "JMeter 默认环境",
						BaseURL: base,
					},
				})
			}
		}
	case "HeaderManager":
		for _, col := range el.allCollections() {
			for _, ep := range col.Children {
				if ep.Tag != "elementProp" {
					continue
				}
				k := strings.TrimSpace(ep.prop("Header.name"))
				v := convertJMeterVars(ep.prop("Header.value"), scope.csvCols)
				if k != "" {
					scope.headers = append(scope.headers, store.KeyValue{Name: k, Value: scenario.Scalar(v)})
				}
			}
		}
	case "CSVDataSet":
		rawFile := strings.TrimSpace(el.prop("filename"))
		if rawFile == "" {
			rawFile = "data.csv"
		}
		fileName := path.Base(strings.ReplaceAll(rawFile, "\\", "/"))
		if store.ValidateFileName(fileName) != nil {
			fileName = "jmx_data.csv"
		}
		if !strings.HasSuffix(strings.ToLower(fileName), ".csv") && !strings.HasSuffix(strings.ToLower(fileName), ".json") && !strings.HasSuffix(strings.ToLower(fileName), ".jsonl") {
			fileName += ".csv"
		}
		alias := sanitizeVarAlias(strings.TrimSuffix(fileName, path.Ext(fileName)))
		if alias == "" {
			alias = "data"
		}
		if !scope.seenCSV[alias] {
			scope.seenCSV[alias] = true
			scope.csvData = append(scope.csvData, scenario.DataSource{
				Name: alias,
				File: fileName,
				Mode: "sequential",
			})
		}
		varsRaw := el.prop("variableNames")
		var cols []string
		for _, c := range strings.Split(varsRaw, ",") {
			col := strings.TrimSpace(c)
			if col != "" {
				scope.csvCols[col] = alias + "." + col
				cols = append(cols, col)
			}
		}
		if !p.seenFiles[fileName] && len(cols) > 0 {
			p.seenFiles[fileName] = true
			placeholderVals := make([]string, len(cols))
			for i, col := range cols {
				placeholderVals[i] = col + "_sample"
			}
			csvStub := strings.Join(cols, ",") + "\n" + strings.Join(placeholderVals, ",") + "\n"
			p.files = append(p.files, parsedFile{Name: fileName, Content: []byte(csvStub)})
			p.report.Warnings = append(p.report.Warnings, fmt.Sprintf("CSVDataSet %q 未附带本地数据内容，已生成占位数据文件 %q（列：%s），建议开压前替换真实数据", rawFile, fileName, strings.Join(cols, ",")))
		}
	case "CookieManager", "CacheManager":
		// Managed automatically per virtual user by Tidegate's engine.
	}
}

func (p *jmxParser) extractArguments(container *jmxNode, scope *jmxScope, projVars *store.KeyValueList) {
	for _, col := range container.allCollections() {
		for _, ep := range col.Children {
			if ep.Tag != "elementProp" {
				continue
			}
			k := strings.TrimSpace(ep.prop("Argument.name"))
			v := convertJMeterVars(ep.prop("Argument.value"), scope.csvCols)
			if k == "" || p.seenProjVar[k] {
				continue
			}
			p.seenProjVar[k] = true
			if projVars != nil {
				*projVars = append(*projVars, store.KeyValue{Name: k, Value: scenario.Scalar(v)})
			}
		}
	}
}

func (p *jmxParser) parseThreadGroup(pair jmxTreePair, scope *jmxScope) {
	el := pair.elem
	name := strings.TrimSpace(el.attr("testname"))
	if name == "" {
		name = "线程组"
	}
	scope.folder = []string{name}
	p.folders = append(p.folders, parsedFolder{Path: append([]string(nil), scope.folder...)})

	// Collect ThreadGroup-scoped config elements first.
	for _, child := range pairedChildren(pair.sub) {
		if child.elem.enabled() && isJMeterConfigTag(child.elem.Tag) {
			p.collectConfig(child.elem, scope, &p.projectVars)
		}
	}

	var steps []store.ScenarioStep
	for _, child := range pairedChildren(pair.sub) {
		if !child.elem.enabled() || isJMeterConfigTag(child.elem.Tag) {
			continue
		}
		steps = append(steps, p.walkNodePair(child, scope, nil)...)
	}
	if len(steps) == 0 {
		return
	}

	threads, _ := strconv.Atoi(el.prop("ThreadGroup.num_threads"))
	if threads <= 0 {
		threads = 1
	}
	rampSec, _ := strconv.Atoi(el.prop("ThreadGroup.ramp_time"))
	durSec, _ := strconv.Atoi(el.prop("ThreadGroup.duration"))
	scheduler := strings.EqualFold(el.prop("ThreadGroup.scheduler"), "true")

	loops := 1
	if mc := el.childNamed("elementProp", "ThreadGroup.main_controller"); mc != nil {
		if lv, err := strconv.Atoi(mc.prop("LoopController.loops")); err == nil && lv > 0 {
			loops = lv
		}
	}

	var stages []scenario.Stage
	var maxReqs int64
	if scheduler && durSec > 0 {
		if rampSec > 0 && rampSec < durSec {
			stages = []scenario.Stage{
				{Duration: scenario.Duration(time.Duration(rampSec) * time.Second), Target: float64(threads)},
				{Duration: scenario.Duration(time.Duration(durSec-rampSec) * time.Second), Target: float64(threads)},
			}
		} else {
			stages = []scenario.Stage{
				{Duration: scenario.Duration(time.Duration(durSec) * time.Second), Target: float64(threads)},
			}
		}
	} else {
		stages = []scenario.Stage{
			{Duration: scenario.Duration(60 * time.Second), Target: float64(threads)},
		}
		leafCount := max(1, countLeafScenarioSteps(steps))
		maxReqs = int64(threads * max(1, loops) * leafCount)
	}

	p.scenarios = append(p.scenarios, parsedScenario{
		Scenario: store.Scenario{
			Name:          name,
			EnvironmentID: p.defaultEnvOldID(),
			Data:          scope.csvData,
			Steps:         steps,
			Load: scenario.Load{
				Mode:        "vus",
				Stages:      stages,
				MaxRequests: maxReqs,
			},
		},
	})
}

func (p *jmxParser) nextStepID(prefix string) string {
	p.stepSeq++
	return fmt.Sprintf("%s_%d", prefix, p.stepSeq)
}

func (p *jmxParser) walkNodePair(pair jmxTreePair, parentScope *jmxScope, pendingTimers *[]*scenario.Think) []store.ScenarioStep {
	el := pair.elem
	if !el.enabled() {
		return nil
	}
	switch el.Tag {
	case "HTTPSamplerProxy", "HTTPSampler", "HTTPSampler2":
		st := p.parseHTTPSampler(pair, parentScope)
		return []store.ScenarioStep{st}

	case "TransactionController":
		sc := parentScope.clone()
		name := strings.TrimSpace(el.attr("testname"))
		if name == "" {
			name = "事务组"
		}
		sc.folder = append(sc.folder, name)
		p.folders = append(p.folders, parsedFolder{Path: append([]string(nil), sc.folder...)})
		children := p.collectContainerChildren(pair.sub, sc)
		if len(children) == 0 {
			return nil
		}
		return []store.ScenarioStep{{
			ID:       p.nextStepID("grp"),
			Name:     name,
			Type:     "group",
			Children: children,
		}}

	case "LoopController":
		sc := parentScope.clone()
		children := p.collectContainerChildren(pair.sub, sc)
		if len(children) == 0 {
			return nil
		}
		loops, err := strconv.Atoi(el.prop("LoopController.loops"))
		if err != nil || loops <= 0 {
			loops = 1
			p.report.Warnings = append(p.report.Warnings, fmt.Sprintf("LoopController %q 为无限循环或无效次数，已折算为 1 次（可在场景里调整循环条件）", el.attr("testname")))
		}
		return []store.ScenarioStep{{
			ID:        p.nextStepID("loop"),
			Name:      strings.TrimSpace(el.attr("testname")),
			Type:      "loop",
			LoopCount: loops,
			Children:  children,
		}}

	case "IfController":
		sc := parentScope.clone()
		children := p.collectContainerChildren(pair.sub, sc)
		if len(children) == 0 {
			return nil
		}
		rawCond := strings.TrimSpace(el.prop("IfController.condition"))
		cond := convertJMeterVars(rawCond, sc.csvCols)
		if !isSimpleTidegateCondition(cond) {
			p.report.Warnings = append(p.report.Warnings, fmt.Sprintf("IfController %q 条件 %q 非 Tidegate 原生比较语法，已暂存为占位条件，请在导入后检查", el.attr("testname"), rawCond))
			cond = "{{jmx_condition}} not_empty"
		}
		return []store.ScenarioStep{{
			ID:       p.nextStepID("if"),
			Name:     strings.TrimSpace(el.attr("testname")),
			Type:     "if",
			Expr:     cond,
			Children: children,
		}}

	case "GenericController", "RecordingController", "ModuleController":
		sc := parentScope.clone()
		if name := strings.TrimSpace(el.attr("testname")); name != "" {
			sc.folder = append(sc.folder, name)
			p.folders = append(p.folders, parsedFolder{Path: append([]string(nil), sc.folder...)})
		}
		return p.collectContainerChildren(pair.sub, sc)

	case "ThreadGroup", "SetupThreadGroup", "PostThreadGroup":
		p.parseThreadGroup(pair, parentScope.clone())
		return nil

	default:
		if isJMeterConfigTag(el.Tag) {
			return nil
		}
		if tm := parseJMeterTimer(el); tm != nil {
			if pendingTimers != nil {
				*pendingTimers = append(*pendingTimers, tm)
			}
			return nil
		}
		label := strings.TrimSpace(el.attr("testname"))
		if label == "" {
			label = el.Tag
		}
		p.report.Skipped = append(p.report.Skipped, fmt.Sprintf("JMeter 元件暂不支持：%s (%s)", label, el.Tag))
		return nil
	}
}

func (p *jmxParser) collectContainerChildren(ht *jmxNode, sc *jmxScope) []store.ScenarioStep {
	pairs := pairedChildren(ht)
	for _, cp := range pairs {
		if cp.elem.enabled() && isJMeterConfigTag(cp.elem.Tag) {
			p.collectConfig(cp.elem, sc, &p.projectVars)
		}
	}
	var out []store.ScenarioStep
	var pending []*scenario.Think
	for _, cp := range pairs {
		if !cp.elem.enabled() || isJMeterConfigTag(cp.elem.Tag) {
			continue
		}
		built := p.walkNodePair(cp, sc, &pending)
		if len(built) > 0 && len(pending) > 0 && built[len(built)-1].Think == nil {
			built[len(built)-1].Think = pending[0]
			pending = pending[1:]
		}
		out = append(out, built...)
	}
	return out
}

func (p *jmxParser) parseHTTPSampler(pair jmxTreePair, scope *jmxScope) store.ScenarioStep {
	el := pair.elem
	name := strings.TrimSpace(el.attr("testname"))
	if name == "" {
		name = "HTTP 请求"
	}
	method := strings.ToUpper(strings.TrimSpace(el.prop("HTTPSampler.method")))
	if method == "" {
		method = "GET"
	}

	rawPath := convertJMeterVars(el.prop("HTTPSampler.path"), scope.csvCols)
	domain := convertJMeterVars(el.prop("HTTPSampler.domain"), scope.csvCols)
	proto := strings.TrimSpace(el.prop("HTTPSampler.protocol"))
	port := strings.TrimSpace(el.prop("HTTPSampler.port"))

	var fullURL string
	lowerPath := strings.ToLower(rawPath)
	if strings.HasPrefix(lowerPath, "http://") || strings.HasPrefix(lowerPath, "https://") {
		fullURL = rawPath
	} else if domain != "" {
		if proto == "" {
			proto = scope.defaults.Scheme
		}
		if proto == "" {
			proto = "http"
		}
		authority := domain
		if port != "" && !strings.Contains(authority, ":") {
			authority += ":" + port
		}
		if rawPath != "" && !strings.HasPrefix(rawPath, "/") && !strings.HasPrefix(rawPath, "?") {
			rawPath = "/" + rawPath
		}
		fullURL = proto + "://" + authority + rawPath
	} else {
		if rawPath == "" {
			rawPath = "/"
		}
		fullURL = rawPath
	}

	// Collect Headers, Query, Body, Extractors, Assertions, Timers.
	headers := append(store.KeyValueList(nil), scope.headers...)
	var query store.KeyValueList
	postBodyRaw := strings.EqualFold(el.prop("HTTPSampler.postBodyRaw"), "true")
	doMultipart := strings.EqualFold(el.prop("HTTPSampler.DO_MULTIPART_POST"), "true")

	var body *store.APIBody
	if argsEl := el.childNamed("elementProp", "HTTPsampler.Arguments"); argsEl != nil {
		var argPairs []store.KeyValue
		var rawPayload strings.Builder
		for _, col := range argsEl.allCollections() {
			for _, ep := range col.Children {
				if ep.Tag != "elementProp" {
					continue
				}
				k := convertJMeterVars(ep.prop("Argument.name"), scope.csvCols)
				v := convertJMeterVars(ep.prop("Argument.value"), scope.csvCols)
				if postBodyRaw {
					rawPayload.WriteString(v)
				} else if k != "" {
					argPairs = append(argPairs, store.KeyValue{Name: k, Value: scenario.Scalar(v)})
				}
			}
		}
		if postBodyRaw {
			content := rawPayload.String()
			trimmed := strings.TrimSpace(content)
			if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
				body = &store.APIBody{Type: "json", Content: content}
			} else {
				body = &store.APIBody{Type: "raw", Content: content}
			}
		} else if len(argPairs) > 0 {
			if method == "GET" || method == "HEAD" || method == "DELETE" {
				query = append(query, argPairs...)
			} else if doMultipart {
				body = &store.APIBody{Type: "multipart", Fields: argPairs}
			} else {
				body = &store.APIBody{Type: "form", Fields: argPairs}
			}
		}
	}

	// Parse query string embedded in URL into QueryList when convenient (keep clean base path).
	if !strings.Contains(fullURL, "{{") {
		if u, err := url.Parse(fullURL); err == nil && u.RawQuery != "" {
			for k, vals := range u.Query() {
				for _, val := range vals {
					query = append(query, store.KeyValue{Name: k, Value: scenario.Scalar(val)})
				}
			}
			u.RawQuery = ""
			fullURL = u.String()
		}
	}

	var extracts []scenario.Extract
	var asserts []scenario.Assert
	var stepThink *scenario.Think

	for _, child := range pairedChildren(pair.sub) {
		ce := child.elem
		if !ce.enabled() {
			continue
		}
		switch ce.Tag {
		case "HeaderManager":
			for _, col := range ce.allCollections() {
				for _, ep := range col.Children {
					if ep.Tag != "elementProp" {
						continue
					}
					k := strings.TrimSpace(ep.prop("Header.name"))
					v := convertJMeterVars(ep.prop("Header.value"), scope.csvCols)
					if k != "" {
						headers = append(headers, store.KeyValue{Name: k, Value: scenario.Scalar(v)})
					}
				}
			}
		case "JSONPostProcessor":
			names := strings.Split(ce.prop("JSONPostProcessor.referenceNames"), ";")
			exprs := strings.Split(ce.prop("JSONPostProcessor.jsonPathExprs"), ";")
			defs := strings.Split(ce.prop("JSONPostProcessor.defaultValues"), ";")
			for i, rawName := range names {
				varName := strings.TrimSpace(rawName)
				if varName == "" || i >= len(exprs) {
					continue
				}
				ex := scenario.Extract{
					Name: sanitizeVarAlias(varName),
					From: "jsonpath",
					Expr: strings.TrimSpace(exprs[i]),
				}
				if i < len(defs) && defs[i] != "" && defs[i] != "NOT_FOUND" {
					d := defs[i]
					ex.Default = &d
				}
				extracts = append(extracts, ex)
			}
		case "RegexExtractor":
			ref := strings.TrimSpace(ce.prop("RegexExtractor.refname"))
			reExpr := ce.prop("RegexExtractor.regex")
			if ref != "" && reExpr != "" {
				ex := scenario.Extract{
					Name: sanitizeVarAlias(ref),
					From: "regex",
					Expr: reExpr,
				}
				tmpl := strings.TrimSpace(ce.prop("RegexExtractor.template"))
				if strings.HasPrefix(tmpl, "$") && strings.HasSuffix(tmpl, "$") && len(tmpl) >= 3 {
					if grp, err := strconv.Atoi(tmpl[1 : len(tmpl)-1]); err == nil && grp >= 0 {
						ex.Group = &grp
					}
				}
				if defVal := ce.prop("RegexExtractor.default"); defVal != "" {
					ex.Default = &defVal
				}
				extracts = append(extracts, ex)
			}
		case "XPathExtractor", "XPath2Extractor":
			ref := ce.prop(ce.Tag + ".refname")
			if ref == "" {
				ref = ce.prop("XPathExtractor.refname")
			}
			xp := ce.prop(ce.Tag + ".xpathQuery")
			if xp == "" {
				xp = ce.prop("XPathExtractor.xpathQuery")
			}
			if ref != "" && xp != "" {
				ex := scenario.Extract{
					Name: sanitizeVarAlias(ref),
					From: "xpath",
					Expr: strings.TrimSpace(xp),
				}
				if defVal := ce.prop(ce.Tag + ".default"); defVal != "" {
					ex.Default = &defVal
				}
				extracts = append(extracts, ex)
			}
		case "ResponseAssertion":
			field := ce.prop("Assertion.test_field")
			var vals []string
			for _, col := range ce.allCollections() {
				for _, sp := range col.Children {
					if sp.Tag == "stringProp" && strings.TrimSpace(sp.Text) != "" {
						vals = append(vals, convertJMeterVars(strings.TrimSpace(sp.Text), scope.csvCols))
					}
				}
			}
			if field == "Assertion.response_code" && len(vals) > 0 {
				var codes []int
				for _, v := range vals {
					if c, err := strconv.Atoi(v); err == nil {
						codes = append(codes, c)
					}
				}
				if len(codes) == 1 {
					sc := scenario.Scalar(strconv.Itoa(codes[0]))
					asserts = append(asserts, scenario.Assert{Type: "status", Equals: &sc})
				} else if len(codes) > 1 {
					asserts = append(asserts, scenario.Assert{Type: "status", In: codes})
				}
			} else if len(vals) > 0 {
				for _, v := range vals {
					asserts = append(asserts, scenario.Assert{Type: "body_contains", Value: v})
				}
			}
		case "DurationAssertion":
			if msVal, err := strconv.Atoi(ce.prop("DurationAssertion.duration")); err == nil && msVal > 0 {
				asserts = append(asserts, scenario.Assert{
					Type: "duration",
					Max:  scenario.Duration(time.Duration(msVal) * time.Millisecond),
				})
			}
		default:
			if tm := parseJMeterTimer(ce); tm != nil {
				if stepThink == nil {
					stepThink = tm
				}
			} else {
				p.report.Skipped = append(p.report.Skipped, fmt.Sprintf("Sampler %q 的子元件暂不支持：%s (%s)", name, ce.attr("testname"), ce.Tag))
			}
		}
	}

	p.apiSeq++
	oldAPIID := fmt.Sprintf("jmx-api-%d", p.apiSeq)
	p.apis = append(p.apis, parsedAPI{
		OldID:      oldAPIID,
		FolderPath: append([]string(nil), scope.folder...),
		API: store.API{
			Name: name,
			Request: store.APIRequest{
				Method:  method,
				URL:     fullURL,
				Headers: headers,
				Query:   query,
				Body:    body,
			},
			Extract: extracts,
			Assert:  asserts,
		},
	})
	return store.ScenarioStep{
		ID:    p.nextStepID("step"),
		Name:  name,
		Type:  "http",
		APIID: oldAPIID,
		Think: stepThink,
	}
}

func parseJMeterTimer(el *jmxNode) *scenario.Think {
	delay, _ := strconv.ParseFloat(el.prop("ConstantTimer.delay"), 64)
	rng, _ := strconv.ParseFloat(el.prop("RandomTimer.range"), 64)
	switch el.Tag {
	case "ConstantTimer":
		if delay <= 0 {
			return nil
		}
		return &scenario.Think{
			Type: "fixed",
			Min:  scenario.Duration(time.Duration(delay) * time.Millisecond),
		}
	case "UniformRandomTimer":
		if delay <= 0 && rng <= 0 {
			return nil
		}
		minD := time.Duration(max(0.0, delay)) * time.Millisecond
		maxD := minD + time.Duration(max(0.0, rng))*time.Millisecond
		return &scenario.Think{
			Type: "uniform",
			Min:  scenario.Duration(minD),
			Max:  scenario.Duration(maxD),
		}
	case "GaussianRandomTimer":
		if delay <= 0 && rng <= 0 {
			return nil
		}
		meanD := time.Duration(max(1.0, delay)) * time.Millisecond
		stdD := time.Duration(max(0.0, rng)) * time.Millisecond
		return &scenario.Think{
			Type:   "normal",
			Mean:   scenario.Duration(meanD),
			Stddev: scenario.Duration(stdD),
		}
	}
	return nil
}

func isSimpleTidegateCondition(s string) bool {
	s = strings.TrimSpace(s)
	for _, op := range []string{" == ", " != ", " contains ", " not_contains ", " >= ", " <= ", " > ", " < "} {
		if strings.Contains(s, op) {
			return len(strings.Split(s, op)) == 2
		}
	}
	parts := strings.Fields(s)
	return len(parts) == 2 && (parts[1] == "empty" || parts[1] == "not_empty")
}

func countLeafScenarioSteps(steps []store.ScenarioStep) int {
	total := 0
	for _, st := range steps {
		switch strings.ToLower(strings.TrimSpace(st.Type)) {
		case "", "http":
			total++
		case "group", "loop":
			total += countLeafScenarioSteps(st.Children)
		case "if":
			total += max(countLeafScenarioSteps(st.Children), countLeafScenarioSteps(st.Else))
		case "weighted":
			best := 0
			for _, br := range st.Branches {
				if n := countLeafScenarioSteps(br.Steps); n > best {
					best = n
				}
			}
			total += best
		}
	}
	return total
}

func sanitizeVarAlias(s string) string {
	var b strings.Builder
	for i, r := range strings.TrimSpace(s) {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r == '_':
			b.WriteRune(r)
		case i > 0 && ((r >= '0' && r <= '9') || r == '-'):
			b.WriteRune(r)
		default:
			if i == 0 {
				b.WriteByte('v')
			} else {
				b.WriteByte('_')
			}
		}
	}
	if b.Len() == 0 {
		return "v"
	}
	return b.String()
}
