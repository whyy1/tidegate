package scenario

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/textproto"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// CompileOptions configure compilation.
type CompileOptions struct {
	// BaseDir resolves relative data and body file paths, normally the
	// directory of the scenario file.
	BaseDir string
}

// FieldError is one validation problem.
type FieldError struct {
	Where string // human readable location, e.g. `步骤 "login"（第 1 步）的 request.url`
	Msg   string
}

func (e FieldError) String() string { return e.Where + "：" + e.Msg }

// ValidationError lists every problem found while compiling.
type ValidationError struct {
	Errors []FieldError
}

func (e *ValidationError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "场景校验失败，共 %d 个问题：", len(e.Errors))
	for _, fe := range e.Errors {
		b.WriteString("\n  - ")
		b.WriteString(fe.String())
	}
	return b.String()
}

// Defaults for omitted options.
const (
	DefaultConnectTimeout   = 5 * time.Second
	DefaultTLSTimeout       = 5 * time.Second
	DefaultFirstByteTimeout = 30 * time.Second
	DefaultTotalTimeout     = 60 * time.Second
	DefaultMaxRedirects     = 10
	DefaultGracefulStop     = 5 * time.Second
	maxAutoVUs              = 100_000
)

var (
	identRe  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_\-]*$`)
	tokenRe  = regexp.MustCompile("^[!#$%&'*+\\-.^_`|~0-9A-Za-z]+$")
	stepIDRe = regexp.MustCompile(`^[A-Za-z0-9_\-.]+$`)
)

type compiler struct {
	def     *Definition
	opts    CompileOptions
	errs    []FieldError
	plan    *Plan
	consts  map[string]string // constant variable sources
	slots   map[string]int    // mutable variable slots
	expand  map[string][]segment
	busy    map[string]bool
	dataIdx map[string]int
}

func (c *compiler) fail(where, format string, args ...any) {
	c.errs = append(c.errs, FieldError{Where: where, Msg: fmt.Sprintf(format, args...)})
}

func stepWhere(i int, id, field string) string {
	label := fmt.Sprintf("步骤 %q（第 %d 步）", id, i+1)
	if id == "" {
		label = fmt.Sprintf("第 %d 步", i+1)
	}
	if field == "" {
		return label
	}
	return label + "的 " + field
}

// Compile validates def and turns it into an execution plan. All problems
// are reported together in a *ValidationError.
func Compile(def *Definition, opts CompileOptions) (*Plan, error) {
	c := &compiler{
		def: def, opts: opts, plan: &Plan{Name: def.Name},
		consts: map[string]string{}, slots: map[string]int{},
		expand: map[string][]segment{}, busy: map[string]bool{}, dataIdx: map[string]int{},
	}
	if def.Version != 0 && def.Version != 1 {
		c.fail("version", "不支持的版本 %d，目前只支持 1", def.Version)
	}
	if strings.TrimSpace(def.Name) == "" {
		c.plan.Name = "未命名场景"
	}
	c.compileHTTP()
	c.compileData()
	c.collectVars()
	c.compileSteps()
	c.compileLoad()
	for _, ds := range c.plan.Data {
		if ds.Used && ds.Mode == DataUnique && c.plan.Load.MaxVUs > len(ds.Rows) {
			where := fmt.Sprintf("data %q", ds.Name)
			if c.plan.Load.Mode == LoadRate && c.plan.Load.MaxVUsAuto {
				c.plan.Load.MaxVUs = len(ds.Rows)
			} else {
				c.fail(where, "unique 模式要求每个虚拟用户一行，但只有 %d 行，最大虚拟用户数是 %d", len(ds.Rows), c.plan.Load.MaxVUs)
			}
		}
	}
	if len(c.errs) > 0 {
		return nil, &ValidationError{Errors: c.errs}
	}
	return c.plan, nil
}

func (c *compiler) compileHTTP() {
	o := c.def.HTTP
	h := HTTPConfig{
		KeepAlive:          boolOr(o.KeepAlive, true),
		MaxRequestsPerConn: o.MaxRequestsPerConn,
		FollowRedirects:    boolOr(o.FollowRedirects, true),
		MaxRedirects:       DefaultMaxRedirects,
		InsecureSkipVerify: o.InsecureSkipVerify,
		Cookies:            boolOr(o.Cookies, true),
		ConnectTimeout:     durOr(o.Timeout.Connect, DefaultConnectTimeout),
		TLSTimeout:         durOr(o.Timeout.TLS, DefaultTLSTimeout),
		FirstByteTimeout:   durOr(o.Timeout.FirstByte, DefaultFirstByteTimeout),
		TotalTimeout:       durOr(o.Timeout.Total, DefaultTotalTimeout),
		MaxBodyRead:        o.MaxBodyRead,
		DNSRefresh:         o.DNSRefresh.D(),
	}
	switch o.ConnMode {
	case "", "per_vu":
		h.ConnMode = ConnPerVU
	case "shared":
		h.ConnMode = ConnShared
	default:
		c.fail("http.conn_mode", "取值 %q 无效，只能是 per_vu 或 shared", o.ConnMode)
	}
	if o.MaxRedirects != nil {
		h.MaxRedirects = *o.MaxRedirects
		if h.MaxRedirects < 0 {
			c.fail("http.max_redirects", "不能是负数")
		}
	}
	if o.MaxRequestsPerConn < 0 {
		c.fail("http.max_requests_per_conn", "不能是负数")
	}
	if o.MaxBodyRead < 0 {
		c.fail("http.max_body_read", "不能是负数")
	}
	for name, d := range map[string]Duration{
		"http.timeout.connect": o.Timeout.Connect, "http.timeout.tls": o.Timeout.TLS,
		"http.timeout.first_byte": o.Timeout.FirstByte, "http.timeout.total": o.Timeout.Total,
		"http.dns_refresh": o.DNSRefresh,
	} {
		if d < 0 {
			c.fail(name, "不能是负数")
		}
	}
	if h.FirstByteTimeout > h.TotalTimeout {
		h.FirstByteTimeout = h.TotalTimeout
	}
	c.plan.HTTP = h
}

func boolOr(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

func durOr(d Duration, def time.Duration) time.Duration {
	if d == 0 {
		return def
	}
	return d.D()
}

func (c *compiler) compileData() {
	for i, src := range c.def.Data {
		where := fmt.Sprintf("data[%d]", i)
		if src.Name != "" {
			where = fmt.Sprintf("data %q", src.Name)
		}
		if !identRe.MatchString(src.Name) {
			c.fail(where+" 的 name", "名称 %q 无效，只能包含字母、数字、下划线和连字符，且不能以数字开头", src.Name)
			continue
		}
		if _, dup := c.dataIdx[src.Name]; dup {
			c.fail(where+" 的 name", "名称重复")
			continue
		}
		ds := &Dataset{Name: src.Name}
		switch src.Mode {
		case "", "sequential":
			ds.Mode = DataSequential
		case "random":
			ds.Mode = DataRandom
		case "unique":
			ds.Mode = DataUnique
		default:
			c.fail(where+" 的 mode", "取值 %q 无效，只能是 sequential、random 或 unique", src.Mode)
		}
		if strings.TrimSpace(src.File) == "" {
			c.fail(where+" 的 file", "不能为空")
			continue
		}
		path := resolvePath(c.opts.BaseDir, src.File)
		if !strings.EqualFold(pathExt(path), ".csv") {
			c.fail(where+" 的 file", "目前只支持 CSV 文件")
			continue
		}
		cols, rows, err := LoadCSV(path)
		if err != nil {
			c.fail(where+" 的 file", "读取 %s 失败：%v", src.File, err)
			continue
		}
		ds.Columns, ds.Rows = cols, rows
		c.dataIdx[src.Name] = len(c.plan.Data)
		c.plan.Data = append(c.plan.Data, ds)
	}
}

func pathExt(p string) string {
	if i := strings.LastIndexByte(p, '.'); i >= 0 && !strings.ContainsAny(p[i:], `/\`) {
		return p[i:]
	}
	return ""
}

// collectVars splits variables into constants (inlined into templates) and
// mutable ones (targets of extractors, stored per virtual user).
func (c *compiler) collectVars() {
	names := make([]string, 0, len(c.def.Variables))
	for name := range c.def.Variables {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !identRe.MatchString(name) {
			c.fail(fmt.Sprintf("variables.%s", name), "变量名无效，只能包含字母、数字、下划线和连字符，且不能以数字开头")
			continue
		}
		c.consts[name] = string(c.def.Variables[name])
	}
	for i, st := range c.def.Steps {
		for j, ex := range st.Extract {
			if !identRe.MatchString(ex.Name) {
				c.fail(stepWhere(i, st.ID, fmt.Sprintf("extract[%d].name", j)), "变量名 %q 无效，只能包含字母、数字、下划线和连字符，且不能以数字开头", ex.Name)
				continue
			}
			if _, ok := c.slots[ex.Name]; ok {
				continue
			}
			c.slots[ex.Name] = len(c.plan.Vars)
			c.plan.Vars = append(c.plan.Vars, ex.Name)
			init := ""
			if v, ok := c.consts[ex.Name]; ok {
				if strings.Contains(v, "{{") {
					c.fail("variables."+ex.Name, "这个变量会被提取器改写，初始值不能包含模板")
				}
				init = v
				delete(c.consts, ex.Name)
			}
			c.plan.VarInit = append(c.plan.VarInit, init)
		}
	}
}

// resolveVar implements templateResolver.
func (c *compiler) resolveVar(name string, escape escapeMode, depth int) ([]segment, error) {
	if slot, ok := c.slots[name]; ok {
		return []segment{{kind: segVar, escape: escape, a: int64(slot)}}, nil
	}
	src, ok := c.consts[name]
	if !ok {
		if _, isData := c.dataIdx[name]; isData {
			return nil, errf("%q 是数据文件，请用 {{%s.列名}} 引用某一列", name, name)
		}
		return nil, errf("引用了未定义的变量 %q", name)
	}
	key := fmt.Sprintf("%d/%s", escape, name)
	if segs, ok := c.expand[key]; ok {
		return segs, nil
	}
	if c.busy[name] || depth > 32 {
		return nil, errf("变量 %q 存在循环引用", name)
	}
	c.busy[name] = true
	segs, err := parseTemplate(src, c, escape, depth+1)
	delete(c.busy, name)
	if err != nil {
		return nil, err
	}
	c.expand[key] = segs
	return segs, nil
}

// resolveData implements templateResolver.
func (c *compiler) resolveData(ds, col string) (int, int, error) {
	di, ok := c.dataIdx[ds]
	if !ok {
		return 0, 0, errf("引用了未定义的数据文件 %q", ds)
	}
	d := c.plan.Data[di]
	ci := d.Column(col)
	if ci < 0 {
		return 0, 0, errf("数据文件 %q 没有列 %q（现有列：%s）", ds, col, strings.Join(d.Columns, "、"))
	}
	d.Used = true
	return di, ci, nil
}

func (c *compiler) template(where, src string, escape escapeMode) *Template {
	segs, err := parseTemplate(src, c, escape, 0)
	if err != nil {
		c.fail(where, "%v", err)
		return StaticTemplate("")
	}
	return newTemplate(src, segs)
}

func (c *compiler) compileSteps() {
	if len(c.def.Steps) == 0 {
		c.fail("steps", "至少需要一个步骤")
		return
	}
	ids := map[string]int{}
	for i := range c.def.Steps {
		st := &c.def.Steps[i]
		if st.ID == "" {
			st.ID = fmt.Sprintf("step%d", i+1)
		} else if !stepIDRe.MatchString(st.ID) {
			c.fail(stepWhere(i, st.ID, "id"), "只能包含字母、数字、下划线、连字符和点")
		}
		if prev, dup := ids[st.ID]; dup {
			c.fail(stepWhere(i, st.ID, "id"), "和第 %d 步重复", prev+1)
		}
		ids[st.ID] = i
		if st.Request == nil {
			c.fail(stepWhere(i, st.ID, "request"), "缺少 request")
			continue
		}
		c.plan.Steps = append(c.plan.Steps, c.compileHTTPStep(i, st))
	}
}

func (c *compiler) compileHTTPStep(i int, st *Step) *HTTPStep {
	w := func(field string) string { return stepWhere(i, st.ID, field) }
	req := st.Request
	name := st.Name
	if name == "" {
		name = st.ID
	}
	s := &HTTPStep{StepInfo: StepInfo{Index: len(c.plan.Steps), ID: st.ID, Name: name}}

	s.Method = strings.ToUpper(strings.TrimSpace(req.Method))
	if s.Method == "" {
		s.Method = "GET"
	}
	if !tokenRe.MatchString(s.Method) {
		c.fail(w("request.method"), "请求方法 %q 无效", req.Method)
	}

	// URL plus query parameters form one template.
	rawURL := strings.TrimSpace(req.URL)
	if rawURL == "" {
		c.fail(w("request.url"), "不能为空")
	} else {
		parts := []*Template{c.template(w("request.url"), rawURL, escapeNone)}
		if len(req.Query) > 0 {
			sep := "?"
			if strings.Contains(rawURL, "?") {
				sep = "&"
			}
			for _, k := range sortedKeys(req.Query) {
				parts = append(parts, StaticTemplate(sep+queryEscape(k)+"="))
				parts = append(parts, c.template(w("request.query."+k), string(req.Query[k]), escapeQuery))
				sep = "&"
			}
		}
		s.URL = c.compileURL(w("request.url"), concatTemplates(parts...))
	}

	// Headers in a stable order.
	hasContentType := false
	for _, k := range sortedKeys(req.Headers) {
		name := strings.TrimSpace(k)
		if !tokenRe.MatchString(name) {
			c.fail(w("request.headers"), "请求头名称 %q 无效", k)
			continue
		}
		canon := textproto.CanonicalMIMEHeaderKey(name)
		switch canon {
		case "Content-Length", "Transfer-Encoding":
			c.fail(w("request.headers."+name), "由引擎根据请求体自动生成，不能手动设置")
			continue
		case "Content-Type":
			hasContentType = true
		case "User-Agent":
			s.HasUserAgent = true
		case "Accept-Encoding":
			s.HasAcceptEncoding = true
		case "Connection":
			s.HasConnection = true
		case "Host":
			s.HasHost = true
		}
		val := req.Headers[k]
		if strings.ContainsAny(val, "\r\n") {
			c.fail(w("request.headers."+name), "值不能包含换行")
			continue
		}
		s.Headers = append(s.Headers, HeaderTemplate{Name: name, Canonical: canon, Value: c.template(w("request.headers."+name), val, escapeNone)})
	}

	if req.Body != nil {
		s.Body, s.ContentType = c.compileBody(w, req.Body)
	}
	if hasContentType {
		s.ContentType = ""
	}

	for j, ex := range st.Extract {
		if e, ok := c.compileExtract(w(fmt.Sprintf("extract[%d]", j)), ex); ok {
			if e.Source != ExtractHeader {
				s.NeedBody = true
			}
			s.Extract = append(s.Extract, e)
		}
	}
	for j, as := range st.Assert {
		if a, ok := c.compileAssert(w(fmt.Sprintf("assert[%d]", j)), as); ok {
			switch a.Kind {
			case AssertBodyContains, AssertJSONPath:
				s.NeedBody = true
			case AssertStatus:
				s.HasStatusAssert = true
			}
			s.Assert = append(s.Assert, a)
		}
	}
	if st.Think != nil {
		s.Think = c.compileThink(w("think"), st.Think)
		c.plan.ThinkMax += s.Think.Max
	}
	return s
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (c *compiler) compileURL(where string, t *Template) URLTemplate {
	if t.Static() {
		tg, err := ParseTarget(t.Literal())
		if err != nil {
			c.fail(where, "%v", err)
			return URLTemplate{}
		}
		return URLTemplate{Static: tg}
	}
	// Static origin with a dynamic path: split at the first '/' after the
	// authority if everything before it is literal.
	if first := t.segs[0]; first.kind == segLit {
		lit := first.lit
		if i := strings.Index(lit, "://"); i > 0 {
			rest := lit[i+3:]
			if j := strings.IndexAny(rest, "/?"); j >= 0 {
				tg, err := ParseTarget(lit[:i+3+j])
				if err != nil {
					c.fail(where, "%v", err)
					return URLTemplate{}
				}
				pathSegs := append([]segment{}, t.segs...)
				pathSegs[0].lit = rest[j:]
				if strings.HasPrefix(rest[j:], "?") {
					pathSegs[0].lit = "/" + rest[j:]
				}
				return URLTemplate{Origin: tg.Origin, Path: newTemplate(t.src, pathSegs)}
			}
		} else {
			c.fail(where, "URL 必须以 http:// 或 https:// 开头")
			return URLTemplate{}
		}
	}
	return URLTemplate{Full: t}
}

func (c *compiler) compileBody(w func(string) string, b *Body) (*Template, string) {
	switch b.Type {
	case "", "none":
		if b.Content != "" || len(b.Fields) > 0 || len(b.Files) > 0 {
			c.fail(w("request.body.type"), "type 为 none 时不能设置 content、fields 或 files")
		}
		return nil, ""
	case "json":
		t := c.template(w("request.body.content"), b.Content, escapeNone)
		if t.Static() && strings.TrimSpace(t.Literal()) != "" && !json.Valid([]byte(t.Literal())) {
			c.fail(w("request.body.content"), "不是合法的 JSON")
		}
		c.noFields(w, b)
		return t, "application/json"
	case "raw":
		c.noFields(w, b)
		return c.template(w("request.body.content"), b.Content, escapeNone), ""
	case "form":
		if len(b.Files) > 0 {
			c.fail(w("request.body.files"), "form 类型不支持文件，请改用 multipart")
		}
		if len(b.Fields) == 0 {
			return c.template(w("request.body.content"), b.Content, escapeNone), "application/x-www-form-urlencoded"
		}
		if b.Content != "" {
			c.fail(w("request.body"), "form 类型的 content 和 fields 只能二选一")
		}
		var parts []*Template
		for i, k := range sortedKeys(b.Fields) {
			sep := "&"
			if i == 0 {
				sep = ""
			}
			parts = append(parts, StaticTemplate(sep+queryEscape(k)+"="))
			parts = append(parts, c.template(w("request.body.fields."+k), string(b.Fields[k]), escapeQuery))
		}
		return concatTemplates(parts...), "application/x-www-form-urlencoded"
	case "multipart":
		if b.Content != "" {
			c.fail(w("request.body.content"), "multipart 类型请用 fields 和 files")
		}
		var rnd [12]byte
		_, _ = rand.Read(rnd[:])
		boundary := "TidegateBoundary" + hex.EncodeToString(rnd[:])
		var parts []*Template
		for _, k := range sortedKeys(b.Fields) {
			parts = append(parts, StaticTemplate("--"+boundary+"\r\nContent-Disposition: form-data; name=\""+escapeQuotes(k)+"\"\r\n\r\n"))
			parts = append(parts, c.template(w("request.body.fields."+k), string(b.Fields[k]), escapeNone))
			parts = append(parts, StaticTemplate("\r\n"))
		}
		for j, f := range b.Files {
			fw := w(fmt.Sprintf("request.body.files[%d]", j))
			if f.Field == "" || f.Path == "" {
				c.fail(fw, "field 和 path 都不能为空")
				continue
			}
			data, err := os.ReadFile(resolvePath(c.opts.BaseDir, f.Path))
			if err != nil {
				c.fail(fw+".path", "读取文件失败：%v", err)
				continue
			}
			fname := f.Filename
			if fname == "" {
				fname = baseName(f.Path)
			}
			ct := f.ContentType
			if ct == "" {
				ct = "application/octet-stream"
			}
			parts = append(parts, StaticTemplate("--"+boundary+"\r\nContent-Disposition: form-data; name=\""+escapeQuotes(f.Field)+
				"\"; filename=\""+escapeQuotes(fname)+"\"\r\nContent-Type: "+ct+"\r\n\r\n"+string(data)+"\r\n"))
		}
		parts = append(parts, StaticTemplate("--"+boundary+"--\r\n"))
		return concatTemplates(parts...), "multipart/form-data; boundary=" + boundary
	}
	c.fail(w("request.body.type"), "取值 %q 无效，只能是 none、json、form、raw 或 multipart", b.Type)
	return nil, ""
}

func (c *compiler) noFields(w func(string) string, b *Body) {
	if len(b.Fields) > 0 || len(b.Files) > 0 {
		c.fail(w("request.body"), "%s 类型只使用 content，不能设置 fields 或 files", b.Type)
	}
}

func escapeQuotes(s string) string { return strings.ReplaceAll(s, `"`, `\"`) }

func baseName(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}

func (c *compiler) compileExtract(where string, ex Extract) (Extractor, bool) {
	slot, ok := c.slots[ex.Name]
	if !ok {
		return Extractor{}, false // invalid name, already reported
	}
	e := Extractor{Name: ex.Name, Slot: slot, Expr: ex.Expr}
	if ex.Default != nil {
		e.Default, e.HasDefault = *ex.Default, true
	}
	if strings.TrimSpace(ex.Expr) == "" {
		c.fail(where+".expr", "不能为空")
		return e, false
	}
	switch ex.From {
	case "jsonpath":
		p, err := CompileJSONPath(ex.Expr)
		if err != nil {
			c.fail(where+".expr", "%v", err)
			return e, false
		}
		e.Source, e.Path = ExtractJSONPath, p
	case "regex":
		re, err := regexp.Compile(ex.Expr)
		if err != nil {
			c.fail(where+".expr", "正则表达式无效：%v", err)
			return e, false
		}
		e.Source, e.Regex = ExtractRegex, re
		if re.NumSubexp() > 0 {
			e.Group = 1
		}
		if ex.Group != nil {
			e.Group = *ex.Group
			if e.Group < 0 || e.Group > re.NumSubexp() {
				c.fail(where+".group", "分组 %d 不存在，表达式共有 %d 个分组", e.Group, re.NumSubexp())
				return e, false
			}
		}
	case "header":
		e.Source, e.Header = ExtractHeader, textproto.CanonicalMIMEHeaderKey(strings.TrimSpace(ex.Expr))
	default:
		c.fail(where+".from", "取值 %q 无效，只能是 jsonpath、regex 或 header", ex.From)
		return e, false
	}
	if ex.Group != nil && ex.From != "regex" {
		c.fail(where+".group", "只有 regex 提取器可以设置 group")
	}
	return e, true
}

func (c *compiler) compileAssert(where string, as Assert) (Assertion, bool) {
	a := Assertion{}
	switch as.Type {
	case "status":
		a.Kind = AssertStatus
		if as.Equals != nil {
			n, err := strconv.Atoi(strings.TrimSpace(string(*as.Equals)))
			if err != nil {
				c.fail(where+".equals", "状态码 %q 不是整数", string(*as.Equals))
				return a, false
			}
			a.Statuses = append(a.Statuses, n)
		}
		a.Statuses = append(a.Statuses, as.In...)
		if len(a.Statuses) == 0 {
			c.fail(where, "status 断言需要 equals 或 in")
			return a, false
		}
		for _, n := range a.Statuses {
			if n < 100 || n > 999 {
				c.fail(where, "状态码 %d 超出范围 100-999", n)
				return a, false
			}
		}
		if len(a.Statuses) == 1 {
			a.Desc = fmt.Sprintf("状态码应为 %d", a.Statuses[0])
		} else {
			a.Desc = fmt.Sprintf("状态码应为 %v 之一", a.Statuses)
		}
	case "duration":
		a.Kind = AssertDuration
		if as.Max <= 0 {
			c.fail(where+".max", "duration 断言需要大于 0 的 max，例如 500ms")
			return a, false
		}
		a.MaxDuration = as.Max.D()
		a.Desc = fmt.Sprintf("耗时应不超过 %v", a.MaxDuration)
	case "body_contains":
		a.Kind = AssertBodyContains
		if as.Value == "" {
			c.fail(where+".value", "body_contains 断言需要非空的 value")
			return a, false
		}
		a.Contains = []byte(as.Value)
		a.Desc = fmt.Sprintf("响应体应包含 %q", truncate(as.Value, 64))
	case "jsonpath":
		a.Kind = AssertJSONPath
		p, err := CompileJSONPath(as.Expr)
		if err != nil {
			c.fail(where+".expr", "%v", err)
			return a, false
		}
		a.Path = p
		n := 0
		if as.Equals != nil {
			a.Equals, a.HasEquals = string(*as.Equals), true
			a.Desc = fmt.Sprintf("%s 应等于 %q", p.src, a.Equals)
			n++
		}
		if as.Exists != nil {
			a.Exists, a.HasExists = *as.Exists, true
			if a.Exists {
				a.Desc = fmt.Sprintf("%s 应存在", p.src)
			} else {
				a.Desc = fmt.Sprintf("%s 不应存在", p.src)
			}
			n++
		}
		if as.Contains != nil {
			a.Contains, a.HasContains = []byte(*as.Contains), true
			a.Desc = fmt.Sprintf("%s 应包含 %q", p.src, *as.Contains)
			n++
		}
		if n != 1 {
			c.fail(where, "jsonpath 断言需要且只能设置 equals、exists、contains 中的一个")
			return a, false
		}
	default:
		c.fail(where+".type", "取值 %q 无效，只能是 status、duration、body_contains 或 jsonpath", as.Type)
		return a, false
	}
	return a, true
}

func (c *compiler) compileThink(where string, t *Think) ThinkTime {
	th := ThinkTime{Min: t.Min.D(), Max: t.Max.D()}
	if th.Min < 0 || th.Max < 0 {
		c.fail(where, "思考时间不能是负数")
	}
	switch t.Type {
	case "fixed", "":
		th.Kind = ThinkFixed
		if th.Min == 0 {
			th.Min = th.Max
		}
		th.Max = th.Min
	case "uniform":
		th.Kind = ThinkUniform
		if th.Max < th.Min {
			c.fail(where+".max", "uniform 的 max 不能小于 min")
		}
	default:
		c.fail(where+".type", "取值 %q 无效，只能是 fixed 或 uniform", t.Type)
	}
	if th.Max == 0 {
		th.Kind = ThinkNone
	}
	return th
}

func (c *compiler) compileLoad() {
	l := c.def.Load
	lp := LoadPlan{MaxRequests: l.MaxRequests, GracefulStop: DefaultGracefulStop}
	switch l.Mode {
	case "vus", "":
		lp.Mode = LoadVUs
	case "rate":
		lp.Mode = LoadRate
	default:
		c.fail("load.mode", "取值 %q 无效，只能是 vus 或 rate", l.Mode)
	}
	if l.GracefulStop != nil {
		lp.GracefulStop = l.GracefulStop.D()
		if lp.GracefulStop < 0 {
			c.fail("load.graceful_stop", "不能是负数")
		}
	}
	if l.MaxRequests < 0 {
		c.fail("load.max_requests", "不能是负数")
	}
	if len(l.Stages) == 0 {
		c.fail("load.stages", "至少需要一个阶段")
	}
	for i, st := range l.Stages {
		where := fmt.Sprintf("load.stages[%d]", i)
		if st.Duration < 0 {
			c.fail(where+".duration", "不能是负数")
		}
		if st.Target < 0 || math.IsNaN(st.Target) || math.IsInf(st.Target, 0) {
			c.fail(where+".target", "必须是非负数")
		}
		if lp.Mode == LoadVUs && st.Target != math.Trunc(st.Target) {
			c.fail(where+".target", "vus 模式的目标并发必须是整数")
		}
		if lp.Mode == LoadVUs && st.Target > 1_000_000 {
			c.fail(where+".target", "单个节点最多 100 万虚拟用户")
		}
		lp.Stages = append(lp.Stages, StageSpec{Duration: st.Duration.D(), Target: st.Target})
		lp.TotalDuration += st.Duration.D()
		lp.PeakTarget = max(lp.PeakTarget, st.Target)
	}
	if len(l.Stages) > 0 && lp.TotalDuration <= 0 {
		c.fail("load.stages", "阶段总时长必须大于 0")
	}
	if len(l.Stages) > 0 && lp.PeakTarget <= 0 {
		c.fail("load.stages", "至少一个阶段的 target 要大于 0")
	}
	switch lp.Mode {
	case LoadVUs:
		if l.MaxVUs != 0 {
			c.fail("load.max_vus", "只在 rate 模式下使用")
		}
		lp.MaxVUs = int(lp.PeakTarget)
	case LoadRate:
		if l.MaxVUs < 0 {
			c.fail("load.max_vus", "不能是负数")
		}
		lp.MaxVUs = l.MaxVUs
		if lp.MaxVUs == 0 {
			// Enough virtual users for iterations lasting up to the think
			// time plus two seconds of response time at the peak rate.
			lp.MaxVUsAuto = true
			need := lp.PeakTarget * (c.plan.ThinkMax.Seconds() + 2)
			lp.MaxVUs = int(min(max(math.Ceil(need), 10), maxAutoVUs))
		}
	}
	c.plan.Load = lp
}
