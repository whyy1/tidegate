package exporter

import (
	"encoding/json"
	"net/url"
	"strings"

	"tidegate/internal/store"
)

// ExportPostman exports a Tidegate project as a Postman Collection v2.1.0 JSON
// document with secret variables and literal credentials scrubbed.
func ExportPostman(st *store.Store, projectID string) ([]byte, error) {
	pr, err := st.GetProject(projectID)
	if err != nil {
		return nil, err
	}
	envs, err := st.ListEnvironments(projectID)
	if err != nil {
		return nil, err
	}
	tree, err := st.Tree(projectID)
	if err != nil {
		return nil, err
	}

	var vars []any
	hasBaseURL := false
	for _, v := range pr.Variables {
		name := strings.TrimSpace(v.Name)
		if name == "" || v.Disabled {
			continue
		}
		if strings.EqualFold(name, "baseUrl") || strings.EqualFold(name, "base_url") {
			hasBaseURL = true
		}
		val := string(v.Value)
		vType := "string"
		if v.Secret {
			val = ""
			vType = "secret"
		}
		item := map[string]any{
			"key":   name,
			"value": val,
			"type":  vType,
		}
		if v.Description != "" {
			item["description"] = v.Description
		}
		vars = append(vars, item)
	}
	if !hasBaseURL && len(envs) > 0 && strings.TrimSpace(envs[0].BaseURL) != "" {
		vars = append(vars, map[string]any{
			"key":   "baseUrl",
			"value": strings.TrimSpace(envs[0].BaseURL),
			"type":  "string",
		})
	}

	var walkNodes func(nodes []*store.TreeNode) ([]any, error)
	walkNodes = func(nodes []*store.TreeNode) ([]any, error) {
		items := make([]any, 0, len(nodes))
		for _, n := range nodes {
			if n.Type == store.NodeFolder {
				fld, err := st.GetFolder(projectID, n.ID)
				if err != nil {
					return nil, err
				}
				children, err := walkNodes(n.Children)
				if err != nil {
					return nil, err
				}
				fObj := map[string]any{
					"name": fld.Name,
					"item": children,
				}
				if pa := postmanAuthObj(fld.Auth); pa != nil {
					fObj["auth"] = pa
				}
				items = append(items, fObj)
				continue
			}

			a, err := st.GetAPI(projectID, n.ID)
			if err != nil {
				return nil, err
			}
			items = append(items, postmanAPIItem(a))
		}
		return items, nil
	}

	topItems, err := walkNodes(tree.Children)
	if err != nil {
		return nil, err
	}

	info := map[string]any{
		"_postman_id": pr.ID,
		"name":        pr.Name,
		"schema":      "https://schema.getpostman.com/json/collection/v2.1.0/collection.json",
	}
	if pr.Description != "" {
		info["description"] = pr.Description
	}

	col := map[string]any{
		"info": info,
		"item": topItems,
	}
	if len(vars) > 0 {
		col["variable"] = vars
	}
	return json.MarshalIndent(col, "", "  ")
}

func postmanAPIItem(a *store.API) map[string]any {
	method := strings.ToUpper(strings.TrimSpace(a.Request.Method))
	if method == "" {
		method = "GET"
	}

	var headers []any
	for _, h := range a.Request.Headers {
		name := strings.TrimSpace(h.Name)
		if name == "" && string(h.Value) == "" {
			continue
		}
		val := string(h.Value)
		if h.Secret || (isSensitiveHeaderName(name) && !isTemplateExpr(val)) {
			val = ""
		}
		kv := map[string]any{
			"key":   name,
			"value": val,
		}
		if h.Disabled {
			kv["disabled"] = true
		}
		if h.Description != "" {
			kv["description"] = h.Description
		}
		headers = append(headers, kv)
	}

	req := map[string]any{
		"method": method,
		"url":    postmanURLObj(a.Request),
	}
	if len(headers) > 0 {
		req["header"] = headers
	}
	if a.Description != "" {
		req["description"] = a.Description
	}
	if body := postmanBodyObj(a.Request.Body); body != nil {
		req["body"] = body
	}
	if auth := postmanAuthObj(a.Auth); auth != nil {
		req["auth"] = auth
	}
	return map[string]any{
		"name":    a.Name,
		"request": req,
	}
}

func postmanURLObj(req store.APIRequest) map[string]any {
	raw := strings.TrimSpace(req.URL)
	if raw == "" {
		raw = "/"
	}
	if !strings.HasPrefix(strings.ToLower(raw), "http://") &&
		!strings.HasPrefix(strings.ToLower(raw), "https://") &&
		!strings.HasPrefix(raw, "{{") {
		if !strings.HasPrefix(raw, "/") {
			raw = "/" + raw
		}
		raw = "{{baseUrl}}" + raw
	}

	var queries []any
	for _, q := range req.Query {
		name := strings.TrimSpace(q.Name)
		if name == "" && string(q.Value) == "" {
			continue
		}
		val := string(q.Value)
		if q.Secret {
			val = ""
		}
		kv := map[string]any{
			"key":   name,
			"value": val,
		}
		if q.Disabled {
			kv["disabled"] = true
		}
		if q.Description != "" {
			kv["description"] = q.Description
		}
		queries = append(queries, kv)
	}

	var pathVars []any
	for _, p := range req.PathParams {
		name := strings.TrimSpace(p.Name)
		if name == "" || p.Disabled {
			continue
		}
		val := string(p.Value)
		if p.Secret {
			val = ""
		}
		pv := map[string]any{
			"key":   name,
			"value": val,
		}
		if p.Description != "" {
			pv["description"] = p.Description
		}
		pathVars = append(pathVars, pv)
	}

	out := map[string]any{
		"raw": raw,
	}
	// Derive host and path segments when standard URL or {{baseUrl}}/path.
	clean := raw
	if idx := strings.IndexAny(clean, "?#"); idx >= 0 {
		clean = clean[:idx]
	}
	if strings.HasPrefix(clean, "{{baseUrl}}") {
		out["host"] = []string{"{{baseUrl}}"}
		rest := strings.TrimPrefix(clean, "{{baseUrl}}")
		if segs := splitPostmanPath(rest); len(segs) > 0 {
			out["path"] = segs
		}
	} else if u, err := url.Parse(clean); err == nil && u.Host != "" {
		if u.Scheme != "" {
			out["protocol"] = u.Scheme
		}
		out["host"] = strings.Split(u.Hostname(), ".")
		if p := u.Port(); p != "" {
			out["port"] = p
		}
		if segs := splitPostmanPath(u.Path); len(segs) > 0 {
			out["path"] = segs
		}
	} else if segs := splitPostmanPath(clean); len(segs) > 0 {
		out["path"] = segs
	}

	if len(queries) > 0 {
		out["query"] = queries
	}
	if len(pathVars) > 0 {
		out["variable"] = pathVars
	}
	return out
}

func splitPostmanPath(p string) []string {
	p = strings.Trim(p, "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

func postmanBodyObj(b *store.APIBody) map[string]any {
	if b == nil || b.Type == "" || b.Type == "none" {
		return nil
	}
	switch b.Type {
	case "json":
		return map[string]any{
			"mode": "raw",
			"raw":  b.Content,
			"options": map[string]any{
				"raw": map[string]any{"language": "json"},
			},
		}
	case "form":
		return map[string]any{
			"mode":       "urlencoded",
			"urlencoded": postmanKVList(b.Fields),
		}
	case "multipart":
		items := postmanKVList(b.Fields)
		for _, bf := range b.Files {
			if bf.Disabled || strings.TrimSpace(bf.Field) == "" {
				continue
			}
			items = append(items, map[string]any{
				"key":  strings.TrimSpace(bf.Field),
				"type": "file",
				"src":  bf.Path,
			})
		}
		return map[string]any{
			"mode":     "formdata",
			"formdata": items,
		}
	case "binary":
		return map[string]any{
			"mode": "file",
			"file": map[string]any{"src": b.File},
		}
	default:
		lang := "text"
		if strings.Contains(strings.ToLower(b.ContentType), "xml") {
			lang = "xml"
		} else if strings.Contains(strings.ToLower(b.ContentType), "html") {
			lang = "html"
		}
		return map[string]any{
			"mode": "raw",
			"raw":  b.Content,
			"options": map[string]any{
				"raw": map[string]any{"language": lang},
			},
		}
	}
}

func postmanKVList(list store.KeyValueList) []any {
	items := make([]any, 0, len(list))
	for _, f := range list {
		name := strings.TrimSpace(f.Name)
		if name == "" && string(f.Value) == "" {
			continue
		}
		val := string(f.Value)
		if f.Secret {
			val = ""
		}
		row := map[string]any{
			"key":   name,
			"value": val,
			"type":  "text",
		}
		if f.Disabled {
			row["disabled"] = true
		}
		if f.Description != "" {
			row["description"] = f.Description
		}
		items = append(items, row)
	}
	return items
}

func postmanAuthObj(a *store.Auth) map[string]any {
	if a == nil || a.Type == "" || a.Type == store.AuthInherit || a.Type == store.AuthNone {
		return nil
	}
	switch a.Type {
	case store.AuthBearer:
		tok := a.Token
		if tok != "" && !isTemplateExpr(tok) {
			tok = ""
		}
		return map[string]any{
			"type": "bearer",
			"bearer": []any{
				map[string]any{"key": "token", "value": tok, "type": "string"},
			},
		}
	case store.AuthBasic:
		pwd := a.Password
		if pwd != "" && !isTemplateExpr(pwd) {
			pwd = ""
		}
		return map[string]any{
			"type": "basic",
			"basic": []any{
				map[string]any{"key": "username", "value": a.Username, "type": "string"},
				map[string]any{"key": "password", "value": pwd, "type": "string"},
			},
		}
	case store.AuthAPIKey:
		in := a.In
		if in != "query" {
			in = "header"
		}
		val := a.Value
		if val != "" && !isTemplateExpr(val) {
			val = ""
		}
		return map[string]any{
			"type": "apikey",
			"apikey": []any{
				map[string]any{"key": "key", "value": a.Key, "type": "string"},
				map[string]any{"key": "value", "value": val, "type": "string"},
				map[string]any{"key": "in", "value": in, "type": "string"},
			},
		}
	}
	return nil
}
