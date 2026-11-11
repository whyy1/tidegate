package scenario

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"net/url"
	"regexp"
	"strconv"
	"time"
)

// Plan is the compiled, immutable form of a Definition. It is safe for
// concurrent use by any number of virtual users.
type Plan struct {
	Name  string
	Steps []Node
	// Vars are the names of mutable variables (targets of extractors);
	// VarInit holds their initial values. Constant variables are inlined
	// into templates and do not appear here.
	Vars    []string
	VarInit []string
	Data    []*Dataset
	HTTP    HTTPConfig
	Load    LoadPlan
	// ThinkMax is the longest think time of one iteration.
	ThinkMax time.Duration
}

// NodeKind is the type of a step node.
type NodeKind uint8

const (
	// NodeHTTP is an HTTP request step. Future kinds (groups, loops,
	// conditions, other protocols) are added here.
	NodeHTTP NodeKind = iota + 1
)

// StepInfo identifies a step.
type StepInfo struct {
	Index int // position in Plan.Steps, also the metrics step index
	ID    string
	Name  string
}

// Node is an executable element of a scenario.
type Node interface {
	Kind() NodeKind
	Info() StepInfo
}

// ConnMode selects how virtual users get connections.
type ConnMode uint8

const (
	ConnPerVU  ConnMode = iota // every virtual user owns its connections
	ConnShared                 // connections are pooled across virtual users
)

func (m ConnMode) String() string {
	if m == ConnShared {
		return "shared"
	}
	return "per_vu"
}

// HTTPConfig are the resolved connection options.
type HTTPConfig struct {
	ConnMode           ConnMode
	KeepAlive          bool
	MaxRequestsPerConn int
	FollowRedirects    bool
	MaxRedirects       int
	InsecureSkipVerify bool
	Cookies            bool
	ConnectTimeout     time.Duration
	TLSTimeout         time.Duration
	FirstByteTimeout   time.Duration
	TotalTimeout       time.Duration
	// MaxBodyRead caps how many body bytes are kept for extractors and
	// assertions. The rest of the body is still read (and counted) so the
	// connection stays reusable. 0 keeps the whole body when needed.
	MaxBodyRead int64
	DNSRefresh  time.Duration
}

// LoadMode is the load model.
type LoadMode uint8

const (
	LoadVUs  LoadMode = iota // closed model: target is a number of virtual users
	LoadRate                 // open model: target is iterations per second
)

func (m LoadMode) String() string {
	if m == LoadRate {
		return "rate"
	}
	return "vus"
}

// StageSpec ramps linearly from the previous target (0 at the start) to Target.
type StageSpec struct {
	Duration time.Duration
	Target   float64
}

// LoadPlan is the resolved load model.
type LoadPlan struct {
	Mode          LoadMode
	Stages        []StageSpec
	MaxVUs        int // rate mode: resolved virtual user limit
	MaxVUsAuto    bool
	MaxRequests   int64
	GracefulStop  time.Duration
	TotalDuration time.Duration
	PeakTarget    float64
}

// Origin is the scheme and authority of a URL, prepared for dialing.
type Origin struct {
	Scheme   string // "http" or "https"
	TLS      bool
	Host     string // Host header value
	HostName string // name for DNS and TLS SNI
	Port     string
	Addr     string // HostName:Port
}

// NewOrigin builds an origin from a scheme and an authority (host[:port]).
func NewOrigin(scheme, authority string) (*Origin, error) {
	o := &Origin{Scheme: scheme}
	switch scheme {
	case "http":
	case "https":
		o.TLS = true
	default:
		return nil, fmt.Errorf("不支持的协议 %q，只支持 http 和 https", scheme)
	}
	if authority == "" {
		return nil, fmt.Errorf("URL 缺少主机名")
	}
	u := &url.URL{Host: authority}
	o.HostName = u.Hostname()
	o.Port = u.Port()
	if o.HostName == "" {
		return nil, fmt.Errorf("URL 缺少主机名")
	}
	defPort := "80"
	if o.TLS {
		defPort = "443"
	}
	if o.Port == "" {
		o.Port = defPort
	} else if n, err := strconv.Atoi(o.Port); err != nil || n < 1 || n > 65535 {
		return nil, fmt.Errorf("端口 %q 无效", o.Port)
	}
	o.Host = authority
	if o.Port == defPort {
		o.Host = trimDefaultPort(authority, defPort)
	}
	o.Addr = joinHostPort(o.HostName, o.Port)
	return o, nil
}

func trimDefaultPort(authority, port string) string {
	suffix := ":" + port
	if len(authority) > len(suffix) && authority[len(authority)-len(suffix):] == suffix {
		return authority[:len(authority)-len(suffix)]
	}
	return authority
}

func joinHostPort(host, port string) string {
	for i := 0; i < len(host); i++ {
		if host[i] == ':' {
			return "[" + host + "]:" + port
		}
	}
	return host + ":" + port
}

// Target is a fully resolved request URL.
type Target struct {
	*Origin
	RequestURI string
	URL        *url.URL
}

// ParseTarget parses an absolute http or https URL.
func ParseTarget(raw string) (*Target, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("URL %q 无效：%v", raw, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("URL %q 必须是带协议和主机名的完整地址，例如 http://127.0.0.1:8080/path", raw)
	}
	if u.User != nil {
		return nil, fmt.Errorf("URL %q 不支持用户名密码写法，请改用 Authorization 请求头", raw)
	}
	o, err := NewOrigin(u.Scheme, u.Host)
	if err != nil {
		return nil, fmt.Errorf("URL %q：%v", raw, err)
	}
	u.Fragment, u.RawFragment = "", ""
	return &Target{Origin: o, RequestURI: u.RequestURI(), URL: u}, nil
}

// SplitURL splits a rendered absolute URL into scheme, authority and
// request URI without allocating. Used for fully dynamic URLs.
func SplitURL(raw []byte) (scheme, authority, requestURI []byte, ok bool) {
	i := bytes.Index(raw, []byte("://"))
	if i <= 0 {
		return nil, nil, nil, false
	}
	scheme = raw[:i]
	rest := raw[i+3:]
	end := len(rest)
	for j, c := range rest {
		if c == '/' || c == '?' || c == '#' {
			end = j
			break
		}
	}
	authority = rest[:end]
	requestURI = rest[end:]
	if h := bytes.IndexByte(requestURI, '#'); h >= 0 {
		requestURI = requestURI[:h]
	}
	if len(authority) == 0 || bytes.IndexByte(authority, '@') >= 0 {
		return nil, nil, nil, false
	}
	return scheme, authority, requestURI, true
}

// URLTemplate is a request URL prepared for fast rendering. Exactly one
// form is set: Static (nothing dynamic), Origin+Path (static origin,
// dynamic path or query) or Full (dynamic origin).
type URLTemplate struct {
	Static *Target
	Origin *Origin
	Path   *Template
	Full   *Template
}

// HeaderTemplate is a request header with a templated value.
type HeaderTemplate struct {
	Name      string // as written by the user
	Canonical string // canonical MIME form, used by net/http
	Value     *Template
}

// HTTPStep is a compiled HTTP request step.
type HTTPStep struct {
	StepInfo
	Method      string
	URL         URLTemplate
	Headers     []HeaderTemplate
	Body        *Template // nil when there is no body
	ContentType string    // default Content-Type when the user set none
	Extract     []Extractor
	Assert      []Assertion
	Think       ThinkTime
	// NeedBody is set when an extractor or assertion reads the body.
	NeedBody bool
	// HasStatusAssert disables the default "4xx/5xx is an error" rule:
	// the status assertion decides instead.
	HasStatusAssert bool
	// Flags for headers the engine would otherwise add itself.
	HasUserAgent      bool
	HasAcceptEncoding bool
	HasConnection     bool
	HasHost           bool
}

// Kind implements Node.
func (s *HTTPStep) Kind() NodeKind { return NodeHTTP }

// Info implements Node.
func (s *HTTPStep) Info() StepInfo { return s.StepInfo }

// ThinkKind is the think time distribution.
type ThinkKind uint8

const (
	ThinkNone ThinkKind = iota
	ThinkFixed
	ThinkUniform
)

// ThinkTime is a compiled think time.
type ThinkTime struct {
	Kind ThinkKind
	Min  time.Duration
	Max  time.Duration
}

// Duration draws one think time.
func (t ThinkTime) Duration(r *rand.Rand) time.Duration {
	switch t.Kind {
	case ThinkFixed:
		return t.Min
	case ThinkUniform:
		if t.Max <= t.Min {
			return t.Min
		}
		return t.Min + time.Duration(r.Int64N(int64(t.Max-t.Min)+1))
	}
	return 0
}

// ExtractSource is where an extractor reads from.
type ExtractSource uint8

const (
	ExtractJSONPath ExtractSource = iota
	ExtractRegex
	ExtractHeader
)

// Extractor stores part of a response in a mutable variable.
type Extractor struct {
	Name       string
	Slot       int
	Source     ExtractSource
	Expr       string
	Path       *JSONPath
	Regex      *regexp.Regexp
	Group      int
	Header     string
	Default    string
	HasDefault bool
}

// ResponseView gives extractors and assertions access to a response.
type ResponseView struct {
	Status   int
	Duration time.Duration
	Body     []byte
	// Header returns the first value of a response header (case-insensitive).
	Header func(name string) ([]byte, bool)
}

// Extract appends the extracted value to dst. ok is false when the value
// was not found; the default value (if any) is then appended instead and
// ok reports whether a default existed.
func (e *Extractor) Extract(dst []byte, r *ResponseView) ([]byte, bool) {
	switch e.Source {
	case ExtractJSONPath:
		if raw, ok := e.Path.Lookup(r.Body); ok {
			if out, ok := AppendJSONText(dst, raw); ok {
				return out, true
			}
		}
	case ExtractRegex:
		if m := e.Regex.FindSubmatchIndex(r.Body); m != nil && m[2*e.Group] >= 0 {
			return append(dst, r.Body[m[2*e.Group]:m[2*e.Group+1]]...), true
		}
	case ExtractHeader:
		if r.Header != nil {
			if v, ok := r.Header(e.Header); ok {
				return append(dst, v...), true
			}
		}
	}
	if e.HasDefault {
		return append(dst, e.Default...), true
	}
	return dst, false
}

// AssertKind is the type of an assertion.
type AssertKind uint8

const (
	AssertStatus AssertKind = iota
	AssertDuration
	AssertBodyContains
	AssertJSONPath
)

// Assertion is a compiled response check.
type Assertion struct {
	Kind        AssertKind
	Statuses    []int
	MaxDuration time.Duration
	Contains    []byte
	Path        *JSONPath
	Equals      string
	HasEquals   bool
	Exists      bool
	HasExists   bool
	HasContains bool
	// Desc describes the expectation for error messages.
	Desc string
}

// Check evaluates the assertion. msg explains a failure.
func (a *Assertion) Check(r *ResponseView) (ok bool, msg string) {
	switch a.Kind {
	case AssertStatus:
		for _, s := range a.Statuses {
			if r.Status == s {
				return true, ""
			}
		}
		return false, fmt.Sprintf("断言失败：%s，实际状态码 %d", a.Desc, r.Status)
	case AssertDuration:
		if r.Duration <= a.MaxDuration {
			return true, ""
		}
		return false, fmt.Sprintf("断言失败：%s，实际 %v", a.Desc, r.Duration.Round(time.Microsecond))
	case AssertBodyContains:
		if bytes.Contains(r.Body, a.Contains) {
			return true, ""
		}
		return false, fmt.Sprintf("断言失败：%s", a.Desc)
	case AssertJSONPath:
		raw, found := a.Path.Lookup(r.Body)
		if a.HasExists {
			if found == a.Exists {
				return true, ""
			}
			return false, fmt.Sprintf("断言失败：%s", a.Desc)
		}
		if !found {
			return false, fmt.Sprintf("断言失败：%s，路径不存在", a.Desc)
		}
		var buf [128]byte
		text, _ := AppendJSONText(buf[:0], raw)
		if a.HasEquals {
			if jsonValueEquals(text, raw, a.Equals) {
				return true, ""
			}
			return false, fmt.Sprintf("断言失败：%s，实际值 %s", a.Desc, truncate(string(text), 64))
		}
		if a.HasContains {
			if bytes.Contains(text, a.Contains) {
				return true, ""
			}
			return false, fmt.Sprintf("断言失败：%s，实际值 %s", a.Desc, truncate(string(text), 64))
		}
	}
	return true, ""
}

func jsonValueEquals(text, raw []byte, want string) bool {
	if string(text) == want {
		return true
	}
	// Compare numbers numerically so that 1 equals 1.0.
	if len(raw) > 0 && (raw[0] == '-' || raw[0] >= '0' && raw[0] <= '9') {
		a, err1 := strconv.ParseFloat(string(text), 64)
		b, err2 := strconv.ParseFloat(want, 64)
		return err1 == nil && err2 == nil && a == b
	}
	return false
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
