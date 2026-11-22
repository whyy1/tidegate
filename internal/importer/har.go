package importer

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"

	"tidegate/internal/scenario"
	"tidegate/internal/store"
)

type harDocument struct {
	Log struct {
		Version string     `json:"version"`
		Entries []harEntry `json:"entries"`
	} `json:"log"`
}

type harEntry struct {
	Request struct {
		Method      string      `json:"method"`
		URL         string      `json:"url"`
		Headers     []harKV     `json:"headers"`
		QueryString []harKV     `json:"queryString"`
		PostData    *harPost    `json:"postData"`
	} `json:"request"`
}

type harKV struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type harPost struct {
	MimeType string  `json:"mimeType"`
	Text     string  `json:"text"`
	Params   []harKV `json:"params"`
}

var staticAssetExts = map[string]bool{
	".js": true, ".mjs": true, ".css": true, ".map": true,
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true,
	".svg": true, ".ico": true, ".webp": true, ".avif": true,
	".woff": true, ".woff2": true, ".ttf": true, ".eot": true,
}

func parseHAR(content []byte) (*parsedBundle, error) {
	trimmed := strings.TrimSpace(strings.TrimPrefix(string(content), "\xef\xbb\xbf"))
	var doc harDocument
	if err := json.Unmarshal([]byte(trimmed), &doc); err != nil {
		return nil, fmt.Errorf("解析 HAR JSON 失败：%w", err)
	}
	if len(doc.Log.Entries) == 0 {
		return nil, errors.New("HAR 文件不包含任何请求记录")
	}

	report := &ConversionReport{
		Format:          FormatHAR,
		DetectedVersion: doc.Log.Version,
	}
	b := &parsedBundle{Report: report}

	envOrigins := map[string]bool{}
	seenReq := map[string]bool{}
	staticFiltered := 0
	nonHTTPFiltered := 0
	order := int64(1024)

	for _, entry := range doc.Log.Entries {
		u, err := url.Parse(strings.TrimSpace(entry.Request.URL))
		if err != nil {
			continue
		}
		scheme := strings.ToLower(u.Scheme)
		if scheme != "http" && scheme != "https" {
			nonHTTPFiltered++
			continue
		}
		ext := strings.ToLower(path.Ext(u.Path))
		if staticAssetExts[ext] {
			staticFiltered++
			continue
		}

		origin := scheme + "://" + u.Host
		if !envOrigins[origin] {
			envOrigins[origin] = true
			b.Envs = append(b.Envs, parsedEnv{
				Env: store.Environment{
					Name:    u.Host,
					BaseURL: origin,
				},
			})
		}

		method := strings.ToUpper(strings.TrimSpace(entry.Request.Method))
		if method == "" {
			method = "GET"
		}
		relPath := u.Path
		if relPath == "" {
			relPath = "/"
		}
		dedupKey := method + " " + origin + relPath
		if seenReq[dedupKey] {
			continue
		}
		seenReq[dedupKey] = true

		var queryList store.KeyValueList
		if len(entry.Request.QueryString) > 0 {
			for _, q := range entry.Request.QueryString {
				if q.Name != "" {
					queryList = append(queryList, store.KeyValue{Name: q.Name, Value: scenario.Scalar(q.Value)})
				}
			}
		} else if u.RawQuery != "" {
			for k, vs := range u.Query() {
				for _, v := range vs {
					queryList = append(queryList, store.KeyValue{Name: k, Value: scenario.Scalar(v)})
				}
			}
		}

		var headers store.KeyValueList
		var auth *store.Auth
		for _, h := range entry.Request.Headers {
			name := strings.TrimSpace(h.Name)
			if name == "" || strings.HasPrefix(name, ":") ||
				strings.EqualFold(name, "Host") ||
				strings.EqualFold(name, "Content-Length") ||
				strings.EqualFold(name, "Connection") {
				continue
			}
			if auth == nil && strings.EqualFold(name, "Authorization") {
				val := strings.TrimSpace(h.Value)
				if len(val) >= 7 && strings.EqualFold(val[:7], "bearer ") {
					auth = &store.Auth{Type: store.AuthBearer, Token: strings.TrimSpace(val[7:])}
					continue
				}
			}
			headers = append(headers, store.KeyValue{Name: name, Value: scenario.Scalar(h.Value)})
		}

		var body *store.APIBody
		if pd := entry.Request.PostData; pd != nil {
			mt := strings.ToLower(pd.MimeType)
			switch {
			case strings.Contains(mt, "json"):
				body = &store.APIBody{Type: "json", Content: pd.Text}
			case strings.Contains(mt, "application/x-www-form-urlencoded"):
				var fields store.KeyValueList
				for _, p := range pd.Params {
					fields = append(fields, store.KeyValue{Name: p.Name, Value: scenario.Scalar(p.Value)})
				}
				if len(fields) == 0 && pd.Text != "" {
					if vals, err := url.ParseQuery(pd.Text); err == nil {
						for k, vs := range vals {
							for _, v := range vs {
								fields = append(fields, store.KeyValue{Name: k, Value: scenario.Scalar(v)})
							}
						}
					}
				}
				body = &store.APIBody{Type: "form", Fields: fields}
			case pd.Text != "":
				body = &store.APIBody{Type: "raw", Content: pd.Text, ContentType: pd.MimeType}
			}
		}

		var folderPath []string
		segs := strings.Split(strings.Trim(relPath, "/"), "/")
		if len(segs) > 1 && segs[0] != "" {
			folderPath = []string{segs[0]}
		}

		b.APIs = append(b.APIs, parsedAPI{
			FolderPath: folderPath,
			API: store.API{
				Name:  method + " " + relPath,
				Order: order,
				Request: store.APIRequest{
					Method:  method,
					URL:     origin + relPath,
					Query:   queryList,
					Headers: headers,
					Body:    body,
				},
				Auth: auth,
			},
		})
		order += 1024
	}

	if staticFiltered > 0 {
		report.Skipped = append(report.Skipped, fmt.Sprintf("已过滤 %d 条静态资源请求（JS/CSS/图片/字体）", staticFiltered))
	}
	if nonHTTPFiltered > 0 {
		report.Skipped = append(report.Skipped, fmt.Sprintf("已过滤 %d 条非 HTTP 请求", nonHTTPFiltered))
	}
	report.APIsFound = len(b.APIs)
	return b, nil
}
