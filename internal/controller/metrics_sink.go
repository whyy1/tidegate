package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"tidegate/internal/exporter"
	"tidegate/internal/metrics"
	"tidegate/internal/scenario"
	"tidegate/internal/store"
)

const (
	metricsSinkSettingKey = "metrics_sink"
	metricsSinkSecretScope = "metrics_sink"
	metricsSinkSecretName  = "token"
	metricsSinkQueueCap    = 128
	metricsSinkTimeout     = 5 * time.Second
)

// MetricsSinkConfig configures real-time outbound push to InfluxDB or Prometheus Pushgateway.
type MetricsSinkConfig struct {
	Enabled   bool              `json:"enabled"`
	Type      string            `json:"type"` // "influxdb" | "prometheus_pushgateway"
	URL       string            `json:"url"`
	Org       string            `json:"org,omitempty"`
	Bucket    string            `json:"bucket,omitempty"`
	Database  string            `json:"database,omitempty"`
	Username  string            `json:"username,omitempty"`
	Token     string            `json:"token,omitempty"`
	Job       string            `json:"job,omitempty"`
	ExtraTags map[string]string `json:"extra_tags,omitempty"`
}

type sinkBatch struct {
	runID        string
	projectID    string
	scenarioName string
	view         *metrics.SnapshotView
	totalReqs    int64
	totalErrors  int64
	promState    exporter.PrometheusState
}

type metricsSink struct {
	ctrl   *Controller
	cfg    atomic.Pointer[MetricsSinkConfig]
	ch     chan sinkBatch
	tr     *http.Transport
	client *http.Client
}

func safeSinkDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	d := &net.Dialer{Timeout: 4 * time.Second, KeepAlive: 30 * time.Second}
	if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil {
		if isBlockedSinkIP(ip) {
			return nil, fmt.Errorf("拒绝向云元数据或链路本地地址 (%s) 推送指标", ip.String())
		}
		return d.DialContext(ctx, network, addr)
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil || len(ips) == 0 {
		return nil, fmt.Errorf("解析指标外发目标 %q 失败：%v", host, err)
	}
	for _, entry := range ips {
		if isBlockedSinkIP(entry.IP) {
			return nil, fmt.Errorf("目标 %q 解析到受保护的链路本地/元数据地址 %s，已拦截", host, entry.IP.String())
		}
	}
	var lastErr error
	for _, entry := range ips {
		c, derr := d.DialContext(ctx, network, net.JoinHostPort(entry.IP.String(), port))
		if derr == nil {
			return c, nil
		}
		lastErr = derr
	}
	return nil, lastErr
}

func isBlockedSinkIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return true
	}
	u := addr.Unmap()
	return u.IsUnspecified() || u.IsLinkLocalUnicast() || u.IsLinkLocalMulticast()
}

func newMetricsSink(ctrl *Controller) *metricsSink {
	tr := &http.Transport{
		DialContext:         safeSinkDialContext,
		MaxIdleConns:        8,
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     30 * time.Second,
	}
	client := &http.Client{
		Timeout:   metricsSinkTimeout,
		Transport: tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("redirects exceeded")
			}
			if len(via) > 0 && via[0].URL != nil && req.URL != nil {
				if req.URL.Scheme != via[0].URL.Scheme || !strings.EqualFold(req.URL.Host, via[0].URL.Host) {
					return errors.New("拒绝跨源重定向推送指标")
				}
			}
			return nil
		},
	}
	return &metricsSink{
		ctrl:   ctrl,
		ch:     make(chan sinkBatch, metricsSinkQueueCap),
		tr:     tr,
		client: client,
	}
}

func (s *metricsSink) close() {
	if s != nil && s.tr != nil {
		s.tr.CloseIdleConnections()
	}
}

func (s *metricsSink) run() {
	defer s.ctrl.bgWG.Done()
	for {
		select {
		case <-s.ctrl.ctx.Done():
			return
		case batch := <-s.ch:
			cfg := s.cfg.Load()
			if cfg == nil || !cfg.Enabled {
				continue
			}
			ctx, cancel := context.WithTimeout(s.ctrl.ctx, metricsSinkTimeout)
			err := s.push(ctx, cfg, batch)
			cancel()
			if err != nil && !s.ctrl.closing.Load() {
				s.ctrl.log.Warn("推送实时指标到外部存储失败", "type", cfg.Type, "err", err)
			}
		}
	}
}

func (s *metricsSink) enqueue(batch sinkBatch) {
	cfg := s.cfg.Load()
	if cfg == nil || !cfg.Enabled {
		return
	}
	select {
	case s.ch <- batch:
	default:
		// Drop when outbound TSDB lags so load execution is never stalled.
	}
}

func (s *metricsSink) push(ctx context.Context, cfg *MetricsSinkConfig, batch sinkBatch) error {
	switch cfg.Type {
	case "influxdb":
		endpoint, err := influxWriteURL(cfg)
		if err != nil {
			return err
		}
		payload := formatInfluxLineProtocol(batch, cfg.ExtraTags)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(payload))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "text/plain; charset=utf-8")
		if cfg.Username != "" && cfg.Token != "" {
			req.SetBasicAuth(cfg.Username, cfg.Token)
		} else if cfg.Token != "" {
			req.Header.Set("Authorization", "Token "+cfg.Token)
		}
		return doSinkRequest(s.client, req)

	case "prometheus_pushgateway":
		endpoint, err := pushgatewayWriteURL(cfg)
		if err != nil {
			return err
		}
		var buf bytes.Buffer
		if err := exporter.WritePrometheus(&buf, batch.promState); err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &buf)
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		if cfg.Username != "" && cfg.Token != "" {
			req.SetBasicAuth(cfg.Username, cfg.Token)
		} else if cfg.Token != "" {
			req.Header.Set("Authorization", "Bearer "+cfg.Token)
		}
		return doSinkRequest(s.client, req)

	default:
		return fmt.Errorf("不支持的指标外发类型 %q", cfg.Type)
	}
}

func doSinkRequest(client *http.Client, req *http.Request) error {
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil
	}
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
}

func normalizeMetricsSinkConfig(in MetricsSinkConfig) (MetricsSinkConfig, error) {
	out := in
	out.Type = strings.ToLower(strings.TrimSpace(out.Type))
	out.URL = strings.TrimSpace(out.URL)
	out.Org = strings.TrimSpace(out.Org)
	out.Bucket = strings.TrimSpace(out.Bucket)
	out.Database = strings.TrimSpace(out.Database)
	out.Username = strings.TrimSpace(out.Username)
	out.Job = strings.TrimSpace(out.Job)

	for field, v := range map[string]string{
		"url":      out.URL,
		"org":      out.Org,
		"bucket":   out.Bucket,
		"database": out.Database,
		"username": out.Username,
		"token":    out.Token,
		"job":      out.Job,
	} {
		if strings.ContainsAny(v, "\r\n") {
			return out, userErr(ErrInvalidInput, fmt.Sprintf("metrics_sink.%s 不能包含换行", field), nil)
		}
	}

	if len(out.ExtraTags) > 0 {
		cleaned := make(map[string]string, len(out.ExtraTags))
		for k, v := range out.ExtraTags {
			tk := strings.TrimSpace(k)
			if tk == "" {
				continue
			}
			if strings.ContainsAny(tk+v, "\r\n") {
				return out, userErr(ErrInvalidInput, "extra_tags 不能包含换行", nil)
			}
			cleaned[tk] = strings.TrimSpace(v)
		}
		out.ExtraTags = cleaned
	}

	if out.Enabled || out.Type != "" || out.URL != "" {
		switch out.Type {
		case "influxdb", "prometheus_pushgateway":
		default:
			if out.Enabled || out.Type != "" {
				return out, userErr(ErrInvalidInput, "metrics_sink.type 只能是 influxdb 或 prometheus_pushgateway", nil)
			}
		}
		if out.URL != "" {
			u, err := url.Parse(out.URL)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				return out, userErr(ErrInvalidInput, "metrics_sink.url 必须是合法的 http:// 或 https:// 地址", nil)
			}
		} else if out.Enabled {
			return out, userErr(ErrInvalidInput, "启用指标外发时必须填写目标 url", nil)
		}
	}
	if out.Type == "prometheus_pushgateway" && out.Job == "" {
		out.Job = "tidegate"
	}
	return out, nil
}

func (c *Controller) sealSinkToken(ctx context.Context, plaintext string) error {
	if c.store != nil {
		if v := c.store.SecretVault(); v != nil {
			if plaintext == "" {
				return v.DeleteSystemSecret(ctx, metricsSinkSecretScope, metricsSinkSecretName)
			}
			return v.SealSystemSecret(ctx, metricsSinkSecretScope, metricsSinkSecretName, plaintext)
		}
	}
	if c.results == nil {
		return nil
	}
	if plaintext == "" {
		return c.results.DeleteProjectSecret(ctx, "__system__", metricsSinkSecretScope, metricsSinkSecretName)
	}
	return c.results.SetProjectSecret(ctx, "__system__", metricsSinkSecretScope, metricsSinkSecretName, []byte(plaintext))
}

func (c *Controller) openSinkToken(ctx context.Context) (string, bool, error) {
	if c.store != nil {
		if v := c.store.SecretVault(); v != nil {
			return v.OpenSystemSecret(ctx, metricsSinkSecretScope, metricsSinkSecretName)
		}
	}
	if c.results == nil {
		return "", false, nil
	}
	sec, err := c.results.ProjectSecrets(ctx, "__system__", metricsSinkSecretScope)
	if err != nil {
		return "", false, err
	}
	raw, ok := sec[metricsSinkSecretName]
	if !ok {
		return "", false, nil
	}
	return string(raw), true, nil
}

func (c *Controller) loadResolvedSinkConfig(ctx context.Context) (MetricsSinkConfig, bool, error) {
	var cfg MetricsSinkConfig
	if c == nil || c.results == nil {
		return cfg, false, nil
	}
	raw, ok, err := c.results.Setting(ctx, metricsSinkSettingKey)
	if err != nil {
		return cfg, false, err
	}
	if ok && strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			return cfg, false, fmt.Errorf("解析指标外发配置失败：%w", err)
		}
	}
	tok, hasTok, err := c.openSinkToken(ctx)
	if err != nil {
		return cfg, false, err
	}
	if hasTok {
		cfg.Token = tok
	}
	return cfg, hasTok, nil
}

func (c *Controller) reloadMetricsSink(ctx context.Context) {
	if c.sink == nil {
		return
	}
	cfg, _, err := c.loadResolvedSinkConfig(ctx)
	if err != nil {
		c.log.Warn("加载指标外发配置失败", "err", err)
		return
	}
	c.sink.cfg.Store(&cfg)
}

// GetMetricsSinkConfig loads MetricsSinkConfig with any stored token masked as SecretMask.
func (c *Controller) GetMetricsSinkConfig(ctx context.Context) (MetricsSinkConfig, error) {
	cfg, hasTok, err := c.loadResolvedSinkConfig(ctx)
	if err != nil {
		return cfg, err
	}
	if hasTok && cfg.Token != "" {
		cfg.Token = store.SecretMask
	} else {
		cfg.Token = ""
	}
	return cfg, nil
}

// SetMetricsSinkConfig validates, persists (encrypting Token), applies, and
// returns the masked MetricsSinkConfig.
func (c *Controller) SetMetricsSinkConfig(ctx context.Context, in MetricsSinkConfig) (MetricsSinkConfig, error) {
	cfg, err := normalizeMetricsSinkConfig(in)
	if err != nil {
		return MetricsSinkConfig{}, err
	}

	resolvedTok := cfg.Token
	if resolvedTok == store.SecretMask {
		oldTok, _, oerr := c.openSinkToken(ctx)
		if oerr != nil {
			return MetricsSinkConfig{}, oerr
		}
		resolvedTok = oldTok
	} else {
		if err := c.sealSinkToken(ctx, resolvedTok); err != nil {
			return MetricsSinkConfig{}, err
		}
	}

	diskCfg := cfg
	diskCfg.Token = ""
	b, err := json.Marshal(diskCfg)
	if err != nil {
		return MetricsSinkConfig{}, fmt.Errorf("序列化指标外发配置失败：%w", err)
	}
	if err := c.results.SetSetting(ctx, metricsSinkSettingKey, string(b)); err != nil {
		return MetricsSinkConfig{}, err
	}

	liveCfg := diskCfg
	liveCfg.Token = resolvedTok
	if c.sink != nil {
		c.sink.cfg.Store(&liveCfg)
	}

	masked := diskCfg
	if resolvedTok != "" {
		masked.Token = store.SecretMask
	}
	return masked, nil
}

// TestMetricsSink sends a test telemetry payload to verify endpoint reachability and auth.
func (c *Controller) TestMetricsSink(ctx context.Context, draft *MetricsSinkConfig) error {
	var cfg MetricsSinkConfig
	if draft != nil {
		norm, err := normalizeMetricsSinkConfig(*draft)
		if err != nil {
			return err
		}
		cfg = norm
		if cfg.Token == store.SecretMask {
			oldTok, _, err := c.openSinkToken(ctx)
			if err != nil {
				return err
			}
			cfg.Token = oldTok
		}
	} else {
		cur, _, err := c.loadResolvedSinkConfig(ctx)
		if err != nil {
			return err
		}
		cfg = cur
	}
	if cfg.URL == "" || cfg.Type == "" {
		return userErr(ErrInvalidInput, "请先填写目标类型和地址后再测试连接", nil)
	}
	if u, perr := url.Parse(cfg.URL); perr == nil && u.Hostname() != "" {
		if pol, serr := c.GetSecurityPolicy(ctx); serr == nil && pol.AllowlistEnabled {
			m, merr := scenario.NewTargetMatcher(pol.AllowedTargets)
			if merr == nil && m.Len() > 0 {
				if err := m.CheckDial(u.Hostname(), nil); err != nil {
					if !polMatchSinkSavedURL(ctx, c, cfg.URL) {
						return userErr(ErrTargetNotAllowed, fmt.Sprintf("指标外发地址 %q 不在安全策略白名单内，请先加入白名单或保持已保存配置", u.Hostname()), err)
					}
				}
			}
		}
	}

	now := time.Now()
	testView := &metrics.SnapshotView{
		Seq:  0,
		Time: now,
		StepView: metrics.StepView{
			Requests: 1,
		},
	}
	batch := sinkBatch{
		runID:        "connection-test",
		projectID:    "system",
		scenarioName: "connection-test",
		view:         testView,
		totalReqs:    1,
		totalErrors:  0,
		promState: exporter.PrometheusState{
			Run: exporter.PrometheusRun{
				Active:        false,
				RunID:         "connection-test",
				ScenarioName:  "connection-test",
				Snapshot:      testView,
				RequestsTotal: 1,
			},
		},
	}
	tctx, cancel := context.WithTimeout(ctx, metricsSinkTimeout)
	defer cancel()
	if err := c.sink.push(tctx, &cfg, batch); err != nil {
		return userErr(ErrInvalidInput, fmt.Sprintf("测试推送失败：%v", err), err)
	}
	return nil
}

// MetricsState builds the current Prometheus exposition snapshot for cluster agents and any active run.
func (c *Controller) MetricsState(ctx context.Context) exporter.PrometheusState {
	var st exporter.PrometheusState
	agents, err := c.Agents(ctx)
	if err == nil {
		st.Agents = make([]exporter.PrometheusAgent, 0, len(agents))
		for _, ag := range agents {
			pa := exporter.PrometheusAgent{
				ID:           ag.ID,
				Name:         ag.Name,
				RTTMs:        ag.RTTMs,
			}
			if ag.Stats != nil {
				pa.CPURatio = ag.Stats.CPU.Machine
				if ag.Stats.Memory.TotalBytes > 0 {
					pa.MemoryRatio = float64(ag.Stats.Memory.UsedBytes) / float64(ag.Stats.Memory.TotalBytes)
				}
				for _, nic := range ag.Stats.NICs {
					if !nic.Loopback {
						pa.UplinkBps += float64(nic.TxBytesPerSec) * 8
						pa.DownlinkBps += float64(nic.RxBytesPerSec) * 8
					}
				}
			}
			st.Agents = append(st.Agents, pa)
		}
	}

	c.mu.Lock()
	ar := c.active
	c.mu.Unlock()
	if ar != nil {
		st.Run = ar.prometheusRun()
	}
	return st
}

func influxWriteURL(cfg *MetricsSinkConfig) (string, error) {
	u, err := url.Parse(cfg.URL)
	if err != nil {
		return "", err
	}
	q := u.Query()
	if q.Get("precision") == "" {
		q.Set("precision", "ns")
	}
	if u.Path == "" || u.Path == "/" {
		if cfg.Bucket != "" {
			u.Path = "/api/v2/write"
			if cfg.Org != "" && q.Get("org") == "" {
				q.Set("org", cfg.Org)
			}
			if q.Get("bucket") == "" {
				q.Set("bucket", cfg.Bucket)
			}
		} else {
			u.Path = "/write"
			if cfg.Database != "" && q.Get("db") == "" {
				q.Set("db", cfg.Database)
			}
		}
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func pushgatewayWriteURL(cfg *MetricsSinkConfig) (string, error) {
	u, err := url.Parse(cfg.URL)
	if err != nil {
		return "", err
	}
	if !strings.Contains(u.Path, "/metrics/job/") {
		job := cfg.Job
		if job == "" {
			job = "tidegate"
		}
		u.Path = strings.TrimRight(u.Path, "/") + "/metrics/job/" + url.PathEscape(job)
	}
	return u.String(), nil
}

func formatInfluxLineProtocol(batch sinkBatch, extraTags map[string]string) string {
	sv := batch.view
	if sv == nil {
		return ""
	}
	ts := sv.Time.UnixNano()
	if ts <= 0 {
		ts = time.Now().UnixNano()
	}

	tagPairs := func(base ...string) string {
		tags := make(map[string]string, len(extraTags)+len(base)/2)
		for k, v := range extraTags {
			if strings.TrimSpace(k) != "" && strings.TrimSpace(v) != "" {
				tags[strings.TrimSpace(k)] = strings.TrimSpace(v)
			}
		}
		for i := 0; i+1 < len(base); i += 2 {
			if base[i+1] != "" {
				tags[base[i]] = base[i+1]
			}
		}
		keys := make([]string, 0, len(tags))
		for k := range tags {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var out strings.Builder
		for _, k := range keys {
			out.WriteByte(',')
			out.WriteString(influxEscTag(k))
			out.WriteByte('=')
			out.WriteString(influxEscTag(tags[k]))
		}
		return out.String()
	}

	var b strings.Builder
	runTags := tagPairs("run_id", batch.runID, "project_id", batch.projectID, "scenario", batch.scenarioName)
	fmt.Fprintf(&b, "tidegate_run%s vus=%di,conns=%di,target=%s,rps=%s,error_rate=%s,p50_ms=%s,p90_ms=%s,p95_ms=%s,p99_ms=%s,max_ms=%s,requests=%di,errors=%di,sent_bps=%s,recv_bps=%s %d\n",
		runTags,
		sv.ActiveVUs,
		sv.ActiveConns,
		influxFloat(sv.Target),
		influxFloat(sv.RPS),
		influxFloat(sv.ErrorRate),
		influxFloat(sv.Latency.P50),
		influxFloat(sv.Latency.P90),
		influxFloat(sv.Latency.P95),
		influxFloat(sv.Latency.P99),
		influxFloat(sv.Latency.Max),
		batch.totalReqs,
		batch.totalErrors,
		influxFloat(sv.SentBps),
		influxFloat(sv.ReceivedBps),
		ts,
	)

	for _, step := range sv.Steps {
		stepTags := tagPairs(
			"run_id", batch.runID,
			"project_id", batch.projectID,
			"scenario", batch.scenarioName,
			"step_id", step.ID,
			"step_name", step.Name,
			"is_group", strconv.FormatBool(step.IsGroup),
		)
		fmt.Fprintf(&b, "tidegate_step%s rps=%s,error_rate=%s,p95_ms=%s,requests=%di,errors=%di %d\n",
			stepTags,
			influxFloat(step.RPS),
			influxFloat(step.ErrorRate),
			influxFloat(step.Latency.P95),
			step.Requests,
			step.Errors,
			ts,
		)
	}
	return b.String()
}

func influxEscTag(s string) string {
	r := strings.NewReplacer(",", `\,`, "=", `\=`, " ", `\ `)
	return r.Replace(s)
}

func influxFloat(v float64) string {
	if v == 0 {
		return "0"
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}

// Keep errors import explicitly used if additional sentinel checks are added later.
var _ = errors.New

func polMatchSinkSavedURL(ctx context.Context, c *Controller, targetURL string) bool {
	saved, _, err := c.loadResolvedSinkConfig(ctx)
	return err == nil && saved.URL != "" && strings.EqualFold(saved.URL, targetURL)
}
