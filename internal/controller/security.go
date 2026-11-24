package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"net"
	"net/netip"
	"strings"
	"time"

	"tidegate/internal/engine"
	"tidegate/internal/metrics"
	"tidegate/internal/scenario"
	"tidegate/internal/store"
)

const securityPolicySettingKey = "security_policy"

// Default MCP execution safety ceilings applied when SecurityPolicy leaves limits at 0.
const (
	DefaultMCPMaxVUs         = 200
	DefaultMCPMaxDurationSec = 300
	DefaultMCPRatePerMin     = 120
)

// ErrTargetNotAllowed indicates a request target fell outside the configured allowlist.
var ErrTargetNotAllowed = scenario.ErrTargetNotAllowed

// ErrScaleLimitExceeded indicates requested VUs or RPS exceeded the caller role limit.
var ErrScaleLimitExceeded = errors.New("压测规模超出当前角色上限")

// ErrScriptsDisabled indicates sandboxed script steps have been disabled by policy.
var ErrScriptsDisabled = errors.New("管理员已全局禁用脚本步骤")

// SecurityPolicy controls outbound target allowlisting, per-role scale limits,
// account/token guardrails, and MCP server execution constraints.
type SecurityPolicy struct {
	AllowlistEnabled    bool     `json:"allowlist_enabled"`
	AllowedTargets      []string `json:"allowed_targets,omitempty"`
	MemberMaxVUs        int      `json:"member_max_vus,omitempty"`
	MemberMaxRPS        float64  `json:"member_max_rps,omitempty"`
	ScriptsDisabled     bool     `json:"scripts_disabled,omitempty"`
	RequireTOTP         string   `json:"require_totp,omitempty"` // "", "off", "admin", "all"
	TokenMaxTTLDays     int      `json:"token_max_ttl_days,omitempty"`
	MCPDisabled         bool     `json:"mcp_disabled,omitempty"`
	MCPDisableLoadRuns  bool     `json:"mcp_disable_load_runs,omitempty"`
	MCPDisableResources bool     `json:"mcp_disable_resources,omitempty"`
	MCPDisablePrompts   bool     `json:"mcp_disable_prompts,omitempty"`
	MCPDisabledTools    []string `json:"mcp_disabled_tools,omitempty"`
	MCPMaxVUs           int      `json:"mcp_max_vus,omitempty"`
	MCPMaxDurationSec   int      `json:"mcp_max_duration_sec,omitempty"`
	MCPRatePerMin       int      `json:"mcp_rate_per_min,omitempty"`
}

// EffectiveMCPRatePerMin returns the configured MCP per-minute rate limit or DefaultMCPRatePerMin.
func (p SecurityPolicy) EffectiveMCPRatePerMin() int {
	if p.MCPRatePerMin > 0 {
		return p.MCPRatePerMin
	}
	return DefaultMCPRatePerMin
}

// IsMCPToolDisabled reports whether the named MCP tool is globally blocked.
func (p SecurityPolicy) IsMCPToolDisabled(name string) bool {
	if p.MCPDisabled {
		return true
	}
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" {
		return true
	}
	if p.MCPDisableLoadRuns && n == "manage_load_run" {
		return true
	}
	for _, d := range p.MCPDisabledTools {
		if strings.EqualFold(strings.TrimSpace(d), n) {
			return true
		}
	}
	return false
}

// EffectiveMCPMaxVUs returns the configured MCP VU cap or DefaultMCPMaxVUs.
func (p SecurityPolicy) EffectiveMCPMaxVUs() int {
	if p.MCPMaxVUs > 0 {
		return p.MCPMaxVUs
	}
	return DefaultMCPMaxVUs
}

// EffectiveMCPMaxDuration returns the configured MCP load duration cap or DefaultMCPMaxDurationSec.
func (p SecurityPolicy) EffectiveMCPMaxDuration() time.Duration {
	if p.MCPMaxDurationSec > 0 {
		return time.Duration(p.MCPMaxDurationSec) * time.Second
	}
	return time.Duration(DefaultMCPMaxDurationSec) * time.Second
}

// GetSecurityPolicy loads the active SecurityPolicy (zero values when unset).
func (c *Controller) GetSecurityPolicy(ctx context.Context) (SecurityPolicy, error) {
	var pol SecurityPolicy
	if c == nil || c.results == nil {
		return pol, nil
	}
	raw, ok, err := c.results.Setting(ctx, securityPolicySettingKey)
	if err != nil {
		return pol, err
	}
	if !ok || strings.TrimSpace(raw) == "" {
		return pol, nil
	}
	if err := json.Unmarshal([]byte(raw), &pol); err != nil {
		return pol, fmt.Errorf("解析安全策略失败：%w", err)
	}
	return pol, nil
}

// SetSecurityPolicy validates and persists SecurityPolicy.
func (c *Controller) SetSecurityPolicy(ctx context.Context, pol SecurityPolicy) (SecurityPolicy, error) {
	if pol.MemberMaxVUs < 0 {
		return pol, userErr(ErrInvalidInput, "member_max_vus 不能为负数", nil)
	}
	if math.IsNaN(pol.MemberMaxRPS) || math.IsInf(pol.MemberMaxRPS, 0) || pol.MemberMaxRPS < 0 {
		return pol, userErr(ErrInvalidInput, "member_max_rps 必须是非负有限数字", nil)
	}
	if pol.MCPMaxVUs < 0 {
		return pol, userErr(ErrInvalidInput, "mcp_max_vus 不能为负数", nil)
	}
	if pol.MCPMaxDurationSec < 0 {
		return pol, userErr(ErrInvalidInput, "mcp_max_duration_sec 不能为负数", nil)
	}
	if pol.MCPRatePerMin < 0 {
		return pol, userErr(ErrInvalidInput, "mcp_rate_per_min 不能为负数", nil)
	}
	if pol.TokenMaxTTLDays < 0 {
		return pol, userErr(ErrInvalidInput, "token_max_ttl_days 不能为负数", nil)
	}
	switch strings.ToLower(strings.TrimSpace(pol.RequireTOTP)) {
	case "", "off":
		pol.RequireTOTP = ""
	case "admin", "all":
		pol.RequireTOTP = strings.ToLower(strings.TrimSpace(pol.RequireTOTP))
	default:
		return pol, userErr(ErrInvalidInput, "require_totp 仅支持 off、admin 或 all", nil)
	}
	cleaned := make([]string, 0, len(pol.AllowedTargets))
	seen := make(map[string]bool, len(pol.AllowedTargets))
	for _, raw := range pol.AllowedTargets {
		p := strings.ToLower(strings.TrimSpace(raw))
		if p == "" || seen[p] {
			continue
		}
		if _, err := scenario.ParseTargetRule(p); err != nil {
			return pol, userErr(ErrInvalidInput, err.Error(), err)
		}
		seen[p] = true
		cleaned = append(cleaned, p)
	}
	pol.AllowedTargets = cleaned

	cleanedTools := make([]string, 0, len(pol.MCPDisabledTools))
	seenTools := make(map[string]bool, len(pol.MCPDisabledTools))
	for _, raw := range pol.MCPDisabledTools {
		t := strings.ToLower(strings.TrimSpace(raw))
		if t == "" || seenTools[t] {
			continue
		}
		seenTools[t] = true
		cleanedTools = append(cleanedTools, t)
	}
	pol.MCPDisabledTools = cleanedTools

	b, err := json.Marshal(pol)
	if err != nil {
		return pol, fmt.Errorf("序列化安全策略失败：%w", err)
	}
	if err := c.results.SetSetting(ctx, securityPolicySettingKey, string(b)); err != nil {
		return pol, err
	}
	return pol, nil
}

// CompileTargetMatcher builds an engine.TargetCheck callback enforcing patterns.
func CompileTargetMatcher(patterns []string) (func(host string, ip net.IP) error, error) {
	return scenario.CompileTargetMatcher(patterns)
}

func planHasScripts(nodes []scenario.Node) bool {
	for _, n := range nodes {
		switch st := n.(type) {
		case *scenario.ScriptStepPlan:
			return true
		case *scenario.HTTPStep:
			if st.PreScript != nil || st.PostScript != nil {
				return true
			}
		case *scenario.WSStep:
			if st.PreScript != nil || st.PostScript != nil {
				return true
			}
		case *scenario.GRPCStepPlan:
			if st.PreScript != nil || st.PostScript != nil {
				return true
			}
		case *scenario.SSEStepPlan:
			if st.PreScript != nil || st.PostScript != nil {
				return true
			}
		case *scenario.GroupNode:
			if planHasScripts(st.Children) {
				return true
			}
		case *scenario.LoopNode:
			if planHasScripts(st.Children) {
				return true
			}
		case *scenario.IfNode:
			if planHasScripts(st.Then) || planHasScripts(st.Else) {
				return true
			}
		case *scenario.WeightedNode:
			for _, br := range st.Branches {
				if planHasScripts(br.Steps) {
					return true
				}
			}
		}
	}
	return false
}

// ValidatePlanTargets verifies every URL, OAuth2 token URL, ProxyURL, DNS override,
// and protocol endpoint in plan conforms to policy, and rejects script steps when
// ScriptsDisabled is set.
func ValidatePlanTargets(ctx context.Context, plan *scenario.Plan, policy SecurityPolicy) error {
	if plan == nil {
		return nil
	}
	if policy.ScriptsDisabled && planHasScripts(plan.Steps) {
		return userErr(ErrScriptsDisabled, "当前安全策略已全局禁用脚本步骤", nil)
	}
	if !policy.AllowlistEnabled {
		return nil
	}
	m, err := scenario.NewTargetMatcher(policy.AllowedTargets)
	if err != nil {
		return userErr(ErrInvalidInput, err.Error(), err)
	}
	if m.Len() == 0 {
		return userErr(ErrTargetNotAllowed, "已开启压测目标白名单，但允许列表为空", nil)
	}

	checkHost := func(host string) error {
		h := strings.Trim(strings.TrimSpace(host), "[]")
		if h == "" {
			return userErr(ErrTargetNotAllowed, "目标主机不能为空", nil)
		}
		if addr, err := netip.ParseAddr(h); err == nil {
			if m.MatchIP(addr.AsSlice()) {
				return nil
			}
			return userErr(ErrTargetNotAllowed, fmt.Sprintf("目标 IP %q 不在允许白名单内", h), nil)
		}
		if m.MatchHost(h) {
			return nil
		}
		if m.HasIPRules() {
			rctx, cancel := context.WithTimeout(ctx, 2*time.Second)
			ips, lerr := net.DefaultResolver.LookupIPAddr(rctx, h)
			cancel()
			if lerr == nil && len(ips) > 0 {
				allOK := true
				for _, ip := range ips {
					if !m.MatchIP(ip.IP) {
						allOK = false
						break
					}
				}
				if allOK {
					return nil
				}
			}
		}
		return userErr(ErrTargetNotAllowed, fmt.Sprintf("目标域名 %q 不在允许白名单内", h), nil)
	}

	checkURLTemplate := func(stepID string, u scenario.URLTemplate, parseFn func(string) (*scenario.Target, error)) error {
		if u.Static != nil && u.Static.Origin != nil {
			return checkHost(u.Static.Origin.HostName)
		}
		if u.Origin != nil {
			return checkHost(u.Origin.HostName)
		}
		if u.Full != nil {
			if !u.Full.Static() {
				return userErr(ErrTargetNotAllowed, fmt.Sprintf("步骤 %q 启用白名单时不支持动态域名 URL", stepID), nil)
			}
			tg, err := parseFn(u.Full.Literal())
			if err != nil {
				return userErr(ErrInvalidInput, err.Error(), err)
			}
			return checkHost(tg.Origin.HostName)
		}
		return nil
	}

	checkOAuth2 := func(stepID string, oa *scenario.OAuth2Plan) error {
		if oa == nil || oa.TokenURL == nil {
			return nil
		}
		if !oa.TokenURL.Static() {
			return userErr(ErrTargetNotAllowed, fmt.Sprintf("步骤 %q 启用白名单时不支持动态 OAuth2 TokenURL", stepID), nil)
		}
		tg, err := scenario.ParseTarget(oa.TokenURL.Literal())
		if err != nil {
			return userErr(ErrInvalidInput, err.Error(), err)
		}
		return checkHost(tg.Origin.HostName)
	}

	if plan.HTTP.ProxyURL != nil && plan.HTTP.ProxyURL.Hostname() != "" {
		if err := checkHost(plan.HTTP.ProxyURL.Hostname()); err != nil {
			return err
		}
	}
	for host, ipStr := range plan.HTTP.DNSOverrides {
		if err := checkHost(host); err != nil {
			return err
		}
		if err := checkHost(ipStr); err != nil {
			return err
		}
	}

	var walk func(nodes []scenario.Node) error
	walk = func(nodes []scenario.Node) error {
		for _, n := range nodes {
			switch st := n.(type) {
			case *scenario.HTTPStep:
				if err := checkURLTemplate(st.ID, st.URL, scenario.ParseTarget); err != nil {
					return err
				}
				if err := checkOAuth2(st.ID, st.OAuth2); err != nil {
					return err
				}
			case *scenario.WSStep:
				if err := checkURLTemplate(st.ID, st.URL, scenario.ParseWSTarget); err != nil {
					return err
				}
			case *scenario.GRPCStepPlan:
				if st.Origin != nil {
					if err := checkHost(st.Origin.HostName); err != nil {
						return err
					}
				} else if st.Target != nil {
					if !st.Target.Static() {
						return userErr(ErrTargetNotAllowed, fmt.Sprintf("步骤 %q 启用白名单时不支持动态 gRPC 目标地址", st.ID), nil)
					}
					o, err := scenario.ParseGRPCTarget(st.Target.Literal(), nil)
					if err != nil {
						return userErr(ErrInvalidInput, err.Error(), err)
					}
					if err := checkHost(o.HostName); err != nil {
						return err
					}
				}
			case *scenario.SSEStepPlan:
				if err := checkURLTemplate(st.ID, st.URL, scenario.ParseTarget); err != nil {
					return err
				}
				if err := checkOAuth2(st.ID, st.OAuth2); err != nil {
					return err
				}
			case *scenario.GroupNode:
				if err := walk(st.Children); err != nil {
					return err
				}
			case *scenario.LoopNode:
				if err := walk(st.Children); err != nil {
					return err
				}
			case *scenario.IfNode:
				if err := walk(st.Then); err != nil {
					return err
				}
				if err := walk(st.Else); err != nil {
					return err
				}
			case *scenario.WeightedNode:
				for _, br := range st.Branches {
					if err := walk(br.Steps); err != nil {
						return err
					}
				}
			}
		}
		return nil
	}
	return walk(plan.Steps)
}

// ValidateRoleScale checks that non-admin callers stay within configured limits.
func ValidateRoleScale(plan *scenario.Plan, actorRole string, policy SecurityPolicy) error {
	if plan == nil {
		return nil
	}
	role := strings.ToLower(strings.TrimSpace(actorRole))
	if role == "" || role == "admin" {
		return nil
	}
	if role == "viewer" {
		return userErr(ErrScaleLimitExceeded, "只读角色无权发起压测", nil)
	}
	if policy.MemberMaxVUs > 0 {
		reqVUs := int(math.Ceil(plan.Load.PeakTarget))
		if plan.Load.Mode == scenario.LoadRate {
			reqVUs = plan.Load.MaxVUs
		}
		if reqVUs > policy.MemberMaxVUs {
			return userErr(ErrScaleLimitExceeded, fmt.Sprintf("并发虚拟用户数 %d 超出当前角色上限 %d", reqVUs, policy.MemberMaxVUs), nil)
		}
	}
	if policy.MemberMaxRPS > 0 && plan.Load.Mode == scenario.LoadRate {
		if plan.Load.PeakTarget > policy.MemberMaxRPS {
			return userErr(ErrScaleLimitExceeded, fmt.Sprintf("目标速率 %.0f RPS 超出当前角色上限 %.0f RPS", plan.Load.PeakTarget, policy.MemberMaxRPS), nil)
		}
	}
	return nil
}

// ValidateMCPRunScale enforces hard MCP safety ceilings (VU count and stage
// duration) on runs started by AI/MCP callers unless explicitly raised by admin.
func ValidateMCPRunScale(plan *scenario.Plan, policy SecurityPolicy) error {
	if plan == nil {
		return nil
	}
	maxVUs := policy.EffectiveMCPMaxVUs()
	reqVUs := int(math.Ceil(plan.Load.PeakTarget))
	if plan.Load.Mode == scenario.LoadRate {
		reqVUs = plan.Load.MaxVUs
	}
	if reqVUs > maxVUs {
		return userErr(ErrScaleLimitExceeded, fmt.Sprintf("MCP 触发的并发虚拟用户数 %d 超出安全上限 %d", reqVUs, maxVUs), nil)
	}
	maxDur := policy.EffectiveMCPMaxDuration()
	if plan.Load.TotalDuration > maxDur {
		return userErr(ErrScaleLimitExceeded, fmt.Sprintf("MCP 触发的压测总时长 %s 超出安全上限 %s", plan.Load.TotalDuration.Round(time.Second), maxDur), nil)
	}
	return nil
}

func collectSecretValues(def *scenario.Definition, secretNames map[string]bool) []string {
	if def == nil || len(secretNames) == 0 || len(def.Variables) == 0 {
		return nil
	}
	seen := make(map[string]bool)
	var vals []string
	for k := range secretNames {
		v := strings.TrimSpace(string(def.Variables[k]))
		if len(v) < 3 || v == store.SecretMask || seen[v] {
			continue
		}
		seen[v] = true
		vals = append(vals, v)
	}
	return vals
}

func maskSecretText(s string, secretVals []string) string {
	if s == "" || len(secretVals) == 0 {
		return s
	}
	for _, sv := range secretVals {
		if strings.Contains(s, sv) {
			s = strings.ReplaceAll(s, sv, store.SecretMask)
		}
	}
	return s
}

func scrubDebugStepSecrets(step engine.DebugStepResult, secretNames map[string]bool, secretVals []string) engine.DebugStepResult {
	cp := step
	cp.ActualRequest.URL = maskSecretText(cp.ActualRequest.URL, secretVals)
	cp.ActualRequest.Body = maskSecretText(cp.ActualRequest.Body, secretVals)
	if len(cp.ActualRequest.Headers) > 0 {
		hdrs := make([]metrics.Header, len(cp.ActualRequest.Headers))
		for i, h := range cp.ActualRequest.Headers {
			val := maskSecretText(h.Value, secretVals)
			if metrics.RedactHeader(h.Name, h.Value) != h.Value {
				val = store.SecretMask
			}
			hdrs[i] = metrics.Header{Name: h.Name, Value: val}
		}
		cp.ActualRequest.Headers = hdrs
	}
	if len(cp.Redirects) > 0 {
		redirs := make([]engine.DebugHop, len(cp.Redirects))
		for i, rd := range cp.Redirects {
			rd.URL = maskSecretText(rd.URL, secretVals)
			rd.Location = maskSecretText(rd.Location, secretVals)
			redirs[i] = rd
		}
		cp.Redirects = redirs
	}
	if len(cp.ResponseHeaders) > 0 {
		rh := make([]metrics.Header, len(cp.ResponseHeaders))
		for i, h := range cp.ResponseHeaders {
			val := maskSecretText(h.Value, secretVals)
			if metrics.RedactHeader(h.Name, h.Value) != h.Value {
				val = store.SecretMask
			}
			rh[i] = metrics.Header{Name: h.Name, Value: val}
		}
		cp.ResponseHeaders = rh
	}
	cp.Body = maskSecretText(cp.Body, secretVals)
	if len(cp.Extracts) > 0 {
		exs := make([]engine.DebugExtractResult, len(cp.Extracts))
		for i, ex := range cp.Extracts {
			if secretNames[ex.Name] && ex.Value != "" {
				ex.Value = store.SecretMask
			} else {
				ex.Value = maskSecretText(ex.Value, secretVals)
			}
			exs[i] = ex
		}
		cp.Extracts = exs
	}
	if len(cp.ExtractedVars) > 0 {
		ev := maps.Clone(cp.ExtractedVars)
		for k, v := range ev {
			if secretNames[k] {
				ev[k] = store.SecretMask
			} else {
				ev[k] = maskSecretText(v, secretVals)
			}
		}
		cp.ExtractedVars = ev
	}
	return cp
}

func scrubDebugVars(vars map[string]string, secretNames map[string]bool, secretVals []string) map[string]string {
	if len(vars) == 0 {
		return nil
	}
	out := maps.Clone(vars)
	for k, v := range out {
		if secretNames[k] {
			out[k] = store.SecretMask
		} else {
			out[k] = maskSecretText(v, secretVals)
		}
	}
	return out
}

func scrubDebugRunSecrets(res *engine.DebugRunResult, secretNames map[string]bool, secretVals []string) *engine.DebugRunResult {
	if res == nil {
		return nil
	}
	cp := *res
	if len(res.Steps) > 0 {
		cp.Steps = make([]engine.DebugStepResult, len(res.Steps))
		for i, st := range res.Steps {
			cp.Steps[i] = scrubDebugStepSecrets(st, secretNames, secretVals)
		}
	}
	cp.FinalVars = scrubDebugVars(res.FinalVars, secretNames, secretVals)
	return &cp
}

// scrubDefinitionSecrets clones def and redacts secret variables, credential
// fields, and sensitive literal headers before persisting run metadata in SQLite.
func scrubDefinitionSecrets(def *scenario.Definition, secretNames map[string]bool) *scenario.Definition {
	cp := cloneDefinition(def)
	if cp == nil {
		return nil
	}
	cp.HTTP.AllowedTargets = nil
	for k := range secretNames {
		if _, ok := cp.Variables[k]; ok {
			cp.Variables[k] = scenario.Scalar(store.SecretMask)
		}
	}
	scrubStepsSecrets(cp.Steps)
	return cp
}

func scrubHeaderMap(hdrs map[string]string) {
	for k, v := range hdrs {
		if !strings.Contains(v, "{{") && metrics.RedactHeader(k, v) != v {
			hdrs[k] = store.SecretMask
		}
	}
}

func scrubHeaderList(list []scenario.NameValue) {
	for i := range list {
		v := string(list[i].Value)
		if !strings.Contains(v, "{{") && metrics.RedactHeader(list[i].Name, v) != v {
			list[i].Value = store.SecretMask
		}
	}
}

func scrubAuthFields(ba *scenario.BasicAuth, oa *scenario.OAuth2Auth) {
	if ba != nil && ba.Password != "" && !strings.Contains(ba.Password, "{{") {
		ba.Password = store.SecretMask
	}
	if oa != nil && oa.ClientSecret != "" && !strings.Contains(oa.ClientSecret, "{{") {
		oa.ClientSecret = store.SecretMask
	}
}

func scrubStepsSecrets(steps []scenario.Step) {
	for i := range steps {
		st := &steps[i]
		if st.Request != nil {
			scrubHeaderMap(st.Request.Headers)
			scrubHeaderList(st.Request.HeaderList)
			scrubAuthFields(st.Request.BasicAuth, st.Request.OAuth2)
		}
		if st.WebSocket != nil {
			scrubHeaderMap(st.WebSocket.Headers)
			scrubHeaderList(st.WebSocket.HeaderList)
		}
		if st.GRPC != nil {
			scrubHeaderMap(st.GRPC.Metadata)
			scrubHeaderList(st.GRPC.MetadataList)
		}
		if st.SSE != nil {
			scrubHeaderMap(st.SSE.Headers)
			scrubHeaderList(st.SSE.HeaderList)
			scrubAuthFields(st.SSE.BasicAuth, st.SSE.OAuth2)
		}
		if st.Group != nil {
			scrubStepsSecrets(st.Group.Children)
		}
		if st.Loop != nil {
			scrubStepsSecrets(st.Loop.Children)
		}
		if st.If != nil {
			scrubStepsSecrets(st.If.Then)
			scrubStepsSecrets(st.If.Else)
		}
		if st.Weighted != nil {
			for bIdx := range st.Weighted.Branches {
				scrubStepsSecrets(st.Weighted.Branches[bIdx].Steps)
			}
		}
	}
}
