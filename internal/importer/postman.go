package importer

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"tidegate/internal/scenario"
	"tidegate/internal/store"
)

type postmanEnvFile struct {
	Name   string       `json:"name"`
	Scope  string       `json:"_postman_variable_scope"`
	Values []postmanVar `json:"values"`
}

type postmanVar struct {
	Key         string `json:"key"`
	Value       string `json:"value"`
	Type        string `json:"type"`
	Description string `json:"description"`
	Enabled     *bool  `json:"enabled"`
	Disabled    bool   `json:"disabled"`
}

type postmanCollection struct {
	Info struct {
		Name        string `json:"name"`
		Description any    `json:"description"`
		Schema      string `json:"schema"`
	} `json:"info"`
	Item     []postmanItem `json:"item"`
	Variable []postmanVar  `json:"variable"`
	Auth     *postmanAuth  `json:"auth"`
}

type postmanItem struct {
	Name    string          `json:"name"`
	Item    []postmanItem   `json:"item"`
	Request *postmanRequest `json:"request"`
	Auth    *postmanAuth    `json:"auth"`
	Event   []postmanEvent  `json:"event"`
}

type postmanEvent struct {
	Listen string `json:"listen"`
}

type postmanAuth struct {
	Type   string            `json:"type"`
	Bearer []postmanAuthAttr `json:"bearer"`
	Basic  []postmanAuthAttr `json:"basic"`
	APIKey []postmanAuthAttr `json:"apikey"`
}

type postmanAuthAttr struct {
	Key   string `json:"key"`
	Value any    `json:"value"`
}

type postmanRequest struct {
	Method      string        `json:"method"`
	URL         any           `json:"url"`
	Header      []postmanKV   `json:"header"`
	Body        *postmanBody  `json:"body"`
	Auth        *postmanAuth  `json:"auth"`
	Description any           `json:"description"`
}

type postmanKV struct {
	Key         string `json:"key"`
	Value       string `json:"value"`
	Type        string `json:"type"`
	Src         any    `json:"src"`
	Description string `json:"description"`
	Disabled    bool   `json:"disabled"`
}

type postmanBody struct {
	Mode       string      `json:"mode"`
	Raw        string      `json:"raw"`
	Urlencoded []postmanKV `json:"urlencoded"`
	Formdata   []postmanKV `json:"formdata"`
	Options    struct {
		Raw struct {
			Language string `json:"language"`
		} `json:"raw"`
	} `json:"options"`
}

type postmanURL struct {
	Raw      string       `json:"raw"`
	Protocol string       `json:"protocol"`
	Host     []string     `json:"host"`
	Port     string       `json:"port"`
	Path     []string     `json:"path"`
	Query    []postmanKV  `json:"query"`
	Variable []postmanVar `json:"variable"`
}

func parsePostman(content []byte) (*parsedBundle, error) {
	trimmed := strings.TrimSpace(strings.TrimPrefix(string(content), "\xef\xbb\xbf"))
	var probe map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &probe); err != nil {
		return nil, fmt.Errorf("解析 Postman JSON 失败：%w", err)
	}

	report := &ConversionReport{
		Format: FormatPostman,
	}

	// Distinguish Postman Environment vs Collection.
	if _, hasScope := probe["_postman_variable_scope"]; hasScope {
		return parsePostmanEnv([]byte(trimmed), report)
	}
	if _, hasValues := probe["values"]; hasValues {
		if _, hasInfo := probe["info"]; !hasInfo {
			return parsePostmanEnv([]byte(trimmed), report)
		}
	}

	var col postmanCollection
	if err := json.Unmarshal([]byte(trimmed), &col); err != nil {
		return nil, fmt.Errorf("解析 Postman Collection 失败：%w", err)
	}
	if strings.TrimSpace(col.Info.Name) == "" && len(col.Item) == 0 {
		return nil, errors.New("Postman 文件中未找到有效的 Collection 内容")
	}

	report.DetectedVersion = col.Info.Schema
	b := &parsedBundle{
		ProjectName: strings.TrimSpace(col.Info.Name),
		ProjectDesc: postmanDescString(col.Info.Description),
		Report:      report,
	}

	for _, v := range col.Variable {
		if strings.TrimSpace(v.Key) == "" {
			continue
		}
		disabled := v.Disabled || (v.Enabled != nil && !*v.Enabled)
		b.ProjectVars = append(b.ProjectVars, store.KeyValue{
			Name:        strings.TrimSpace(v.Key),
			Value:       scenario.Scalar(v.Value),
			Description: v.Description,
			Disabled:    disabled,
			Secret:      strings.EqualFold(v.Type, "secret"),
		})
	}

	colAuth := convertPostmanAuth(col.Auth, col.Info.Name, report)
	order := int64(1024)
	var walkItems func(folderPath []string, items []postmanItem, applyCollectionAuth bool)
	walkItems = func(folderPath []string, items []postmanItem, applyCollectionAuth bool) {
		for idx, it := range items {
			if len(it.Event) > 0 {
				var kinds []string
				for _, ev := range it.Event {
					if ev.Listen != "" {
						kinds = append(kinds, ev.Listen)
					}
				}
				if len(kinds) > 0 {
					report.Skipped = append(report.Skipped, fmt.Sprintf("%s：脚本事件（%s）不支持自动转换，已跳过", it.Name, strings.Join(kinds, "/")))
				}
			}
			if it.Request == nil {
				// Folder node.
				name := strings.TrimSpace(it.Name)
				if name == "" {
					name = fmt.Sprintf("文件夹 %d", idx+1)
				}
				nextPath := append(append([]string(nil), folderPath...), name)
				fAuth := convertPostmanAuth(it.Auth, name, report)
				if fAuth == nil && applyCollectionAuth && len(folderPath) == 0 {
					fAuth = colAuth
				}
				b.Folders = append(b.Folders, parsedFolder{
					Path:  nextPath,
					Order: int64((idx + 1) * 1024),
					Auth:  fAuth,
				})
				walkItems(nextPath, it.Item, false)
				continue
			}

			rawURL, queryList, pathVars := decodePostmanURL(it.Request.URL)
			var headers store.KeyValueList
			for _, h := range it.Request.Header {
				if strings.TrimSpace(h.Key) == "" {
					continue
				}
				headers = append(headers, store.KeyValue{
					Name:        strings.TrimSpace(h.Key),
					Value:       scenario.Scalar(h.Value),
					Description: h.Description,
					Disabled:    h.Disabled,
				})
			}

			var body *store.APIBody
			if it.Request.Body != nil {
				body = convertPostmanBody(it.Request.Body, headers, report, it.Name)
			}

			auth := convertPostmanAuth(it.Request.Auth, it.Name, report)
			if auth == nil && len(folderPath) == 0 && colAuth != nil {
				auth = colAuth
			}
			method := strings.ToUpper(strings.TrimSpace(it.Request.Method))
			if method == "" {
				method = "GET"
			}
			name := strings.TrimSpace(it.Name)
			if name == "" {
				name = method + " " + rawURL
			}

			b.APIs = append(b.APIs, parsedAPI{
				FolderPath: append([]string(nil), folderPath...),
				API: store.API{
					Name:        name,
					Order:       order,
					Description: postmanDescString(it.Request.Description),
					Request: store.APIRequest{
						Method:     method,
						URL:        rawURL,
						PathParams: pathVars,
						Query:      queryList,
						Headers:    headers,
						Body:       body,
					},
					Auth: auth,
				},
			})
			order += 1024
		}
	}

	walkItems(nil, col.Item, true)
	report.APIsFound = len(b.APIs)
	return b, nil
}

func parsePostmanEnv(raw []byte, report *ConversionReport) (*parsedBundle, error) {
	var env postmanEnvFile
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("解析 Postman 环境失败：%w", err)
	}
	report.DetectedVersion = "environment"
	name := strings.TrimSpace(env.Name)
	if name == "" {
		name = "导入环境"
	}
	var vars store.KeyValueList
	var baseURL string
	for _, v := range env.Values {
		key := strings.TrimSpace(v.Key)
		if key == "" {
			continue
		}
		disabled := v.Disabled || (v.Enabled != nil && !*v.Enabled)
		if baseURL == "" && (strings.EqualFold(key, "base_url") || strings.EqualFold(key, "baseUrl")) && !disabled {
			baseURL = v.Value
		}
		vars = append(vars, store.KeyValue{
			Name:        key,
			Value:       scenario.Scalar(v.Value),
			Description: v.Description,
			Disabled:    disabled,
			Secret:      strings.EqualFold(v.Type, "secret"),
		})
	}
	return &parsedBundle{
		Envs: []parsedEnv{{
			Env: store.Environment{
				Name:      name,
				BaseURL:   baseURL,
				Variables: vars,
			},
		}},
		Report: report,
	}, nil
}

func decodePostmanURL(raw any) (baseURL string, query store.KeyValueList, pathParams store.KeyValueList) {
	if raw == nil {
		return "", nil, nil
	}
	if s, ok := raw.(string); ok {
		return splitSimpleQuery(s), nil, nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return "", nil, nil
	}
	var u postmanURL
	if err := json.Unmarshal(b, &u); err != nil {
		return "", nil, nil
	}
	for _, q := range u.Query {
		if strings.TrimSpace(q.Key) == "" {
			continue
		}
		query = append(query, store.KeyValue{
			Name:        strings.TrimSpace(q.Key),
			Value:       scenario.Scalar(q.Value),
			Description: q.Description,
			Disabled:    q.Disabled,
		})
	}
	for _, v := range u.Variable {
		if strings.TrimSpace(v.Key) == "" {
			continue
		}
		pathParams = append(pathParams, store.KeyValue{
			Name:        strings.TrimSpace(v.Key),
			Value:       scenario.Scalar(v.Value),
			Description: v.Description,
			Disabled:    v.Disabled,
		})
	}
	if len(u.Host) > 0 || len(u.Path) > 0 {
		host := strings.Join(u.Host, ".")
		if u.Port != "" && host != "" {
			host += ":" + u.Port
		}
		p := strings.Join(u.Path, "/")
		switch {
		case u.Protocol != "" && host != "":
			baseURL = u.Protocol + "://" + host
			if p != "" {
				baseURL += "/" + strings.TrimLeft(p, "/")
			}
		case host != "" && strings.HasPrefix(host, "{{"):
			baseURL = host
			if p != "" {
				baseURL += "/" + strings.TrimLeft(p, "/")
			}
		default:
			if p != "" {
				baseURL = "/" + strings.TrimLeft(p, "/")
			} else {
				baseURL = splitSimpleQuery(u.Raw)
			}
		}
	} else {
		baseURL = splitSimpleQuery(u.Raw)
	}
	return baseURL, query, pathParams
}

func splitSimpleQuery(rawURL string) string {
	s := strings.TrimSpace(rawURL)
	if idx := strings.IndexByte(s, '?'); idx >= 0 {
		return s[:idx]
	}
	return s
}

func convertPostmanBody(pb *postmanBody, headers store.KeyValueList, report *ConversionReport, label string) *store.APIBody {
	switch strings.ToLower(strings.TrimSpace(pb.Mode)) {
	case "raw":
		lang := strings.ToLower(strings.TrimSpace(pb.Options.Raw.Language))
		if lang == "json" || hasContentTypeJSON(headers) {
			return &store.APIBody{Type: "json", Content: pb.Raw}
		}
		return &store.APIBody{Type: "raw", Content: pb.Raw}
	case "urlencoded":
		var fields store.KeyValueList
		for _, f := range pb.Urlencoded {
			if strings.TrimSpace(f.Key) == "" {
				continue
			}
			fields = append(fields, store.KeyValue{
				Name:        strings.TrimSpace(f.Key),
				Value:       scenario.Scalar(f.Value),
				Description: f.Description,
				Disabled:    f.Disabled,
			})
		}
		return &store.APIBody{Type: "form", Fields: fields}
	case "formdata":
		var fields store.KeyValueList
		var files []scenario.BodyFile
		for _, f := range pb.Formdata {
			if strings.TrimSpace(f.Key) == "" {
				continue
			}
			if strings.EqualFold(f.Type, "file") {
				files = append(files, scenario.BodyFile{
					Field:    strings.TrimSpace(f.Key),
					Path:     strings.TrimSpace(f.Key) + ".bin",
					Disabled: f.Disabled,
				})
			} else {
				fields = append(fields, store.KeyValue{
					Name:        strings.TrimSpace(f.Key),
					Value:       scenario.Scalar(f.Value),
					Description: f.Description,
					Disabled:    f.Disabled,
				})
			}
		}
		return &store.APIBody{Type: "multipart", Fields: fields, Files: files}
	case "file":
		return &store.APIBody{Type: "binary"}
	case "", "none":
		return nil
	default:
		report.Skipped = append(report.Skipped, fmt.Sprintf("%s：请求体模式 %q 暂不支持，已跳过", label, pb.Mode))
		return nil
	}
}

func convertPostmanAuth(a *postmanAuth, label string, report *ConversionReport) *store.Auth {
	if a == nil || a.Type == "" {
		return nil
	}
	kvVal := func(attrs []postmanAuthAttr, k string) string {
		for _, at := range attrs {
			if strings.EqualFold(at.Key, k) {
				return asString(at.Value)
			}
		}
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(a.Type)) {
	case "noauth":
		return &store.Auth{Type: store.AuthNone}
	case "bearer":
		return &store.Auth{Type: store.AuthBearer, Token: kvVal(a.Bearer, "token")}
	case "basic":
		return &store.Auth{Type: store.AuthBasic, Username: kvVal(a.Basic, "username"), Password: kvVal(a.Basic, "password")}
	case "apikey":
		in := strings.ToLower(kvVal(a.APIKey, "in"))
		if in != "query" {
			in = "header"
		}
		return &store.Auth{Type: store.AuthAPIKey, Key: kvVal(a.APIKey, "key"), Value: kvVal(a.APIKey, "value"), In: in}
	default:
		report.Skipped = append(report.Skipped, fmt.Sprintf("%s：Postman 鉴权类型 %q 暂不支持，已跳过", label, a.Type))
		return nil
	}
}

func hasContentTypeJSON(headers store.KeyValueList) bool {
	for _, h := range headers {
		if strings.EqualFold(h.Name, "Content-Type") && strings.Contains(strings.ToLower(string(h.Value)), "json") {
			return true
		}
	}
	return false
}

func postmanDescString(v any) string {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case map[string]any:
		return strings.TrimSpace(asString(x["content"]))
	}
	return ""
}
