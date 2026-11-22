package importer

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"tidegate/internal/scenario"
	"tidegate/internal/store"
)

type apifoxDoc struct {
	ApifoxProject string `json:"apifoxProject"`
	Info          struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	} `json:"info"`
	Environments  []apifoxEnv        `json:"environments"`
	APICollection []apifoxTreeItem   `json:"apiCollection"`
}

type apifoxEnv struct {
	Name      string `json:"name"`
	BaseURL   string `json:"baseUrl"`
	BaseURLs  []struct {
		Name string `json:"name"`
		URL  string `json:"url"`
	} `json:"baseUrls"`
	Variables []apifoxVar `json:"variables"`
}

type apifoxVar struct {
	Name        string `json:"name"`
	Key         string `json:"key"`
	Value       any    `json:"value"`
	Type        string `json:"type"`
	Description string `json:"description"`
	Enable      *bool  `json:"enable"`
}

type apifoxTreeItem struct {
	Name     string           `json:"name"`
	Items    []apifoxTreeItem `json:"items"`
	Children []apifoxTreeItem `json:"children"`
	Auth     *apifoxAuth      `json:"auth"`
	API      *apifoxAPI       `json:"api"`
}

type apifoxAPI struct {
	ID             string                `json:"id"`
	Name           string                `json:"name"`
	Method         string                `json:"method"`
	Path           string                `json:"path"`
	URL            string                `json:"url"`
	Description    string                `json:"description"`
	Auth           *apifoxAuth           `json:"auth"`
	Parameters     apifoxParameters      `json:"parameters"`
	RequestBody    *apifoxRequestBody    `json:"requestBody"`
	Responses      []apifoxResponse      `json:"responses"`
	ResponseEx     []apifoxResponseEx    `json:"responseExamples"`
	PreProcessors  []apifoxProcessor     `json:"preProcessors"`
	PostProcessors []apifoxProcessor     `json:"postProcessors"`
}

type apifoxProcessor struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

type apifoxAuth struct {
	Type   string            `json:"type"`
	Bearer map[string]string `json:"bearer"`
	Basic  map[string]string `json:"basic"`
	APIKey map[string]string `json:"apikey"`
	OAuth2 map[string]string `json:"oauth2"`
}

type apifoxParameters struct {
	Path   []apifoxParam `json:"path"`
	Query  []apifoxParam `json:"query"`
	Header []apifoxParam `json:"header"`
}

type apifoxParam struct {
	Name        string `json:"name"`
	Example     any    `json:"example"`
	Value       any    `json:"value"`
	Description string `json:"description"`
	Enable      *bool  `json:"enable"`
	Required    bool   `json:"required"`
}

type apifoxRequestBody struct {
	Type       string           `json:"type"`
	Parameters []apifoxBodyParam `json:"parameters"`
	Example    any              `json:"example"`
	JSONSchema map[string]any   `json:"jsonSchema"`
}

type apifoxBodyParam struct {
	Name        string `json:"name"`
	Example     any    `json:"example"`
	Value       any    `json:"value"`
	Type        string `json:"type"`
	Description string `json:"description"`
	Enable      *bool  `json:"enable"`
}

type apifoxResponse struct {
	Name        string         `json:"name"`
	Code        int            `json:"code"`
	ContentType string         `json:"contentType"`
	JSONSchema  map[string]any `json:"jsonSchema"`
}

type apifoxResponseEx struct {
	Name        string         `json:"name"`
	ResponseCode int           `json:"responseCode"`
	Status      int            `json:"status"`
	ContentType string         `json:"contentType"`
	Data        any            `json:"data"`
	Body        any            `json:"body"`
}

func parseApifox(content []byte) (*parsedBundle, error) {
	trimmed := strings.TrimSpace(strings.TrimPrefix(string(content), "\xef\xbb\xbf"))
	var doc apifoxDoc
	if err := json.Unmarshal([]byte(trimmed), &doc); err != nil {
		return nil, fmt.Errorf("解析 Apifox JSON 失败：%w", err)
	}
	if len(doc.APICollection) == 0 && len(doc.Environments) == 0 {
		return nil, errors.New("Apifox 文件中不包含接口集合或环境数据")
	}

	b := &parsedBundle{
		ProjectName: strings.TrimSpace(doc.Info.Name),
		ProjectDesc: strings.TrimSpace(doc.Info.Description),
		Report: &ConversionReport{
			Format:          FormatApifox,
			DetectedVersion: doc.ApifoxProject,
		},
	}

	for _, env := range doc.Environments {
		base := strings.TrimSpace(env.BaseURL)
		if base == "" && len(env.BaseURLs) > 0 {
			base = strings.TrimSpace(env.BaseURLs[0].URL)
		}
		var vars store.KeyValueList
		for _, ev := range env.Variables {
			name := strings.TrimSpace(ev.Name)
			if name == "" {
				name = strings.TrimSpace(ev.Key)
			}
			if name == "" {
				continue
			}
			disabled := ev.Enable != nil && !*ev.Enable
			vars = append(vars, store.KeyValue{
				Name:        name,
				Value:       scenario.Scalar(anyString(ev.Value)),
				Description: strings.TrimSpace(ev.Description),
				Disabled:    disabled,
				Secret:      strings.EqualFold(ev.Type, "secret"),
			})
		}
		envName := strings.TrimSpace(env.Name)
		if envName == "" {
			envName = "Apifox 环境"
		}
		b.Envs = append(b.Envs, parsedEnv{
			Env: store.Environment{
				Name:      envName,
				BaseURL:   base,
				Variables: vars,
			},
		})
	}

	var walk func(items []apifoxTreeItem, folderPath []string)
	walk = func(items []apifoxTreeItem, folderPath []string) {
		for _, item := range items {
			if item.API != nil {
				apiObj := convertApifoxAPI(item.API, b.Report)
				b.APIs = append(b.APIs, parsedAPI{
					OldID:      item.API.ID,
					FolderPath: append([]string(nil), folderPath...),
					API:        apiObj,
				})
				continue
			}
			sub := item.Items
			if len(sub) == 0 {
				sub = item.Children
			}
			seg := strings.TrimSpace(item.Name)
			nextPath := append([]string(nil), folderPath...)
			if seg != "" {
				nextPath = append(nextPath, seg)
				b.Folders = append(b.Folders, parsedFolder{
					Path: nextPath,
					Auth: convertApifoxAuth(item.Auth),
				})
			}
			if len(sub) > 0 {
				walk(sub, nextPath)
			}
		}
	}
	walk(doc.APICollection, nil)

	if len(b.APIs) == 0 {
		return nil, errors.New("Apifox 文件中未找到可导入的接口定义")
	}
	return b, nil
}

func convertApifoxAPI(in *apifoxAPI, report *ConversionReport) store.API {
	name := strings.TrimSpace(in.Name)
	method := strings.ToUpper(strings.TrimSpace(in.Method))
	if method == "" {
		method = "GET"
	}
	rawURL := strings.TrimSpace(in.Path)
	if rawURL == "" {
		rawURL = strings.TrimSpace(in.URL)
	}
	if name == "" {
		name = method + " " + rawURL
	}

	for _, pr := range in.PreProcessors {
		if strings.Contains(strings.ToLower(pr.Type), "script") {
			report.Skipped = append(report.Skipped, fmt.Sprintf("接口 %q 包含前置脚本 (%s)，已跳过脚本", name, pr.Type))
		}
	}
	for _, pp := range in.PostProcessors {
		if strings.Contains(strings.ToLower(pp.Type), "script") {
			report.Skipped = append(report.Skipped, fmt.Sprintf("接口 %q 包含后置脚本 (%s)，已跳过脚本", name, pp.Type))
		}
	}

	var pathParams, queryParams, headers store.KeyValueList
	for _, p := range in.Parameters.Path {
		if strings.TrimSpace(p.Name) == "" {
			continue
		}
		pathParams = append(pathParams, apifoxParamToKV(p))
		rawURL = strings.ReplaceAll(rawURL, "{"+strings.TrimSpace(p.Name)+"}", ":"+strings.TrimSpace(p.Name))
	}
	for _, q := range in.Parameters.Query {
		if strings.TrimSpace(q.Name) == "" {
			continue
		}
		queryParams = append(queryParams, apifoxParamToKV(q))
	}
	for _, h := range in.Parameters.Header {
		if strings.TrimSpace(h.Name) == "" {
			continue
		}
		headers = append(headers, apifoxParamToKV(h))
	}

	var body *store.APIBody
	if rb := in.RequestBody; rb != nil && !strings.EqualFold(rb.Type, "none") && rb.Type != "" {
		bt := strings.ToLower(strings.TrimSpace(rb.Type))
		switch {
		case strings.Contains(bt, "json"):
			content := anyJSONString(rb.Example)
			if content == "" && len(rb.JSONSchema) > 0 {
				content = "{}"
			}
			body = &store.APIBody{Type: "json", Content: content}
		case strings.Contains(bt, "x-www-form-urlencoded"):
			var fields store.KeyValueList
			for _, fp := range rb.Parameters {
				if strings.TrimSpace(fp.Name) == "" {
					continue
				}
				fields = append(fields, store.KeyValue{
					Name:        strings.TrimSpace(fp.Name),
					Value:       scenario.Scalar(firstNonEmpty(anyString(fp.Value), anyString(fp.Example))),
					Description: strings.TrimSpace(fp.Description),
					Disabled:    fp.Enable != nil && !*fp.Enable,
				})
			}
			body = &store.APIBody{Type: "form", Fields: fields}
		case strings.Contains(bt, "multipart"):
			var fields store.KeyValueList
			for _, fp := range rb.Parameters {
				if strings.TrimSpace(fp.Name) == "" || strings.EqualFold(fp.Type, "file") {
					continue
				}
				fields = append(fields, store.KeyValue{
					Name:        strings.TrimSpace(fp.Name),
					Value:       scenario.Scalar(firstNonEmpty(anyString(fp.Value), anyString(fp.Example))),
					Description: strings.TrimSpace(fp.Description),
					Disabled:    fp.Enable != nil && !*fp.Enable,
				})
			}
			body = &store.APIBody{Type: "multipart", Fields: fields}
		default:
			body = &store.APIBody{
				Type:        "raw",
				ContentType: rb.Type,
				Content:     anyJSONString(rb.Example),
			}
		}
	}

	var examples []store.APIResponseExample
	for _, ex := range in.ResponseEx {
		code := ex.ResponseCode
		if code == 0 {
			code = ex.Status
		}
		if code == 0 {
			code = 200
		}
		payload := ex.Body
		if payload == nil {
			payload = ex.Data
		}
		examples = append(examples, store.APIResponseExample{
			Name:        strings.TrimSpace(ex.Name),
			Status:      code,
			ContentType: firstNonEmpty(strings.TrimSpace(ex.ContentType), "application/json"),
			Body:        anyJSONString(payload),
		})
	}

	var respFields []store.APIFieldDoc
	for _, resp := range in.Responses {
		if resp.Code >= 200 && resp.Code < 300 && len(resp.JSONSchema) > 0 {
			appendApifoxSchemaFields("$", resp.JSONSchema, &respFields)
			break
		}
	}

	return store.API{
		Name:        name,
		Description: strings.TrimSpace(in.Description),
		Auth:        convertApifoxAuth(in.Auth),
		Request: store.APIRequest{
			Method:     method,
			URL:        rawURL,
			PathParams: pathParams,
			Query:      queryParams,
			Headers:    headers,
			Body:       body,
		},
		Examples:       examples,
		ResponseFields: respFields,
	}
}

func convertApifoxAuth(a *apifoxAuth) *store.Auth {
	if a == nil || a.Type == "" {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(a.Type)) {
	case "inherit":
		return &store.Auth{Type: store.AuthInherit}
	case "noauth", "none":
		return &store.Auth{Type: store.AuthNone}
	case "bearer":
		return &store.Auth{Type: store.AuthBearer, Token: a.Bearer["token"]}
	case "basic":
		return &store.Auth{Type: store.AuthBasic, Username: a.Basic["username"], Password: a.Basic["password"]}
	case "apikey":
		in := a.APIKey["in"]
		if in != "query" {
			in = "header"
		}
		return &store.Auth{Type: store.AuthAPIKey, Key: a.APIKey["key"], Value: a.APIKey["value"], In: in}
	case "oauth2":
		return &store.Auth{
			Type:         store.AuthOAuth2,
			TokenURL:     a.OAuth2["accessTokenUrl"],
			ClientID:     a.OAuth2["clientId"],
			ClientSecret: a.OAuth2["clientSecret"],
			Scope:        a.OAuth2["scope"],
		}
	}
	return nil
}

func apifoxParamToKV(p apifoxParam) store.KeyValue {
	return store.KeyValue{
		Name:        strings.TrimSpace(p.Name),
		Value:       scenario.Scalar(firstNonEmpty(anyString(p.Value), anyString(p.Example))),
		Description: strings.TrimSpace(p.Description),
		Disabled:    p.Enable != nil && !*p.Enable,
	}
}

func appendApifoxSchemaFields(prefix string, schema map[string]any, out *[]store.APIFieldDoc) {
	props, _ := schema["properties"].(map[string]any)
	reqSet := map[string]bool{}
	if reqRaw, ok := schema["required"].([]any); ok {
		for _, r := range reqRaw {
			if s, ok := r.(string); ok {
				reqSet[s] = true
			}
		}
	}
	for k, rawChild := range props {
		child, _ := rawChild.(map[string]any)
		if child == nil {
			continue
		}
		fieldPath := prefix + "." + k
		ftype, _ := child["type"].(string)
		if ftype == "" {
			ftype = "string"
		}
		desc, _ := child["description"].(string)
		*out = append(*out, store.APIFieldDoc{
			Path:        fieldPath,
			Type:        ftype,
			Required:    reqSet[k],
			Description: desc,
		})
		if ftype == "object" {
			appendApifoxSchemaFields(fieldPath, child, out)
		}
	}
}

func anyString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		if x {
			return "true"
		}
		return "false"
	default:
		b, err := json.Marshal(x)
		if err != nil {
			return fmt.Sprint(x)
		}
		return string(b)
	}
}

func anyJSONString(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return anyString(v)
	}
	return string(b)
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
