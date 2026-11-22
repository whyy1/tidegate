package exporter

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"tidegate/internal/metrics"
	"tidegate/internal/store"
)

// ExportSamplesHAR exports captured error and slow samples of a run as a HAR
// 1.2 archive with sensitive authentication headers redacted.
func ExportSamplesHAR(run *store.Run, samples []store.SampleRecord) ([]byte, error) {
	entries := make([]any, 0, len(samples))
	for _, rec := range samples {
		if len(rec.Data) == 0 {
			continue
		}
		var sv metrics.SampleView
		if err := json.Unmarshal(rec.Data, &sv); err != nil {
			continue
		}

		reqHeaders, reqCT := convertHARHeaders(sv.RequestHeaders)
		respHeaders, respCT := convertHARHeaders(sv.ResponseHeaders)
		queryString := extractHARQuery(sv.URL)

		reqObj := map[string]any{
			"method":      sv.Method,
			"url":         sv.URL,
			"httpVersion": "HTTP/1.1",
			"headers":     reqHeaders,
			"queryString": queryString,
			"headersSize": -1,
			"bodySize":    len(sv.RequestBody),
		}
		if sv.RequestBody != "" {
			mt := reqCT
			if mt == "" {
				mt = "text/plain"
			}
			reqObj["postData"] = map[string]any{
				"mimeType": mt,
				"text":     sv.RequestBody,
			}
		}

		statusText := http.StatusText(sv.Status)
		if sv.Status == 0 && sv.Error != "" {
			statusText = sv.Error
		}
		mt := respCT
		if mt == "" {
			mt = "text/plain"
		}
		respObj := map[string]any{
			"status":      sv.Status,
			"statusText":  statusText,
			"httpVersion": "HTTP/1.1",
			"headers":     respHeaders,
			"content": map[string]any{
				"size":     len(sv.Body),
				"mimeType": mt,
				"text":     sv.Body,
			},
			"headersSize": -1,
			"bodySize":    len(sv.Body),
		}

		started := rec.Time
		if !sv.Time.IsZero() {
			started = sv.Time
		}
		entry := map[string]any{
			"startedDateTime": started.UTC().Format(time.RFC3339Nano),
			"time":            sv.DurationMs,
			"request":         reqObj,
			"response":        respObj,
			"timings": map[string]any{
				"send":    0,
				"wait":    sv.DurationMs,
				"receive": 0,
			},
		}
		if sv.Step != "" {
			entry["comment"] = sv.Step
		}
		entries = append(entries, entry)
	}

	pageTitle := "Tidegate Samples"
	if run != nil {
		if run.ScenarioName != "" {
			pageTitle = run.ScenarioName + " (" + run.ID + ")"
		} else if run.ID != "" {
			pageTitle = run.ID
		}
	}

	doc := map[string]any{
		"log": map[string]any{
			"version": "1.2",
			"creator": map[string]any{
				"name":    "Tidegate",
				"version": "1.0",
			},
			"entries": entries,
			"comment": pageTitle,
		},
	}
	return json.MarshalIndent(doc, "", "  ")
}

func convertHARHeaders(hdrs []metrics.Header) ([]map[string]string, string) {
	out := make([]map[string]string, 0, len(hdrs))
	var contentType string
	for _, h := range hdrs {
		val := h.Value
		if isSensitiveHeaderName(h.Name) && val != "" {
			val = "[REDACTED]"
		}
		if strings.EqualFold(h.Name, "Content-Type") && contentType == "" {
			contentType = h.Value
		}
		out = append(out, map[string]string{
			"name":  h.Name,
			"value": val,
		})
	}
	return out, contentType
}

func extractHARQuery(rawURL string) []map[string]string {
	u, err := url.Parse(rawURL)
	if err != nil || u.RawQuery == "" {
		return []map[string]string{}
	}
	q := u.Query()
	out := make([]map[string]string, 0, len(q))
	for k, vals := range q {
		for _, v := range vals {
			out = append(out, map[string]string{
				"name":  k,
				"value": v,
			})
		}
	}
	return out
}
