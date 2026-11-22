package scenario

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
)

// ErrTargetNotAllowed indicates a request target fell outside the configured allowlist.
var ErrTargetNotAllowed = errors.New("目标地址不在允许压测的白名单内")

// TargetRule is one parsed allowlist entry (domain, *.wildcard, IP, or CIDR).
type TargetRule struct {
	Prefix   netip.Prefix
	IsCIDR   bool
	Wildcard string // leading dot stripped, matches "*.domain"
	Host     string
}

// ParseTargetRule parses a domain, *.wildcard, IPv4/IPv6 address, or CIDR rule.
func ParseTargetRule(raw string) (TargetRule, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		return TargetRule{}, errors.New("白名单规则不能为空")
	}
	if strings.ContainsAny(s, "/\\") {
		if pfx, err := netip.ParsePrefix(s); err == nil {
			return TargetRule{Prefix: pfx, IsCIDR: true}, nil
		}
		return TargetRule{}, fmt.Errorf("无效的 CIDR 网段 %q", raw)
	}
	if addr, err := netip.ParseAddr(strings.Trim(s, "[]")); err == nil {
		u := addr.Unmap()
		bits := 32
		if u.Is6() {
			bits = 128
		}
		return TargetRule{Prefix: netip.PrefixFrom(u, bits), IsCIDR: true}, nil
	}
	if s == "*" || s == "*." {
		return TargetRule{}, fmt.Errorf("不允许使用宽泛通配符 %q", raw)
	}
	if strings.HasPrefix(s, "*.") {
		dom := strings.TrimPrefix(s, "*.")
		if dom == "" || strings.Contains(dom, "*") || strings.Contains(dom, ":") {
			return TargetRule{}, fmt.Errorf("无效的域名通配符规则 %q", raw)
		}
		return TargetRule{Wildcard: "." + dom}, nil
	}
	if strings.Contains(s, "*") {
		return TargetRule{}, fmt.Errorf("通配符规则必须形如 *.example.com：%q", raw)
	}
	return TargetRule{Host: strings.TrimSuffix(s, ".")}, nil
}

// TargetMatcher evaluates hostnames and resolved IPs against compiled rules.
type TargetMatcher struct {
	rules      []TargetRule
	hasIPRules bool
}

// NewTargetMatcher compiles patterns into a TargetMatcher.
func NewTargetMatcher(patterns []string) (*TargetMatcher, error) {
	m := &TargetMatcher{rules: make([]TargetRule, 0, len(patterns))}
	for _, raw := range patterns {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		r, err := ParseTargetRule(raw)
		if err != nil {
			return nil, err
		}
		if r.IsCIDR {
			m.hasIPRules = true
		}
		m.rules = append(m.rules, r)
	}
	return m, nil
}

// Len reports how many rules are in the matcher.
func (m *TargetMatcher) Len() int {
	if m == nil {
		return 0
	}
	return len(m.rules)
}

// HasIPRules reports whether any rule matches IP addresses or CIDRs.
func (m *TargetMatcher) HasIPRules() bool {
	return m != nil && m.hasIPRules
}

// MatchHost reports whether host matches a literal domain or wildcard rule.
func (m *TargetMatcher) MatchHost(host string) bool {
	if m == nil {
		return false
	}
	h := strings.TrimSuffix(strings.ToLower(strings.Trim(strings.TrimSpace(host), "[]")), ".")
	if h == "" {
		return false
	}
	for _, r := range m.rules {
		if r.IsCIDR {
			continue
		}
		if r.Host != "" && h == r.Host {
			return true
		}
		if r.Wildcard != "" && strings.HasSuffix(h, r.Wildcard) && len(h) > len(r.Wildcard) {
			return true
		}
	}
	return false
}

// MatchIP reports whether ip falls inside any IP or CIDR rule.
func (m *TargetMatcher) MatchIP(ip net.IP) bool {
	if m == nil || len(ip) == 0 {
		return false
	}
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	addr = addr.Unmap()
	for _, r := range m.rules {
		if r.IsCIDR && r.Prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// CheckDial validates a host and its resolved IP prior to establishing a TCP socket.
func (m *TargetMatcher) CheckDial(host string, ip net.IP) error {
	if m == nil || len(m.rules) == 0 {
		return fmt.Errorf("%w：已开启目标白名单但未配置任何允许条目", ErrTargetNotAllowed)
	}
	if ip != nil && m.MatchIP(ip) {
		return nil
	}
	if _, err := netip.ParseAddr(strings.Trim(strings.TrimSpace(host), "[]")); err == nil {
		return fmt.Errorf("%w：%s", ErrTargetNotAllowed, host)
	}
	if m.MatchHost(host) {
		return nil
	}
	return fmt.Errorf("%w：%s", ErrTargetNotAllowed, host)
}

// CompileTargetMatcher builds a dial-time TargetCheck callback enforcing patterns.
func CompileTargetMatcher(patterns []string) (func(host string, ip net.IP) error, error) {
	m, err := NewTargetMatcher(patterns)
	if err != nil {
		return nil, err
	}
	return m.CheckDial, nil
}
