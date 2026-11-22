package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/netip"
	"strings"
	"time"

	"tidegate/internal/metrics"
	"tidegate/internal/scenario"
	"tidegate/internal/store"
)

const securityPolicySettingKey = "security_policy"

// ErrTargetNotAllowed indicates a request target fell outside the configured allowlist.
var ErrTargetNotAllowed = scenario.ErrTargetNotAllowed

// ErrScaleLimitExceeded indicates requested VUs or RPS exceeded the caller role limit.
var ErrScaleLimitExceeded = errors.New("压测规模超出当前角色上限")

// SecurityPolicy controls outbound target allowlisting and per-role scale limits.
type SecurityPolicy struct {
	AllowlistEnabled bool     `json:"allowlist_enabled"`
	AllowedTargets   []string `json:"allowed_targets,omitempty"`
	MemberMaxVUs     int      `json:"member_max_vus,omitempty"`
	MemberMaxRPS     float64  `json:"member_max_rps,omitempty"`
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

// ValidatePlanTargets verifies every URL, OAuth2 token URL, ProxyURL, and
// DNS override in plan conforms to policy when AllowlistEnabled is true.
func ValidatePlanTargets(ctx context.Context, plan *scenario.Plan, policy SecurityPolicy) error {
	if !policy.AllowlistEnabled || plan == nil {
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
				if st.URL.Static != nil && st.URL.Static.Origin != nil {
					if err := checkHost(st.URL.Static.Origin.HostName); err != nil {
						return err
					}
				} else if st.URL.Origin != nil {
					if err := checkHost(st.URL.Origin.HostName); err != nil {
						return err
					}
				} else if st.URL.Full != nil {
					if !st.URL.Full.Static() {
						return userErr(ErrTargetNotAllowed, fmt.Sprintf("步骤 %q 启用白名单时不支持动态域名 URL", st.ID), nil)
					}
					tg, err := scenario.ParseTarget(st.URL.Full.Literal())
					if err != nil {
						return userErr(ErrInvalidInput, err.Error(), err)
					}
					if err := checkHost(tg.Origin.HostName); err != nil {
						return err
					}
				}
				if st.OAuth2 != nil && st.OAuth2.TokenURL != nil {
					if !st.OAuth2.TokenURL.Static() {
						return userErr(ErrTargetNotAllowed, fmt.Sprintf("步骤 %q 启用白名单时不支持动态 OAuth2 TokenURL", st.ID), nil)
					}
					tg, err := scenario.ParseTarget(st.OAuth2.TokenURL.Literal())
					if err != nil {
						return userErr(ErrInvalidInput, err.Error(), err)
					}
					if err := checkHost(tg.Origin.HostName); err != nil {
						return err
					}
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
