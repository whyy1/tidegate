package store

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"tidegate/internal/scenario"
)

// ResolveOptions select the environment and extra variables of a resolved
// scenario.
type ResolveOptions struct {
	// EnvironmentID selects the environment. Empty uses the scenario's
	// default environment, if any.
	EnvironmentID string
	// Variables override project, environment and scenario variables.
	Variables map[string]scenario.Scalar
	// IncludeSecrets permits decrypting Secret:true variables inside
	// PreviewEffectiveAPI (used when generating unredacted cURL).
	IncludeSecrets bool
	// SkipSecretDecrypt prevents decrypting encrypted secrets from SQLite
	// during ResolveScenario/ResolveAPI (used by code exporters such as k6).
	SkipSecretDecrypt bool
}

// APIRunOptions configure a scenario built from a single API (one-click
// load test or debug request).
type APIRunOptions struct {
	ResolveOptions
	// Load is the load model. Without stages the scenario runs a single
	// iteration with one virtual user.
	Load scenario.Load
	HTTP scenario.HTTPOptions
}

// ResolvedFile is a data file referenced by a resolved scenario.
type ResolvedFile struct {
	// Name is the reference used in the definition, relative to BaseDir.
	Name string
	// Path is the absolute path on this machine.
	Path string
}

// Resolved is a scenario in the exchange format, ready for scenario.Compile.
type Resolved struct {
	Definition *scenario.Definition
	// BaseDir is the project's data file directory; file references in the
	// definition are names relative to it. Pass it as
	// scenario.CompileOptions.BaseDir.
	BaseDir string
	// Files lists the referenced data files in order of first use.
	Files []ResolvedFile
	// SecretNames lists variable names marked Secret:true from project or environment scopes.
	SecretNames map[string]bool
}

// CompileOptions returns the options for scenario.Compile.
func (r *Resolved) CompileOptions() scenario.CompileOptions {
	return scenario.CompileOptions{BaseDir: r.BaseDir}
}

// Compile compiles the definition. Compilation mutates the definition
// (it fills in defaults), so compile a resolved scenario only once.
func (r *Resolved) Compile() (*scenario.Plan, error) {
	return scenario.Compile(r.Definition, r.CompileOptions())
}

// InheritedHeader describes a header inherited from an ancestor folder.
type InheritedHeader struct {
	Name             string `json:"name"`
	Value            string `json:"value"`
	Description      string `json:"description,omitempty"`
	SourceFolderID   string `json:"source_folder_id"`
	SourceFolderName string `json:"source_folder_name"`
	Overridden       bool   `json:"overridden"`
}

// EffectiveAuth describes the resolved auth setting and where it comes from.
type EffectiveAuth struct {
	Auth             *Auth  `json:"auth,omitempty"`
	Source           string `json:"source"` // "api", "folder", "none"
	SourceFolderID   string `json:"source_folder_id,omitempty"`
	SourceFolderName string `json:"source_folder_name,omitempty"`
}

// ResolvedVariable describes an active variable and which scope defined it.
type ResolvedVariable struct {
	Name        string `json:"name"`
	Value       string `json:"value"`
	Source      string `json:"source"` // "project", "environment", "override"
	Description string `json:"description,omitempty"`
	Secret      bool   `json:"secret,omitempty"`
}

// EffectivePreview summarizes inherited headers, effective auth, variables
// and joined URL for an API in the editor without failing on incomplete fields.
type EffectivePreview struct {
	Method           string             `json:"method"`
	URL              string             `json:"url"`
	BaseURL          string             `json:"base_url,omitempty"`
	InheritedHeaders []InheritedHeader  `json:"inherited_headers"`
	EffectiveAuth    EffectiveAuth      `json:"effective_auth"`
	Variables        []ResolvedVariable `json:"variables"`
}

// resolver builds scenario steps from stored APIs.
type resolver struct {
	p           *project
	baseURL     string
	vars        map[string]scenario.Scalar
	mutable     map[string]bool // variables written by extractors
	secretNames map[string]bool
	filesDir    string
	files       map[string]bool
	out         []ResolvedFile
}

// newResolver merges variables (project < scenario < environment < overrides)
// and loads the environment.
func (p *project) newResolver(envID string, scenarioVars KeyValueList, opts ResolveOptions) (*resolver, error) {
	r := &resolver{p: p, vars: map[string]scenario.Scalar{}, mutable: map[string]bool{}, filesDir: p.filesDir(), files: map[string]bool{}}
	vault := p.secretVault()
	if opts.SkipSecretDecrypt {
		vault = nil
	}
	if pr, err := p.readProject(); err == nil {
		var projSecs map[string]string
		if vault != nil {
			projSecs, err = vault.ResolveVariables(context.Background(), p.id, "project")
			if err != nil {
				return nil, err
			}
		}
		for _, v := range pr.Variables {
			if v.Disabled {
				continue
			}
			name := strings.TrimSpace(v.Name)
			if name == "" {
				continue
			}
			val := v.Value
			if v.Secret {
				if r.secretNames == nil {
					r.secretNames = map[string]bool{}
				}
				r.secretNames[name] = true
			}
			if v.Secret && projSecs != nil {
				if plain, ok := projSecs[name]; ok {
					val = scenario.Scalar(plain)
				} else if string(val) == SecretMask {
					val = ""
				}
			}
			r.vars[name] = val
		}
	}
	for _, v := range scenarioVars {
		if !v.Disabled && strings.TrimSpace(v.Name) != "" {
			r.vars[strings.TrimSpace(v.Name)] = v.Value
		}
	}
	if envID != "" {
		env, err := p.readEnvironment(envID)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				return nil, invalid("environment_id", "环境 %s 不存在", envID)
			}
			return nil, err
		}
		r.baseURL = env.BaseURL
		var envSecs map[string]string
		if vault != nil {
			envSecs, err = vault.ResolveVariables(context.Background(), p.id, envID)
			if err != nil {
				return nil, err
			}
		}
		for _, v := range env.Variables {
			if v.Disabled {
				continue
			}
			name := strings.TrimSpace(v.Name)
			if name == "" {
				continue
			}
			val := v.Value
			if v.Secret {
				if r.secretNames == nil {
					r.secretNames = map[string]bool{}
				}
				r.secretNames[name] = true
			}
			if v.Secret && envSecs != nil {
				if plain, ok := envSecs[name]; ok {
					val = scenario.Scalar(plain)
				} else if string(val) == SecretMask {
					val = ""
				}
			}
			r.vars[name] = val
		}
	}
	if err := validateVariablesMap("variables", opts.Variables); err != nil {
		return nil, err
	}
	for k, v := range opts.Variables {
		r.vars[k] = v
	}
	return r, nil
}

func (r *resolver) definitionVars() map[string]scenario.Scalar {
	if len(r.vars) == 0 {
		return nil
	}
	return r.vars
}

// file checks a data file reference and records it.
func (r *resolver) file(field, name string) error {
	if err := ValidateFileName(name); err != nil {
		return withField(field, err)
	}
	path, df, err := r.p.statFile(name)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return invalid(field, "数据文件 %q 不存在，请先上传", name)
		}
		return err
	}
	if !r.files[df.Name] {
		r.files[df.Name] = true
		r.out = append(r.out, ResolvedFile{Name: df.Name, Path: path})
	}
	return nil
}

func joinField(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "." + name
}

// stepSource is an API with its location and the scenario step using it.
type stepSource struct {
	field     string
	api       *API
	folderRel string
	step      *ScenarioStep // nil for single API runs
}

func (src *stepSource) extractors() []scenario.Extract {
	if src.step != nil && src.step.Override != nil && src.step.Override.Extract != nil {
		return *src.step.Override.Extract
	}
	return src.api.Extract
}

// mergeNameValues appends enabled items from src onto dst, replacing any
// existing entry from an earlier layer whose name matches (case-insensitively
// when caseFold is true). Multiple items with the same name inside src itself
// are preserved in order.
func mergeNameValues(dst []scenario.NameValue, src KeyValueList, caseFold bool) []scenario.NameValue {
	if len(src) == 0 {
		return dst
	}
	overridden := make(map[string]bool, len(src))
	var active []scenario.NameValue
	for _, item := range src {
		if item.Disabled {
			continue
		}
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		key := name
		if caseFold {
			key = strings.ToLower(name)
		}
		overridden[key] = true
		active = append(active, scenario.NameValue{Name: name, Value: item.Value})
	}
	if len(active) == 0 {
		return dst
	}
	kept := dst[:0]
	for _, existing := range dst {
		key := existing.Name
		if caseFold {
			key = strings.ToLower(existing.Name)
		}
		if !overridden[key] {
			kept = append(kept, existing)
		}
	}
	return append(kept, active...)
}

func removeNameValue(dst []scenario.NameValue, target string) []scenario.NameValue {
	if len(dst) == 0 {
		return dst
	}
	kept := dst[:0]
	for _, item := range dst {
		if !strings.EqualFold(item.Name, target) {
			kept = append(kept, item)
		}
	}
	return kept
}

func mergeKeyValues(dst, src KeyValueList) KeyValueList {
	if len(src) == 0 {
		return dst
	}
	overridden := make(map[string]bool, len(src))
	var active KeyValueList
	for _, item := range src {
		if item.Disabled {
			continue
		}
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		overridden[name] = true
		cp := item
		cp.Name = name
		active = append(active, cp)
	}
	if len(active) == 0 {
		return dst
	}
	out := make(KeyValueList, 0, len(dst)+len(active))
	for _, existing := range dst {
		if existing.Disabled || strings.TrimSpace(existing.Name) == "" {
			continue
		}
		if !overridden[strings.TrimSpace(existing.Name)] {
			out = append(out, existing)
		}
	}
	return append(out, active...)
}

func cloneAPIBody(b *APIBody) *APIBody {
	if b == nil {
		return nil
	}
	c := *b
	c.Fields = append(KeyValueList(nil), b.Fields...)
	c.Files = append([]scenario.BodyFile(nil), b.Files...)
	return &c
}

func (r *resolver) buildScenarioBody(field string, b *APIBody) (*scenario.Body, error) {
	if b == nil {
		return nil, nil
	}
	switch b.Type {
	case "", "none":
		// Draft fields from other tabs are ignored when none is active.
		return nil, nil
	case "json", "raw":
		return &scenario.Body{Type: b.Type, Content: b.Content}, nil
	case "binary":
		if err := r.file(joinField(field, "file"), b.File); err != nil {
			return nil, err
		}
		return &scenario.Body{Type: "binary", File: b.File, ContentType: b.ContentType}, nil
	case "form":
		sb := &scenario.Body{Type: "form"}
		fields := activeNameValues(b.Fields)
		if len(fields) > 0 {
			sb.FieldList = fields
		} else {
			sb.Content = b.Content
		}
		return sb, nil
	case "multipart":
		sb := &scenario.Body{
			Type:      "multipart",
			FieldList: activeNameValues(b.Fields),
		}
		for j, f := range b.Files {
			if f.Disabled {
				continue
			}
			if err := r.file(joinField(field, fmt.Sprintf("files[%d].path", j)), f.Path); err != nil {
				return nil, err
			}
			sb.Files = append(sb.Files, f)
		}
		return sb, nil
	default:
		return &scenario.Body{Type: b.Type, Content: b.Content}, nil
	}
}

func activeNameValues(list KeyValueList) []scenario.NameValue {
	var out []scenario.NameValue
	for _, item := range list {
		if item.Disabled {
			continue
		}
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		out = append(out, scenario.NameValue{Name: name, Value: item.Value})
	}
	return out
}

// applyPathParams substitutes :name and single-brace {name} path segments
// in rawURL before the query string while leaving {{template}} expressions
// untouched.
func applyPathParams(rawURL string, params KeyValueList) string {
	vals := map[string]string{}
	for _, p := range params {
		if p.Disabled {
			continue
		}
		if name := strings.TrimSpace(p.Name); name != "" {
			vals[name] = string(p.Value)
		}
	}
	if len(vals) == 0 || rawURL == "" {
		return rawURL
	}
	pathPart, queryPart, hasQ := strings.Cut(rawURL, "?")

	// Determine where the path starts so scheme/port colons (e.g. :8080) are
	// never mistaken for :param segments.
	pathStart := 0
	if schemeIdx := strings.Index(pathPart, "://"); schemeIdx >= 0 {
		rest := pathPart[schemeIdx+3:]
		if slashIdx := strings.IndexByte(rest, '/'); slashIdx >= 0 {
			pathStart = schemeIdx + 3 + slashIdx
		} else {
			pathStart = len(pathPart)
		}
	}

	escapeVal := func(v string) string {
		if strings.Contains(v, "{{") {
			return v
		}
		return url.PathEscape(v)
	}

	var b strings.Builder
	for i := 0; i < len(pathPart); {
		// Preserve {{...}} template expressions verbatim.
		if i+1 < len(pathPart) && pathPart[i] == '{' && pathPart[i+1] == '{' {
			end := strings.Index(pathPart[i+2:], "}}")
			if end >= 0 {
				b.WriteString(pathPart[i : i+2+end+2])
				i += 2 + end + 2
				continue
			}
		}
		// Single-brace {name}.
		if pathPart[i] == '{' {
			end := strings.IndexByte(pathPart[i+1:], '}')
			if end > 0 && i+1+end+1 <= len(pathPart) {
				name := pathPart[i+1 : i+1+end]
				if !strings.ContainsAny(name, "{}/") {
					if v, ok := vals[name]; ok {
						b.WriteString(escapeVal(v))
						i += 1 + end + 1
						continue
					}
				}
			}
		}
		// Colon parameter :name inside the path component after '/'.
		if i >= pathStart && pathPart[i] == ':' && i > 0 && pathPart[i-1] == '/' {
			j := i + 1
			for j < len(pathPart) && isIdentByte(pathPart[j]) {
				j++
			}
			if j > i+1 {
				name := pathPart[i+1 : j]
				if v, ok := vals[name]; ok {
					b.WriteString(escapeVal(v))
					i = j
					continue
				}
			}
		}
		b.WriteByte(pathPart[i])
		i++
	}
	if hasQ {
		b.WriteByte('?')
		b.WriteString(queryPart)
	}
	return b.String()
}

func isIdentByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-'
}

// step builds one scenario step: folder headers (top level first), then
// the effective auth, then the API's own headers, then step overrides.
func (r *resolver) step(id string, src *stepSource) (scenario.Step, error) {
	a := src.api
	chain, err := r.p.folderChain(src.folderRel)
	if err != nil {
		return scenario.Step{}, err
	}
	var headers []scenario.NameValue
	var auth *Auth
	for _, f := range chain {
		headers = mergeNameValues(headers, f.Headers, true)
		if !f.Auth.inherits() {
			auth = f.Auth
		}
	}
	if !a.Auth.inherits() {
		auth = a.Auth
	}
	if src.step != nil && src.step.Override != nil && src.step.Override.Auth != nil && !src.step.Override.Auth.inherits() {
		auth = src.step.Override.Auth
	}

	var query []scenario.NameValue
	query = mergeNameValues(query, a.Request.Query, false)

	var basicAuth *scenario.BasicAuth
	var oauth2Auth *scenario.OAuth2Auth
	if auth != nil && auth.Type != AuthNone {
		ba, oa, err := r.applyAuth(joinField(src.field, "auth"), a.Name, auth, &headers, &query)
		if err != nil {
			return scenario.Step{}, err
		}
		basicAuth = ba
		oauth2Auth = oa
	}
	headers = mergeNameValues(headers, a.Request.Headers, true)

	method := a.Request.Method
	rawURL := a.Request.URL
	pathParams := a.Request.PathParams
	apiBody := cloneAPIBody(a.Request.Body)
	extract, assert := a.Extract, a.Assert
	name := a.Name
	var think *scenario.Think

	if st := src.step; st != nil {
		if st.Name != "" {
			name = st.Name
		}
		think = st.Think
		if o := st.Override; o != nil {
			if o.Method != "" {
				method = strings.ToUpper(strings.TrimSpace(o.Method))
			}
			if o.URL != "" {
				rawURL = o.URL
			}
			if len(o.PathParams) > 0 {
				pathParams = mergeKeyValues(pathParams, o.PathParams)
			}
			headers = mergeNameValues(headers, o.Headers, true)
			query = mergeNameValues(query, o.Query, false)
			if o.Body != nil {
				apiBody = cloneAPIBody(o.Body)
			}
			if o.Extract != nil {
				extract = *o.Extract
			}
			if o.Assert != nil {
				assert = *o.Assert
			}
		}
	}

	rawURL = applyPathParams(rawURL, pathParams)
	proto := strings.ToLower(strings.TrimSpace(a.Request.Protocol))
	if proto == "websocket" {
		proto = "ws"
	}
	step := scenario.Step{
		ID:      id,
		Name:    name,
		Extract: append([]scenario.Extract(nil), extract...),
		Assert:  append([]scenario.Assert(nil), assert...),
		Think:   think,
	}

	switch proto {
	case "ws":
		fullURL, err := r.joinBaseForProto(joinField(src.field, "request.url"), a.Name, "ws", rawURL)
		if err != nil {
			return scenario.Step{}, err
		}
		fullURL = appendQueryParams(fullURL, query)
		ws := &scenario.WebSocketStep{
			URL:        fullURL,
			HeaderList: headers,
		}
		if a.Request.WS != nil {
			ws.Subprotocols = append([]string(nil), a.Request.WS.Subprotocols...)
			ws.Messages = append([]scenario.WSMessage(nil), a.Request.WS.Messages...)
			ws.RecvCount = a.Request.WS.RecvCount
			ws.IdleTimeout = a.Request.WS.IdleTimeout
		}
		step.WebSocket = ws
		return step, nil

	case "grpc":
		if a.Request.GRPC == nil {
			return scenario.Step{}, invalid(joinField(src.field, "request.grpc"), "接口 %q 缺少 gRPC 配置", a.Name)
		}
		target, err := r.joinBaseForProto(joinField(src.field, "request.url"), a.Name, "grpc", rawURL)
		if err != nil {
			return scenario.Step{}, err
		}
		payload := ""
		if apiBody != nil {
			payload = apiBody.Content
		}
		step.GRPC = &scenario.GRPCStep{
			Target:           target,
			TLS:              a.Request.GRPC.TLS,
			Service:          a.Request.GRPC.Service,
			Method:           a.Request.GRPC.Method,
			MetadataList:     headers,
			Payload:          payload,
			PayloadEncoding:  a.Request.GRPC.PayloadEncoding,
			ResponseEncoding: a.Request.GRPC.ResponseEncoding,
			ExpectedStatus:   a.Request.GRPC.ExpectedStatus,
			Timeout:          a.Request.GRPC.Timeout,
		}
		return step, nil

	case "sse":
		fullURL, err := r.joinBaseForProto(joinField(src.field, "request.url"), a.Name, "sse", rawURL)
		if err != nil {
			return scenario.Step{}, err
		}
		body, err := r.buildScenarioBody(joinField(src.field, "request.body"), apiBody)
		if err != nil {
			return scenario.Step{}, err
		}
		sse := &scenario.SSEStep{
			Method:     method,
			URL:        fullURL,
			HeaderList: headers,
			QueryList:  query,
			BasicAuth:  basicAuth,
			OAuth2:     oauth2Auth,
			Body:       body,
		}
		if a.Request.SSE != nil {
			sse.MaxEvents = a.Request.SSE.MaxEvents
			sse.StopContains = a.Request.SSE.StopContains
			sse.Timeout = a.Request.SSE.Timeout
		}
		step.SSE = sse
		return step, nil

	default:
		fullURL, err := r.joinBaseForProto(joinField(src.field, "request.url"), a.Name, "http", rawURL)
		if err != nil {
			return scenario.Step{}, err
		}
		body, err := r.buildScenarioBody(joinField(src.field, "request.body"), apiBody)
		if err != nil {
			return scenario.Step{}, err
		}
		step.Request = &scenario.Request{
			Method:     method,
			URL:        fullURL,
			HeaderList: headers,
			QueryList:  query,
			BasicAuth:  basicAuth,
			OAuth2:     oauth2Auth,
			Body:       body,
		}
		return step, nil
	}
}

func (r *resolver) applyAuth(field, apiName string, auth *Auth, headers *[]scenario.NameValue, query *[]scenario.NameValue) (*scenario.BasicAuth, *scenario.OAuth2Auth, error) {
	switch auth.Type {
	case AuthBearer:
		if strings.TrimSpace(auth.Token) == "" {
			return nil, nil, invalid(field+".token", "接口 %q 的 bearer 鉴权缺少 token", apiName)
		}
		*headers = mergeNameValues(*headers, KeyValueList{{Name: "Authorization", Value: scenario.Scalar("Bearer " + auth.Token)}}, true)
	case AuthBasic:
		if auth.Username == "" {
			return nil, nil, invalid(field+".username", "接口 %q 的 basic 鉴权缺少用户名", apiName)
		}
		user, ok1 := r.expandConst(auth.Username, 0)
		pass, ok2 := r.expandConst(auth.Password, 0)
		if ok1 && ok2 {
			cred := base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
			*headers = mergeNameValues(*headers, KeyValueList{{Name: "Authorization", Value: scenario.Scalar("Basic " + cred)}}, true)
			return nil, nil, nil
		}
		*headers = removeNameValue(*headers, "Authorization")
		return &scenario.BasicAuth{Username: auth.Username, Password: auth.Password}, nil, nil
	case AuthAPIKey:
		if !tokenRe.MatchString(auth.Key) {
			return nil, nil, invalid(field+".key", "接口 %q 的 api_key 参数名 %q 无效", apiName, auth.Key)
		}
		if auth.In != "" && auth.In != "header" && auth.In != "query" {
			return nil, nil, invalid(field+".in", "取值 %q 无效，只能是 header 或 query", auth.In)
		}
		if auth.In == "query" {
			*query = mergeNameValues(*query, KeyValueList{{Name: auth.Key, Value: scenario.Scalar(auth.Value)}}, false)
		} else {
			*headers = mergeNameValues(*headers, KeyValueList{{Name: auth.Key, Value: scenario.Scalar(auth.Value)}}, true)
		}
	case AuthOAuth2:
		if strings.TrimSpace(auth.TokenURL) == "" {
			return nil, nil, invalid(field+".token_url", "接口 %q 的 OAuth 2.0 鉴权缺少 token_url", apiName)
		}
		if strings.TrimSpace(auth.ClientID) == "" {
			return nil, nil, invalid(field+".client_id", "接口 %q 的 OAuth 2.0 鉴权缺少 client_id", apiName)
		}
		tokURL, err := r.joinBase(field+".token_url", apiName, auth.TokenURL)
		if err != nil {
			return nil, nil, err
		}
		*headers = removeNameValue(*headers, "Authorization")
		return nil, &scenario.OAuth2Auth{
			TokenURL:     tokURL,
			ClientID:     auth.ClientID,
			ClientSecret: auth.ClientSecret,
			Scope:        auth.Scope,
			ClientAuth:   auth.ClientAuth,
		}, nil
	}
	return nil, nil, nil
}

// expandConst substitutes {{name}} references to constant variables. It
// fails for references to variables written by extractors, data columns,
// built-in functions and undefined names, which are only known at run time.
func (r *resolver) expandConst(s string, depth int) (string, bool) {
	if depth > 16 {
		return "", false
	}
	var b strings.Builder
	for {
		i := strings.Index(s, "{{")
		if i < 0 {
			b.WriteString(s)
			return b.String(), true
		}
		j := strings.Index(s[i+2:], "}}")
		if j < 0 {
			return "", false
		}
		name := strings.TrimSpace(s[i+2 : i+2+j])
		if !identRe.MatchString(name) || r.mutable[name] {
			return "", false
		}
		v, ok := r.vars[name]
		if !ok {
			return "", false
		}
		ev, ok := r.expandConst(string(v), depth+1)
		if !ok {
			return "", false
		}
		b.WriteString(s[:i])
		b.WriteString(ev)
		s = s[i+2+j+2:]
	}
}

// joinBase prefixes relative URLs with the environment's base URL. URLs
// with a scheme and URLs starting with a template are kept as written.
func (r *resolver) joinBase(field, apiName, u string) (string, error) {
	return r.joinBaseForProto(field, apiName, "http", u)
}

func (r *resolver) joinBaseForProto(field, apiName, proto, u string) (string, error) {
	t := strings.TrimSpace(u)
	lower := strings.ToLower(t)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") ||
		strings.HasPrefix(lower, "ws://") || strings.HasPrefix(lower, "wss://") ||
		strings.HasPrefix(lower, "grpc://") || strings.HasPrefix(lower, "grpcs://") ||
		strings.HasPrefix(t, "{{") {
		return t, nil
	}
	if proto == "grpc" {
		if t == "" {
			if r.baseURL == "" {
				return "", invalid(field, "接口 %q 的 gRPC 地址不能为空", apiName)
			}
			bu := strings.TrimSpace(r.baseURL)
			switch {
			case strings.HasPrefix(strings.ToLower(bu), "http://"):
				return "grpc://" + bu[7:], nil
			case strings.HasPrefix(strings.ToLower(bu), "https://"):
				return "grpcs://" + bu[8:], nil
			default:
				return bu, nil
			}
		}
		return t, nil
	}
	if t == "" {
		return "", nil
	}
	if r.baseURL == "" {
		return "", invalid(field, "接口 %q 的地址 %q 是相对路径，请选择设置了基础地址的环境，或改成完整协议地址", apiName, t)
	}
	joined := strings.TrimRight(r.baseURL, "/") + "/" + strings.TrimLeft(t, "/")
	if proto == "ws" {
		if strings.HasPrefix(strings.ToLower(joined), "http://") {
			joined = "ws://" + joined[7:]
		} else if strings.HasPrefix(strings.ToLower(joined), "https://") {
			joined = "wss://" + joined[8:]
		}
	}
	return joined, nil
}

func appendQueryParams(rawURL string, query []scenario.NameValue) string {
	if len(query) == 0 {
		return rawURL
	}
	sep := "?"
	if strings.Contains(rawURL, "?") {
		sep = "&"
	}
	var b strings.Builder
	b.WriteString(rawURL)
	for _, q := range query {
		b.WriteString(sep)
		b.WriteString(url.QueryEscape(q.Name))
		b.WriteByte('=')
		val := string(q.Value)
		if strings.Contains(val, "{{") {
			b.WriteString(val)
		} else {
			b.WriteString(url.QueryEscape(val))
		}
		sep = "&"
	}
	return b.String()
}

func (r *resolver) result(def *scenario.Definition) *Resolved {
	files := r.out
	if files == nil {
		files = []ResolvedFile{}
	}
	return &Resolved{Definition: def, BaseDir: r.filesDir, Files: files, SecretNames: r.secretNames}
}

// singleIteration is the load used when a single API run has no stages:
// one virtual user sending each step once.
func singleIteration(steps int) scenario.Load {
	return scenario.Load{
		Mode:        "vus",
		Stages:      []scenario.Stage{{Duration: scenario.Duration(time.Minute), Target: 1}},
		MaxRequests: int64(steps),
	}
}

type resolvedStepTree struct {
	step     *ScenarioStep
	src      *stepSource
	children []*resolvedStepTree
	els      []*resolvedStepTree
	branches []resolvedBranchTree
}

type resolvedBranchTree struct {
	name   string
	weight float64
	steps  []*resolvedStepTree
}

func (p *project) collectStepTrees(r *resolver, prefix string, steps []ScenarioStep) ([]*resolvedStepTree, error) {
	var out []*resolvedStepTree
	for i := range steps {
		st := &steps[i]
		if st.Disabled {
			continue
		}
		field := fmt.Sprintf("%s[%d]", prefix, i)
		stType := strings.ToLower(strings.TrimSpace(st.Type))
		node := &resolvedStepTree{step: st}
		switch stType {
		case "", "http", "ws", "grpc", "sse":
			a, e, err := p.loadAPI(st.APIID)
			if err != nil {
				if errors.Is(err, ErrNotFound) {
					return nil, invalid(field+".api_id", "引用的接口 %s 不存在", st.APIID)
				}
				return nil, err
			}
			src := &stepSource{field: field, api: a, folderRel: dirOf(e.rel), step: st}
			for _, ex := range src.extractors() {
				if !ex.Disabled {
					r.mutable[ex.Name] = true
				}
			}
			node.src = src
		case "script":
			if strings.TrimSpace(st.Script) == "" {
				return nil, invalid(field+".script", "脚本文本不能为空")
			}
			assigned, err := scenario.ExtractAssignedVars(st.Script)
			if err != nil {
				return nil, invalid(field+".script", "%v", err)
			}
			for _, vname := range assigned {
				r.mutable[vname] = true
			}
		case "group":
			ch, err := p.collectStepTrees(r, field+".children", st.Children)
			if err != nil {
				return nil, err
			}
			if len(ch) == 0 {
				return nil, invalid(field+".children", "事务分组至少需要一个启用的子步骤")
			}
			node.children = ch
		case "loop":
			ch, err := p.collectStepTrees(r, field+".children", st.Children)
			if err != nil {
				return nil, err
			}
			if len(ch) == 0 {
				return nil, invalid(field+".children", "循环步骤至少需要一个启用的子步骤")
			}
			node.children = ch
		case "if":
			ch, err := p.collectStepTrees(r, field+".children", st.Children)
			if err != nil {
				return nil, err
			}
			if len(ch) == 0 {
				return nil, invalid(field+".children", "条件分支至少需要一个启用的子步骤")
			}
			els, err := p.collectStepTrees(r, field+".else", st.Else)
			if err != nil {
				return nil, err
			}
			node.children = ch
			node.els = els
		case "weighted":
			for bIdx := range st.Branches {
				br := &st.Branches[bIdx]
				bSteps, err := p.collectStepTrees(r, fmt.Sprintf("%s.branches[%d].steps", field, bIdx), br.Steps)
				if err != nil {
					return nil, err
				}
				if len(bSteps) == 0 {
					return nil, invalid(fmt.Sprintf("%s.branches[%d].steps", field, bIdx), "权重分支至少需要一个启用的步骤")
				}
				node.branches = append(node.branches, resolvedBranchTree{
					name:   br.Name,
					weight: br.Weight,
					steps:  bSteps,
				})
			}
		}
		out = append(out, node)
	}
	return out, nil
}

func (r *resolver) buildScenarioSteps(trees []*resolvedStepTree) ([]scenario.Step, error) {
	out := make([]scenario.Step, 0, len(trees))
	for _, tn := range trees {
		st := tn.step
		stType := strings.ToLower(strings.TrimSpace(st.Type))
		switch stType {
		case "", "http", "ws", "grpc", "sse":
			step, err := r.step(st.ID, tn.src)
			if err != nil {
				return nil, err
			}
			out = append(out, step)
		case "script":
			out = append(out, scenario.Step{
				ID:     st.ID,
				Name:   st.Name,
				Think:  st.Think,
				Script: &scenario.ScriptStep{Code: st.Script},
			})
		case "group":
			ch, err := r.buildScenarioSteps(tn.children)
			if err != nil {
				return nil, err
			}
			out = append(out, scenario.Step{
				ID:    st.ID,
				Name:  st.Name,
				Think: st.Think,
				Group: &scenario.GroupStep{Children: ch},
			})
		case "loop":
			ch, err := r.buildScenarioSteps(tn.children)
			if err != nil {
				return nil, err
			}
			out = append(out, scenario.Step{
				ID:    st.ID,
				Name:  st.Name,
				Think: st.Think,
				Loop: &scenario.LoopStep{
					Count:         st.LoopCount,
					While:         st.While,
					MaxIterations: st.MaxIterations,
					Children:      ch,
				},
			})
		case "if":
			thenSteps, err := r.buildScenarioSteps(tn.children)
			if err != nil {
				return nil, err
			}
			var elseSteps []scenario.Step
			if len(tn.els) > 0 {
				elseSteps, err = r.buildScenarioSteps(tn.els)
				if err != nil {
					return nil, err
				}
			}
			out = append(out, scenario.Step{
				ID:    st.ID,
				Name:  st.Name,
				Think: st.Think,
				If: &scenario.IfStep{
					Expr: st.Expr,
					Then: thenSteps,
					Else: elseSteps,
				},
			})
		case "weighted":
			var brs []scenario.WeightedBranch
			for _, br := range tn.branches {
				bSteps, err := r.buildScenarioSteps(br.steps)
				if err != nil {
					return nil, err
				}
				brs = append(brs, scenario.WeightedBranch{
					Name:   br.name,
					Weight: br.weight,
					Steps:  bSteps,
				})
			}
			out = append(out, scenario.Step{
				ID:       st.ID,
				Name:     st.Name,
				Think:    st.Think,
				Weighted: &scenario.WeightedStep{Branches: brs},
			})
		}
	}
	return out, nil
}

func (p *project) resolveScenario(sc *Scenario, opts ResolveOptions) (*Resolved, error) {
	envID := opts.EnvironmentID
	if envID == "" {
		envID = sc.EnvironmentID
	}
	r, err := p.newResolver(envID, sc.Variables, opts)
	if err != nil {
		return nil, err
	}
	def := &scenario.Definition{Version: 1, Name: sc.Name, HTTP: sc.HTTP, Load: sc.Load}
	for i, d := range sc.Data {
		if err := r.file(fmt.Sprintf("data[%d].file", i), d.File); err != nil {
			return nil, err
		}
		def.Data = append(def.Data, d)
	}
	trees, err := p.collectStepTrees(r, "steps", sc.Steps)
	if err != nil {
		return nil, err
	}
	if len(trees) == 0 {
		return nil, invalid("steps", "场景没有启用的步骤")
	}
	def.Steps, err = r.buildScenarioSteps(trees)
	if err != nil {
		return nil, err
	}
	def.Variables = r.definitionVars()
	return r.result(def), nil
}

func (p *project) resolveAPI(a *API, folderRel string, opts APIRunOptions) (*Resolved, error) {
	r, err := p.newResolver(opts.EnvironmentID, nil, opts.ResolveOptions)
	if err != nil {
		return nil, err
	}
	src := &stepSource{api: a, folderRel: folderRel}
	for _, ex := range src.extractors() {
		if !ex.Disabled {
			r.mutable[ex.Name] = true
		}
	}
	step, err := r.step("step1", src)
	if err != nil {
		return nil, err
	}
	def := &scenario.Definition{Version: 1, Name: a.Name, HTTP: opts.HTTP, Steps: []scenario.Step{step}, Load: opts.Load}
	if len(def.Load.Stages) == 0 {
		def.Load = singleIteration(len(def.Steps))
	}
	def.Variables = r.definitionVars()
	return r.result(def), nil
}

// ResolveScenario turns a stored scenario into the exchange format:
// referenced APIs with folder inheritance, environment base URL and
// variables, data files checked to exist.
func (s *Store) ResolveScenario(projectID, scenarioID string, opts ResolveOptions) (*Resolved, error) {
	var out *Resolved
	err := s.with(projectID, func(p *project) error {
		sc, err := p.readScenario(scenarioID)
		if err != nil {
			return err
		}
		if err := sc.normalize(); err != nil {
			return err
		}
		out, err = p.resolveScenario(sc, opts)
		return err
	})
	return out, err
}

// ResolveScenarioDraft resolves a scenario that has not been saved.
func (s *Store) ResolveScenarioDraft(projectID string, in *Scenario, opts ResolveOptions) (*Resolved, error) {
	var out *Resolved
	err := s.with(projectID, func(p *project) error {
		sc, err := p.prepareScenario(in)
		if err != nil {
			return err
		}
		out, err = p.resolveScenario(sc, opts)
		return err
	})
	return out, err
}

// ResolveAPI builds a single step scenario from a stored API.
func (s *Store) ResolveAPI(projectID, apiID string, opts APIRunOptions) (*Resolved, error) {
	var out *Resolved
	err := s.with(projectID, func(p *project) error {
		a, e, err := p.loadAPI(apiID)
		if err != nil {
			return err
		}
		out, err = p.resolveAPI(a, dirOf(e.rel), opts)
		return err
	})
	return out, err
}

// ResolveAPIDraft builds a single step scenario from an API definition that
// may not be saved, such as the request being edited in a debug view.
// Folder inheritance comes from in.FolderID, or, when that is empty and
// in.ID names a stored API, from the stored API's folder. An empty name is
// allowed.
func (s *Store) ResolveAPIDraft(projectID string, in *API, opts APIRunOptions) (*Resolved, error) {
	var resolved *Resolved
	err := s.with(projectID, func(p *project) error {
		a := *in
		if strings.TrimSpace(a.Name) == "" {
			a.Name = "未命名接口"
		}
		if err := a.normalize(); err != nil {
			return err
		}
		folderRel, err := p.draftFolderRel(a.FolderID, a.ID)
		if err != nil {
			return err
		}
		out, err := p.resolveAPI(&a, folderRel, opts)
		if err != nil {
			return err
		}
		resolved = out
		return nil
	})
	return resolved, err
}

func (p *project) draftFolderRel(folderID, apiID string) (string, error) {
	switch {
	case folderID != "":
		_, e, err := p.loadFolder(folderID)
		if err != nil {
			return "", withFolderField(err)
		}
		return e.rel, nil
	case apiID != "":
		if _, e, err := p.loadAPI(apiID); err == nil {
			return dirOf(e.rel), nil
		}
	}
	return "", nil
}

// PreviewEffectiveAPI returns the inherited headers, effective auth, active
// variables and joined URL for an unsaved or saved API draft without failing
// on missing BaseURL or incomplete credentials.
func (s *Store) PreviewEffectiveAPI(projectID string, in *API, opts ResolveOptions) (*EffectivePreview, error) {
	var out *EffectivePreview
	err := s.with(projectID, func(p *project) error {
		a := *in
		folderRel, err := p.draftFolderRel(a.FolderID, a.ID)
		if err != nil {
			return err
		}
		chain, err := p.folderChain(folderRel)
		if err != nil {
			return err
		}

		// Variables: project < environment < overrides.
		varMap := map[string]ResolvedVariable{}
		var varOrder []string
		setVar := func(name, val, source, desc string, secret bool) {
			if _, ok := varMap[name]; !ok {
				varOrder = append(varOrder, name)
			}
			varMap[name] = ResolvedVariable{
				Name: name, Value: val, Source: source, Description: desc, Secret: secret,
			}
		}
		vault := p.secretVault()
		if pr, err := p.readProject(); err == nil {
			var projPlain map[string]string
			if vault != nil && opts.IncludeSecrets {
				projPlain, err = vault.ResolveVariables(context.Background(), p.id, "project")
				if err != nil {
					return err
				}
			}
			vars := pr.Variables
			if vault != nil && !opts.IncludeSecrets {
				vars = vault.MaskVariables(context.Background(), p.id, "project", vars)
			}
			for _, v := range vars {
				if !v.Disabled && strings.TrimSpace(v.Name) != "" {
					name := strings.TrimSpace(v.Name)
					val := string(v.Value)
					if v.Secret && projPlain != nil {
						if plain, ok := projPlain[name]; ok {
							val = plain
						}
					}
					setVar(name, val, "project", v.Description, v.Secret)
				}
			}
		}
		baseURL := ""
		if opts.EnvironmentID != "" {
			env, err := p.readEnvironment(opts.EnvironmentID)
			if err != nil {
				if errors.Is(err, ErrNotFound) {
					return invalid("environment_id", "环境 %s 不存在", opts.EnvironmentID)
				}
				return err
			}
			baseURL = env.BaseURL
			var envPlain map[string]string
			if vault != nil && opts.IncludeSecrets {
				envPlain, err = vault.ResolveVariables(context.Background(), p.id, env.ID)
				if err != nil {
					return err
				}
			}
			vars := env.Variables
			if vault != nil && !opts.IncludeSecrets {
				vars = vault.MaskVariables(context.Background(), p.id, env.ID, vars)
			}
			for _, v := range vars {
				if !v.Disabled && strings.TrimSpace(v.Name) != "" {
					name := strings.TrimSpace(v.Name)
					val := string(v.Value)
					if v.Secret && envPlain != nil {
						if plain, ok := envPlain[name]; ok {
							val = plain
						}
					}
					setVar(name, val, "environment", v.Description, v.Secret)
				}
			}
		}
		for _, k := range sortedKeys(opts.Variables) {
			setVar(k, string(opts.Variables[k]), "override", "", false)
		}

		// Effective auth and folder inherited headers.
		effAuth := EffectiveAuth{Source: "none"}
		var inherited []InheritedHeader
		for _, f := range chain {
			for _, h := range f.Headers {
				if h.Disabled || strings.TrimSpace(h.Name) == "" {
					continue
				}
				name := strings.TrimSpace(h.Name)
				for i := range inherited {
					if strings.EqualFold(inherited[i].Name, name) {
						inherited[i].Overridden = true
					}
				}
				inherited = append(inherited, InheritedHeader{
					Name:             name,
					Value:            string(h.Value),
					Description:      h.Description,
					SourceFolderID:   f.ID,
					SourceFolderName: f.Name,
				})
			}
			if !f.Auth.inherits() {
				effAuth = EffectiveAuth{
					Auth:             f.Auth,
					Source:           "folder",
					SourceFolderID:   f.ID,
					SourceFolderName: f.Name,
				}
			}
		}
		if !a.Auth.inherits() {
			effAuth = EffectiveAuth{Auth: a.Auth, Source: "api"}
		}
		if effAuth.Auth != nil && effAuth.Auth.Type == AuthNone {
			effAuth = EffectiveAuth{Auth: effAuth.Auth, Source: "none"}
		}

		// Mark inherited headers overridden by API headers or effective auth.
		markOverridden := func(name string) {
			for i := range inherited {
				if strings.EqualFold(inherited[i].Name, name) {
					inherited[i].Overridden = true
				}
			}
		}
		if effAuth.Auth != nil && (effAuth.Auth.Type == AuthBearer || effAuth.Auth.Type == AuthBasic || effAuth.Auth.Type == AuthOAuth2) {
			markOverridden("Authorization")
		} else if effAuth.Auth != nil && effAuth.Auth.Type == AuthAPIKey && effAuth.Auth.In != "query" && effAuth.Auth.Key != "" {
			markOverridden(effAuth.Auth.Key)
		}
		for _, h := range a.Request.Headers {
			if !h.Disabled && strings.TrimSpace(h.Name) != "" {
				markOverridden(strings.TrimSpace(h.Name))
			}
		}

		method := strings.ToUpper(strings.TrimSpace(a.Request.Method))
		if method == "" {
			method = "GET"
		}
		joinedURL := applyPathParams(strings.TrimSpace(a.Request.URL), a.Request.PathParams)
		lower := strings.ToLower(joinedURL)
		hasProtoPrefix := strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") ||
			strings.HasPrefix(lower, "ws://") || strings.HasPrefix(lower, "wss://") ||
			strings.HasPrefix(lower, "grpc://") || strings.HasPrefix(lower, "grpcs://") ||
			strings.HasPrefix(joinedURL, "{{")
		if joinedURL != "" && baseURL != "" && !hasProtoPrefix && strings.ToLower(strings.TrimSpace(a.Request.Protocol)) != "grpc" {
			joinedURL = strings.TrimRight(baseURL, "/") + "/" + strings.TrimLeft(joinedURL, "/")
		}

		varsOut := make([]ResolvedVariable, 0, len(varOrder))
		for _, k := range varOrder {
			varsOut = append(varsOut, varMap[k])
		}
		if inherited == nil {
			inherited = []InheritedHeader{}
		}
		out = &EffectivePreview{
			Method:           method,
			URL:              joinedURL,
			BaseURL:          baseURL,
			InheritedHeaders: inherited,
			EffectiveAuth:    effAuth,
			Variables:        varsOut,
		}
		return nil
	})
	return out, err
}
