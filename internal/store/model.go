package store

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"tidegate/internal/scenario"
)

// KeyValue is an ordered key-value row with UI metadata (description,
// disabled state, secret flag). When Description is empty and Disabled and
// Secret are false, it serializes cleanly to two YAML lines.
type KeyValue struct {
	Name        string          `yaml:"name" json:"name"`
	Value       scenario.Scalar `yaml:"value" json:"value"`
	Description string          `yaml:"description,omitempty" json:"description,omitempty"`
	Disabled    bool            `yaml:"disabled,omitempty" json:"disabled,omitempty"`
	Secret      bool            `yaml:"secret,omitempty" json:"secret,omitempty"`
}

// KeyValueList is an ordered slice of KeyValue entries. For backwards
// compatibility with hand-written YAML/JSON maps, unmarshaling accepts both
// an array of objects and a key-value mapping.
type KeyValueList []KeyValue

// UnmarshalYAML implements yaml.Unmarshaler.
func (l *KeyValueList) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.SequenceNode:
		var items []KeyValue
		if err := n.Decode(&items); err != nil {
			return err
		}
		*l = items
		return nil
	case yaml.MappingNode:
		var m map[string]scenario.Scalar
		if err := n.Decode(&m); err != nil {
			return err
		}
		out := make([]KeyValue, 0, len(m))
		for _, k := range sortedKeys(m) {
			out = append(out, KeyValue{Name: k, Value: m[k]})
		}
		*l = out
		return nil
	case yaml.ScalarNode:
		if n.Tag == "!!null" {
			*l = nil
			return nil
		}
	}
	return fmt.Errorf("第 %d 行：需要列表或键值映射", n.Line)
}

// UnmarshalJSON implements json.Unmarshaler.
func (l *KeyValueList) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "null" {
		*l = nil
		return nil
	}
	if strings.HasPrefix(s, "{") {
		var m map[string]scenario.Scalar
		if err := json.Unmarshal(b, &m); err != nil {
			return err
		}
		out := make([]KeyValue, 0, len(m))
		for _, k := range sortedKeys(m) {
			out = append(out, KeyValue{Name: k, Value: m[k]})
		}
		*l = out
		return nil
	}
	var items []KeyValue
	if err := json.Unmarshal(b, &items); err != nil {
		return err
	}
	*l = items
	return nil
}

// Project is the content of project.yaml.
type Project struct {
	ID          string       `yaml:"id" json:"id"`
	Name        string       `yaml:"name" json:"name"`
	Description string       `yaml:"description,omitempty" json:"description,omitempty"`
	Variables   KeyValueList `yaml:"variables,omitempty" json:"variables,omitempty"`
	// Version is the content hash of the stored file. Updates must send
	// back the version they read.
	Version string `yaml:"-" json:"version"`
}

// Authentication types of Auth.Type.
const (
	// AuthInherit (or an empty type) uses the auth of the nearest folder
	// that sets one.
	AuthInherit = "inherit"
	// AuthNone sends no credentials and stops inheritance.
	AuthNone = "none"
	// AuthBearer sends "Authorization: Bearer <token>".
	AuthBearer = "bearer"
	// AuthBasic sends HTTP basic credentials.
	AuthBasic = "basic"
	// AuthAPIKey sends a key in a header or a query parameter.
	AuthAPIKey = "api_key"
	// AuthOAuth2 obtains and caches tokens via OAuth 2.0 Client Credentials.
	AuthOAuth2 = "oauth2"
)

// Auth describes request credentials. Values may contain {{variables}}.
type Auth struct {
	Type     string `yaml:"type" json:"type"`
	Token    string `yaml:"token,omitempty" json:"token,omitempty"`
	Username string `yaml:"username,omitempty" json:"username,omitempty"`
	Password string `yaml:"password,omitempty" json:"password,omitempty"`
	// Key and Value are the parameter name and value of an api_key auth.
	Key   string `yaml:"key,omitempty" json:"key,omitempty"`
	Value string `yaml:"value,omitempty" json:"value,omitempty"`
	// In is "header" (default) or "query" for api_key auth.
	In string `yaml:"in,omitempty" json:"in,omitempty"`
	// OAuth 2.0 Client Credentials settings.
	TokenURL     string `yaml:"token_url,omitempty" json:"token_url,omitempty"`
	ClientID     string `yaml:"client_id,omitempty" json:"client_id,omitempty"`
	ClientSecret string `yaml:"client_secret,omitempty" json:"client_secret,omitempty"`
	Scope        string `yaml:"scope,omitempty" json:"scope,omitempty"`
	ClientAuth   string `yaml:"client_auth,omitempty" json:"client_auth,omitempty"`
}

// inherits reports whether the auth defers to the parent folder.
func (a *Auth) inherits() bool { return a == nil || a.Type == "" || a.Type == AuthInherit }

// Folder is the content of _folder.yaml. Its headers and auth are
// inherited by every API below it.
type Folder struct {
	ID      string       `yaml:"id" json:"id"`
	Name    string       `yaml:"name" json:"name"`
	Order   int64        `yaml:"order" json:"order"`
	Headers KeyValueList `yaml:"headers,omitempty" json:"headers,omitempty"`
	Auth    *Auth        `yaml:"auth,omitempty" json:"auth,omitempty"`
	// ParentID is the containing folder, empty for the top level. It is
	// derived from the location and changed only by Move.
	ParentID string `yaml:"-" json:"parent_id"`
	Version  string `yaml:"-" json:"version"`
}

// APIWebSocket configures protocol-specific WebSocket options on an API.
type APIWebSocket struct {
	Subprotocols []string             `yaml:"subprotocols,omitempty" json:"subprotocols,omitempty"`
	Messages     []scenario.WSMessage `yaml:"messages,omitempty" json:"messages,omitempty"`
	RecvCount    int                  `yaml:"recv_count,omitempty" json:"recv_count,omitempty"`
	IdleTimeout  scenario.Duration    `yaml:"idle_timeout,omitempty" json:"idle_timeout,omitempty"`
}

// APIGRPC configures unary gRPC options on an API.
type APIGRPC struct {
	Service          string            `yaml:"service" json:"service"`
	Method           string            `yaml:"method" json:"method"`
	ProtoFile        string            `yaml:"proto_file,omitempty" json:"proto_file,omitempty"`
	UseReflection    bool              `yaml:"use_reflection,omitempty" json:"use_reflection,omitempty"`
	TLS              *bool             `yaml:"tls,omitempty" json:"tls,omitempty"`
	PayloadEncoding  string            `yaml:"payload_encoding,omitempty" json:"payload_encoding,omitempty"`
	ResponseEncoding string            `yaml:"response_encoding,omitempty" json:"response_encoding,omitempty"`
	ExpectedStatus   *int              `yaml:"expected_status,omitempty" json:"expected_status,omitempty"`
	Timeout          scenario.Duration `yaml:"timeout,omitempty" json:"timeout,omitempty"`
}

// APISSE configures Server-Sent Events stream options on an API.
type APISSE struct {
	MaxEvents    int               `yaml:"max_events,omitempty" json:"max_events,omitempty"`
	StopContains string            `yaml:"stop_contains,omitempty" json:"stop_contains,omitempty"`
	Timeout      scenario.Duration `yaml:"timeout,omitempty" json:"timeout,omitempty"`
}

// APIRequest is the UI-friendly request stored inside .api.yaml.
// Templates ({{variables}}) and disabled/draft fields are preserved as
// written; resolve converts active items into scenario steps.
type APIRequest struct {
	Protocol   string        `yaml:"protocol,omitempty" json:"protocol,omitempty"`
	Method     string        `yaml:"method,omitempty" json:"method,omitempty"`
	URL        string        `yaml:"url" json:"url"`
	PathParams KeyValueList  `yaml:"path_params,omitempty" json:"path_params,omitempty"`
	Query      KeyValueList  `yaml:"query,omitempty" json:"query,omitempty"`
	Headers    KeyValueList  `yaml:"headers,omitempty" json:"headers,omitempty"`
	Body       *APIBody      `yaml:"body,omitempty" json:"body,omitempty"`
	WS         *APIWebSocket `yaml:"ws,omitempty" json:"ws,omitempty"`
	GRPC       *APIGRPC      `yaml:"grpc,omitempty" json:"grpc,omitempty"`
	SSE        *APISSE       `yaml:"sse,omitempty" json:"sse,omitempty"`
}

// APIBody stores the request payload along with drafts for inactive body
// types so switching tabs in the editor does not discard input.
type APIBody struct {
	Type        string              `yaml:"type" json:"type"`
	Content     string              `yaml:"content,omitempty" json:"content,omitempty"`
	Fields      KeyValueList        `yaml:"fields,omitempty" json:"fields,omitempty"`
	Files       []scenario.BodyFile `yaml:"files,omitempty" json:"files,omitempty"`
	File        string              `yaml:"file,omitempty" json:"file,omitempty"`
	ContentType string              `yaml:"content_type,omitempty" json:"content_type,omitempty"`
}

// APIResponseExample documents a concrete example response on an API.
type APIResponseExample struct {
	Name        string       `yaml:"name" json:"name"`
	Status      int          `yaml:"status" json:"status"`
	ContentType string       `yaml:"content_type,omitempty" json:"content_type,omitempty"`
	Headers     KeyValueList `yaml:"headers,omitempty" json:"headers,omitempty"`
	Body        string       `yaml:"body,omitempty" json:"body,omitempty"`
}

// APIFieldDoc documents a response field path for OpenAPI documentation export.
type APIFieldDoc struct {
	Path        string `yaml:"path" json:"path"`
	Type        string `yaml:"type,omitempty" json:"type,omitempty"`
	Required    bool   `yaml:"required,omitempty" json:"required,omitempty"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
}

// API is one request definition, stored as <name>.api.yaml. Templates
// ({{variables}}) are kept as written; they are resolved when a scenario is
// built.
type API struct {
	ID             string               `yaml:"id" json:"id"`
	Name           string               `yaml:"name" json:"name"`
	Order          int64                `yaml:"order" json:"order"`
	Description    string               `yaml:"description,omitempty" json:"description,omitempty"`
	Request        APIRequest           `yaml:"request" json:"request"`
	Auth           *Auth                `yaml:"auth,omitempty" json:"auth,omitempty"`
	Extract        []scenario.Extract   `yaml:"extract,omitempty" json:"extract,omitempty"`
	Assert         []scenario.Assert    `yaml:"assert,omitempty" json:"assert,omitempty"`
	Examples       []APIResponseExample `yaml:"examples,omitempty" json:"examples,omitempty"`
	ResponseFields []APIFieldDoc        `yaml:"response_fields,omitempty" json:"response_fields,omitempty"`
	// FolderID is the containing folder, empty for the top level. It is
	// derived from the location and changed only by Move.
	FolderID string `yaml:"-" json:"folder_id"`
	Version  string `yaml:"-" json:"version"`
}

// Environment is a target environment: base URL and variables.
type Environment struct {
	ID        string       `yaml:"id" json:"id"`
	Name      string       `yaml:"name" json:"name"`
	BaseURL   string       `yaml:"base_url,omitempty" json:"base_url,omitempty"`
	Variables KeyValueList `yaml:"variables,omitempty" json:"variables,omitempty"`
	Version   string       `yaml:"-" json:"version"`
}

// Scenario is a stored scenario: steps that reference APIs, plus load and
// connection options. Resolve turns it into a scenario.Definition.
type Scenario struct {
	ID          string `yaml:"id" json:"id"`
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	// EnvironmentID is the environment used when a run does not pick one.
	EnvironmentID string               `yaml:"environment_id,omitempty" json:"environment_id,omitempty"`
	Variables     KeyValueList         `yaml:"variables,omitempty" json:"variables,omitempty"`
	HTTP          scenario.HTTPOptions `yaml:"http,omitempty" json:"http,omitempty"`
	// Data lists data sources; File is the name of a file uploaded to the
	// project (see SaveFile), never a path.
	Data    []scenario.DataSource `yaml:"data,omitempty" json:"data,omitempty"`
	Steps   []ScenarioStep        `yaml:"steps" json:"steps"`
	Load    scenario.Load         `yaml:"load" json:"load"`
	Version string                `yaml:"-" json:"version"`
}

// ScenarioWeightedBranch is a weighted choice in a "weighted" ScenarioStep.
type ScenarioWeightedBranch struct {
	Name   string         `yaml:"name,omitempty" json:"name,omitempty"`
	Weight float64        `yaml:"weight" json:"weight"`
	Steps  []ScenarioStep `yaml:"steps,omitempty" json:"steps,omitempty"`
}

// ScenarioStep runs one API, script, or control-flow construct ("http", "ws",
// "grpc", "sse", "script", "group", "loop", "if", or "weighted").
type ScenarioStep struct {
	// ID is the step ID in the resolved scenario; one is assigned on save
	// when empty.
	ID string `yaml:"id" json:"id"`
	// Name overrides the API name as the step name.
	Name          string                   `yaml:"name,omitempty" json:"name,omitempty"`
	Type          string                   `yaml:"type,omitempty" json:"type,omitempty"`
	APIID         string                   `yaml:"api_id,omitempty" json:"api_id,omitempty"`
	Script        string                   `yaml:"script,omitempty" json:"script,omitempty"`
	OnError       string                   `yaml:"on_error,omitempty" json:"on_error,omitempty"`
	Disabled      bool                     `yaml:"disabled,omitempty" json:"disabled,omitempty"`
	Override      *StepOverride            `yaml:"override,omitempty" json:"override,omitempty"`
	Think         *scenario.Think          `yaml:"think,omitempty" json:"think,omitempty"`
	LoopCount     int                      `yaml:"loop_count,omitempty" json:"loop_count,omitempty"`
	While         string                   `yaml:"while,omitempty" json:"while,omitempty"`
	MaxIterations int                      `yaml:"max_iterations,omitempty" json:"max_iterations,omitempty"`
	Expr          string                   `yaml:"expr,omitempty" json:"expr,omitempty"`
	Children      []ScenarioStep           `yaml:"children,omitempty" json:"children,omitempty"`
	Else          []ScenarioStep           `yaml:"else,omitempty" json:"else,omitempty"`
	Branches      []ScenarioWeightedBranch `yaml:"branches,omitempty" json:"branches,omitempty"`
}

// StepOverride changes parts of the referenced API for one step. Empty
// fields keep the API's value. Headers and Query are merged over the API's
// values; Body replaces the body; Auth replaces the effective auth; Extract
// and Assert replace the API's lists when set, including when set to an empty list.
type StepOverride struct {
	Method     string              `yaml:"method,omitempty" json:"method,omitempty"`
	URL        string              `yaml:"url,omitempty" json:"url,omitempty"`
	PathParams KeyValueList        `yaml:"path_params,omitempty" json:"path_params,omitempty"`
	Headers    KeyValueList        `yaml:"headers,omitempty" json:"headers,omitempty"`
	Query      KeyValueList        `yaml:"query,omitempty" json:"query,omitempty"`
	Body       *APIBody            `yaml:"body,omitempty" json:"body,omitempty"`
	Auth       *Auth               `yaml:"auth,omitempty" json:"auth,omitempty"`
	Extract    *[]scenario.Extract `yaml:"extract,omitempty" json:"extract,omitempty"`
	Assert     *[]scenario.Assert  `yaml:"assert,omitempty" json:"assert,omitempty"`
}

const maxNameRunes = 200

var (
	identRe  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_\-]*$`)
	tokenRe  = regexp.MustCompile("^[!#$%&'*+\\-.^_`|~0-9A-Za-z]+$")
	stepIDRe = regexp.MustCompile(`^[A-Za-z0-9_\-.]+$`)
)

// cleanName trims and validates a display name.
func cleanName(field, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", invalid(field, "名称不能为空")
	}
	if utf8.RuneCountInString(name) > maxNameRunes {
		return "", invalid(field, "名称最多 %d 个字符", maxNameRunes)
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f || r == utf8.RuneError {
			return "", invalid(field, "名称不能包含控制字符")
		}
	}
	return name, nil
}

func validateHeadersList(field string, hs KeyValueList) error {
	for i, h := range hs {
		if strings.ContainsAny(string(h.Value), "\r\n") {
			return invalid(fmt.Sprintf("%s[%d]", field, i), "请求头值不能包含换行")
		}
		if h.Disabled {
			continue
		}
		name := strings.TrimSpace(h.Name)
		if name == "" && string(h.Value) == "" {
			continue
		}
		if !tokenRe.MatchString(name) {
			return invalid(fmt.Sprintf("%s[%d].name", field, i), "请求头名称 %q 无效", h.Name)
		}
	}
	return nil
}

func validateVariablesList(field string, vars KeyValueList) error {
	for i, v := range vars {
		if v.Disabled {
			continue
		}
		name := strings.TrimSpace(v.Name)
		if name == "" && string(v.Value) == "" {
			continue
		}
		if !identRe.MatchString(name) {
			return invalid(fmt.Sprintf("%s[%d].name", field, i), "变量名 %q 无效，只能包含字母、数字、下划线和连字符，且不能以数字开头", v.Name)
		}
	}
	return nil
}

func validateVariablesMap(field string, vars map[string]scenario.Scalar) error {
	for _, k := range sortedKeys(vars) {
		if !identRe.MatchString(k) {
			return invalid(field+"."+k, "变量名无效，只能包含字母、数字、下划线和连字符，且不能以数字开头")
		}
	}
	return nil
}

// validateAuthDraft validates auth syntax on save without requiring
// credentials to be completely filled in yet. Full completeness is checked
// when resolving for execution.
func validateAuthDraft(field string, a *Auth) error {
	if a == nil {
		return nil
	}
	switch a.Type {
	case "", AuthInherit, AuthNone, AuthBearer, AuthBasic:
	case AuthAPIKey:
		if a.Key != "" && !tokenRe.MatchString(a.Key) {
			return invalid(field+".key", "参数名 %q 无效", a.Key)
		}
		if a.In != "" && a.In != "header" && a.In != "query" {
			return invalid(field+".in", "取值 %q 无效，只能是 header 或 query", a.In)
		}
	case AuthOAuth2:
		ca := strings.ToLower(strings.TrimSpace(a.ClientAuth))
		if ca != "" && ca != "basic" && ca != "body" {
			return invalid(field+".client_auth", "取值 %q 无效，只能是 basic 或 body", a.ClientAuth)
		}
	default:
		return invalid(field+".type", "取值 %q 无效，只能是 inherit、none、bearer、basic、api_key 或 oauth2", a.Type)
	}
	for name, v := range map[string]string{
		"token":         a.Token,
		"username":      a.Username,
		"password":      a.Password,
		"value":         a.Value,
		"token_url":     a.TokenURL,
		"client_id":     a.ClientID,
		"client_secret": a.ClientSecret,
		"scope":         a.Scope,
	} {
		if strings.ContainsAny(v, "\r\n") {
			return invalid(field+"."+name, "值不能包含换行")
		}
	}
	return nil
}

func validateRequest(field string, r *APIRequest) error {
	r.Protocol = strings.ToLower(strings.TrimSpace(r.Protocol))
	if r.Protocol == "websocket" {
		r.Protocol = "ws"
	}
	switch r.Protocol {
	case "", "http", "ws", "grpc", "sse":
	default:
		return invalid(field+".protocol", "协议类型 %q 无效，支持 http、ws、grpc 或 sse", r.Protocol)
	}
	if m := strings.TrimSpace(r.Method); m != "" && !tokenRe.MatchString(m) {
		return invalid(field+".method", "请求方法 %q 无效", r.Method)
	}
	if strings.ContainsAny(r.URL, "\r\n") {
		return invalid(field+".url", "不能包含换行")
	}
	if r.WS != nil {
		if r.WS.RecvCount < 0 {
			return invalid(field+".ws.recv_count", "不能为负数")
		}
		if r.WS.IdleTimeout < 0 {
			return invalid(field+".ws.idle_timeout", "不能为负数")
		}
	}
	if r.GRPC != nil {
		if r.GRPC.Timeout < 0 {
			return invalid(field+".grpc.timeout", "不能为负数")
		}
	}
	if r.SSE != nil {
		if r.SSE.MaxEvents < 0 {
			return invalid(field+".sse.max_events", "不能为负数")
		}
		if r.SSE.Timeout < 0 {
			return invalid(field+".sse.timeout", "不能为负数")
		}
	}
	return validateHeadersList(field+".headers", r.Headers)
}

func (f *Folder) normalize() error {
	name, err := cleanName("name", f.Name)
	if err != nil {
		return err
	}
	f.Name = name
	if err := validateHeadersList("headers", f.Headers); err != nil {
		return err
	}
	return validateAuthDraft("auth", f.Auth)
}

func (a *API) normalize() error {
	name, err := cleanName("name", a.Name)
	if err != nil {
		return err
	}
	a.Name = name
	a.Request.Method = strings.ToUpper(strings.TrimSpace(a.Request.Method))
	if err := validateRequest("request", &a.Request); err != nil {
		return err
	}
	return validateAuthDraft("auth", a.Auth)
}

func (e *Environment) normalize() error {
	name, err := cleanName("name", e.Name)
	if err != nil {
		return err
	}
	e.Name = name
	e.BaseURL = strings.TrimSpace(e.BaseURL)
	if e.BaseURL != "" && !strings.Contains(e.BaseURL, "{{") {
		u, err := url.Parse(e.BaseURL)
		scheme := ""
		if u != nil {
			scheme = strings.ToLower(u.Scheme)
		}
		allowedScheme := scheme == "http" || scheme == "https" || scheme == "ws" || scheme == "wss" || scheme == "grpc" || scheme == "grpcs"
		if err != nil || !allowedScheme || u.Host == "" {
			return invalid("base_url", "基础地址 %q 无效，需要以 http://、https://、ws://、wss://、grpc:// 或 grpcs:// 开头", e.BaseURL)
		}
	}
	return validateVariablesList("variables", e.Variables)
}

// normalize validates a scenario and assigns missing step IDs. Whether the
// referenced APIs exist is checked by the caller.
func (s *Scenario) normalize() error {
	name, err := cleanName("name", s.Name)
	if err != nil {
		return err
	}
	s.Name = name
	if s.EnvironmentID != "" {
		id, ok := normalizeID(s.EnvironmentID)
		if !ok {
			return invalid("environment_id", "环境 ID %q 无效", s.EnvironmentID)
		}
		s.EnvironmentID = id
	}
	if err := validateVariablesList("variables", s.Variables); err != nil {
		return err
	}
	seenData := map[string]bool{}
	for i, d := range s.Data {
		field := fmt.Sprintf("data[%d]", i)
		if !identRe.MatchString(d.Name) {
			return invalid(field+".name", "名称 %q 无效，只能包含字母、数字、下划线和连字符，且不能以数字开头", d.Name)
		}
		if seenData[d.Name] {
			return invalid(field+".name", "名称 %q 重复", d.Name)
		}
		seenData[d.Name] = true
		if err := ValidateFileName(d.File); err != nil {
			return withField(field+".file", err)
		}
	}
	used := map[string]bool{}
	collectUsedStepIDs(s.Steps, used)
	seen := map[string]bool{}
	next := 1
	return normalizeScenarioSteps("steps", s.Steps, used, seen, &next)
}

func cloneScenarioSteps(in []ScenarioStep) []ScenarioStep {
	if len(in) == 0 {
		return nil
	}
	out := make([]ScenarioStep, len(in))
	for i, st := range in {
		cp := st
		cp.Children = cloneScenarioSteps(st.Children)
		cp.Else = cloneScenarioSteps(st.Else)
		if len(st.Branches) > 0 {
			cp.Branches = make([]ScenarioWeightedBranch, len(st.Branches))
			for j, br := range st.Branches {
				cp.Branches[j] = ScenarioWeightedBranch{
					Name:   br.Name,
					Weight: br.Weight,
					Steps:  cloneScenarioSteps(br.Steps),
				}
			}
		}
		out[i] = cp
	}
	return out
}

func collectUsedStepIDs(steps []ScenarioStep, used map[string]bool) {
	for _, st := range steps {
		if st.ID != "" {
			used[st.ID] = true
		}
		collectUsedStepIDs(st.Children, used)
		collectUsedStepIDs(st.Else, used)
		for _, br := range st.Branches {
			collectUsedStepIDs(br.Steps, used)
		}
	}
}

func normalizeScenarioSteps(prefix string, steps []ScenarioStep, used, seen map[string]bool, next *int) error {
	for i := range steps {
		st := &steps[i]
		field := fmt.Sprintf("%s[%d]", prefix, i)
		st.Name = strings.TrimSpace(st.Name)
		st.Type = strings.ToLower(strings.TrimSpace(st.Type))
		if st.ID == "" {
			for used[fmt.Sprintf("step%d", *next)] {
				*next++
			}
			st.ID = fmt.Sprintf("step%d", *next)
			used[st.ID] = true
		}
		if !stepIDRe.MatchString(st.ID) {
			return invalid(field+".id", "步骤 ID %q 无效，只能包含字母、数字、下划线、连字符和点", st.ID)
		}
		if seen[st.ID] {
			return invalid(field+".id", "步骤 ID %q 重复", st.ID)
		}
		seen[st.ID] = true

		switch st.Type {
		case "", "http", "ws", "grpc", "sse":
			id, ok := normalizeID(st.APIID)
			if !ok {
				return invalid(field+".api_id", "接口 ID %q 无效", st.APIID)
			}
			st.APIID = id
			if o := st.Override; o != nil {
				if m := strings.TrimSpace(o.Method); m != "" && !tokenRe.MatchString(m) {
					return invalid(field+".override.method", "请求方法 %q 无效", o.Method)
				}
				if err := validateHeadersList(field+".override.headers", o.Headers); err != nil {
					return err
				}
				if err := validateAuthDraft(field+".override.auth", o.Auth); err != nil {
					return err
				}
			}
		case "script":
			if strings.TrimSpace(st.Script) == "" {
				return invalid(field+".script", "脚本文本不能为空")
			}
		case "group":
			if err := normalizeScenarioSteps(field+".children", st.Children, used, seen, next); err != nil {
				return err
			}
		case "loop":
			if st.LoopCount < 0 {
				return invalid(field+".loop_count", "循环次数不能为负数")
			}
			if st.MaxIterations < 0 {
				return invalid(field+".max_iterations", "最大循环次数不能为负数")
			}
			if err := normalizeScenarioSteps(field+".children", st.Children, used, seen, next); err != nil {
				return err
			}
		case "if":
			if strings.TrimSpace(st.Expr) == "" {
				return invalid(field+".expr", "条件分支表达式不能为空")
			}
			if err := normalizeScenarioSteps(field+".children", st.Children, used, seen, next); err != nil {
				return err
			}
			if err := normalizeScenarioSteps(field+".else", st.Else, used, seen, next); err != nil {
				return err
			}
		case "weighted":
			if len(st.Branches) == 0 {
				return invalid(field+".branches", "权重分支至少需要一个分支")
			}
			for bIdx := range st.Branches {
				bf := fmt.Sprintf("%s.branches[%d]", field, bIdx)
				if st.Branches[bIdx].Weight <= 0 {
					return invalid(bf+".weight", "分支权重必须大于 0")
				}
				if err := normalizeScenarioSteps(bf+".steps", st.Branches[bIdx].Steps, used, seen, next); err != nil {
					return err
				}
			}
		default:
			return invalid(field+".type", "步骤类型 %q 无效，只能是 http、ws、grpc、sse、script、group、loop、if 或 weighted", st.Type)
		}
	}
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
