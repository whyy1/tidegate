package exporter

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"tidegate/internal/metrics"
	"tidegate/internal/store"
)

var jtlHeader = []string{
	"timeStamp",
	"elapsed",
	"label",
	"responseCode",
	"responseMessage",
	"threadName",
	"dataType",
	"success",
	"failureMessage",
	"bytes",
	"sentBytes",
	"grpThreads",
	"allThreads",
	"URL",
	"Latency",
	"IdleTime",
	"Connect",
}

type jtlRow struct {
	tsMS   int64
	record []string
}

// ExportRunJTL writes a standard 17-column JMeter CSV JTL log from the captured
// samples and per-second step snapshots of run into w.
func ExportRunJTL(run *store.Run, snaps []store.SnapshotRecord, samples []store.SampleRecord, w io.Writer) error {
	if w == nil {
		return fmt.Errorf("输出写入器不能为空")
	}
	cw := csv.NewWriter(w)
	if err := cw.Write(jtlHeader); err != nil {
		return err
	}

	threadPrefix := "Tidegate"
	if run != nil && strings.TrimSpace(run.ScenarioName) != "" {
		threadPrefix = strings.TrimSpace(run.ScenarioName)
	}

	var rows []jtlRow
	for _, rec := range samples {
		if len(rec.Data) == 0 {
			continue
		}
		var sv metrics.SampleView
		if err := json.Unmarshal(rec.Data, &sv); err != nil {
			continue
		}
		started := rec.Time
		if !sv.Time.IsZero() {
			started = sv.Time
		}
		tsMS := started.UnixMilli()
		elapsedMS := int64(sv.DurationMs + 0.5)
		label := firstString(strings.TrimSpace(sv.Step), "request")
		code := strconv.Itoa(sv.Status)
		msg := http.StatusText(sv.Status)
		if sv.Status == 0 {
			code = "Non HTTP response code"
			msg = sv.Error
		} else if msg == "" {
			msg = "HTTP " + code
		}
		success := sv.Error == "" && sv.Status > 0 && sv.Status < 400
		if sv.Error != "" {
			success = false
		}
		bytesRecv := len(sv.Body)
		bytesSent := len(sv.RequestBody)
		rows = append(rows, jtlRow{
			tsMS: tsMS,
			record: []string{
				strconv.FormatInt(tsMS, 10),
				strconv.FormatInt(elapsedMS, 10),
				label,
				code,
				msg,
				threadPrefix + " 1-1",
				"text",
				strconv.FormatBool(success),
				sv.Error,
				strconv.Itoa(bytesRecv),
				strconv.Itoa(bytesSent),
				"1",
				"1",
				redactJTLURL(sv.URL),
				strconv.FormatInt(elapsedMS, 10),
				"0",
				"0",
			},
		})
	}

	// Add representative per-second step rows from aggregated non-group metrics so
	// JMeter reports also reflect aggregate throughput and latency breakdowns.
	for _, rec := range snaps {
		if len(rec.Data) == 0 {
			continue
		}
		var sv metrics.SnapshotView
		if err := json.Unmarshal(rec.Data, &sv); err != nil {
			continue
		}
		ts := rec.Time
		if !sv.Time.IsZero() {
			ts = sv.Time
		}
		tsMS := ts.UnixMilli()
		threads := strconv.FormatInt(max(int64(1), sv.ActiveVUs), 10)
		for _, st := range sv.Steps {
			if st.IsGroup || st.Requests <= 0 {
				continue
			}
			label := firstString(strings.TrimSpace(st.Name), strings.TrimSpace(st.ID), "step")
			elapsedMS := int64(st.Latency.Mean + 0.5)
			ttfbMS := elapsedMS
			if fb, ok := st.Phases["first_byte"]; ok && fb.Count > 0 {
				ttfbMS = int64(fb.AvgMs + 0.5)
			}
			connMS := int64(0)
			if cn, ok := st.Phases["connect"]; ok && cn.Count > 0 {
				connMS = int64(cn.AvgMs + 0.5)
			}
			avgRecv := st.BytesReceived / st.Requests
			avgSent := st.BytesSent / st.Requests
			successReqs := st.Requests - st.Errors
			if successReqs > 0 {
				rows = append(rows, jtlRow{
					tsMS: tsMS,
					record: []string{
						strconv.FormatInt(tsMS, 10),
						strconv.FormatInt(elapsedMS, 10),
						label,
						"200",
						"OK",
						threadPrefix + " 1-1",
						"text",
						"true",
						"",
						strconv.FormatInt(avgRecv, 10),
						strconv.FormatInt(avgSent, 10),
						threads,
						threads,
						"",
						strconv.FormatInt(ttfbMS, 10),
						"0",
						strconv.FormatInt(connMS, 10),
					},
				})
			}
		}
	}

	sort.SliceStable(rows, func(i, j int) bool {
		return rows[i].tsMS < rows[j].tsMS
	})
	for _, r := range rows {
		if err := cw.Write(r.record); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

func redactJTLURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.RawQuery == "" {
		return raw
	}
	q := u.Query()
	changed := false
	for k := range q {
		lk := strings.ToLower(k)
		if strings.Contains(lk, "token") || strings.Contains(lk, "secret") ||
			strings.Contains(lk, "password") || strings.Contains(lk, "passwd") ||
			strings.Contains(lk, "key") || strings.Contains(lk, "auth") || strings.Contains(lk, "sig") {
			q.Set(k, "[REDACTED]")
			changed = true
		}
	}
	if changed {
		u.RawQuery = q.Encode()
		return u.String()
	}
	return raw
}

func firstString(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
