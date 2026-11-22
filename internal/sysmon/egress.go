package sysmon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Egress is the network interface the OS uses to reach a target.
type Egress struct {
	// Target is the address as given.
	Target string `json:"target"`
	// IP is the resolved address used for the route lookup.
	IP string `json:"ip"`
	// LocalIP is the source address the OS would use, when known.
	LocalIP string `json:"local_ip,omitempty"`
	// Interface and Index identify the interface; they match NICStats.
	Interface string `json:"interface"`
	Index     int    `json:"index"`
	// Loopback is true when the target is this machine (a loopback address,
	// an unspecified address or one of the machine's own addresses). Such
	// traffic never leaves the host and has no bandwidth limit.
	Loopback bool `json:"loopback"`
	// LinkSpeedBitsPerSec is the interface's negotiated speed, 0 when
	// unknown or for loopback.
	LinkSpeedBitsPerSec uint64 `json:"link_speed_bits_per_sec"`
}

// EgressInterface returns the interface the OS routes traffic to target
// through. target may be "host", "host:port", "ip", "ip:port", "[ipv6]:port"
// or a URL such as "https://example.com/path". Host names are resolved with
// a five second timeout; the first resolved address is used, as a dialer
// would. No packets are sent.
func EgressInterface(target string) (Egress, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return EgressInterfaceContext(ctx, target)
}

// EgressInterfaceContext is EgressInterface with a caller-supplied context
// for name resolution.
func EgressInterfaceContext(ctx context.Context, target string) (Egress, error) {
	e := Egress{Target: target}
	host, port, err := splitTarget(target)
	if err != nil {
		return e, err
	}
	ip, err := resolveHost(ctx, host)
	if err != nil {
		return e, err
	}
	e.IP = ip.String()

	if ip.IsLoopback() || ip.IsUnspecified() || isLocalAddr(ip) {
		e.Loopback = true
		e.LocalIP = e.IP
		if ip.IsUnspecified() {
			e.LocalIP = ""
		}
		if lo, ok := loopbackInterface(); ok {
			e.Interface, e.Index = lo.Name, lo.Index
		}
		return e, nil
	}

	local, err := sourceAddr(ip, port)
	if err == nil {
		e.LocalIP = local.String()
	}
	idx, name, speed, loop, rerr := routeInterface(ip, local)
	if rerr != nil {
		if err != nil {
			return e, fmt.Errorf("sysmon: no route to %s: %w", e.IP, err)
		}
		return e, rerr
	}
	e.Index, e.Interface, e.LinkSpeedBitsPerSec, e.Loopback = idx, name, speed, loop
	if loop {
		e.LinkSpeedBitsPerSec = 0
	}
	return e, nil
}

// splitTarget extracts host and port from the accepted target forms. The
// port defaults to 80 (443 for https and wss URLs).
func splitTarget(target string) (host string, port int, err error) {
	t := strings.TrimSpace(target)
	if t == "" {
		return "", 0, errors.New("sysmon: empty target")
	}
	port = 80
	if strings.Contains(t, "://") {
		u, err := url.Parse(t)
		if err != nil {
			return "", 0, fmt.Errorf("sysmon: invalid target %q: %w", target, err)
		}
		switch strings.ToLower(u.Scheme) {
		case "https", "wss":
			port = 443
		}
		host = u.Hostname()
		if p := u.Port(); p != "" {
			if port, err = parsePort(p); err != nil {
				return "", 0, err
			}
		}
	} else if h, p, err := net.SplitHostPort(t); err == nil {
		host = h
		if port, err = parsePort(p); err != nil {
			return "", 0, err
		}
	} else {
		// No port: a host name, an IPv4 address or a bare or bracketed
		// IPv6 address.
		host = strings.TrimSuffix(strings.TrimPrefix(t, "["), "]")
	}
	if host == "" {
		return "", 0, fmt.Errorf("sysmon: no host in target %q", target)
	}
	return host, port, nil
}

func parsePort(p string) (int, error) {
	n, err := strconv.Atoi(p)
	if err != nil || n < 1 || n > 65535 {
		return 0, fmt.Errorf("sysmon: invalid port %q", p)
	}
	return n, nil
}

func resolveHost(ctx context.Context, host string) (netip.Addr, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		return ip.Unmap(), nil
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("sysmon: resolve %s: %w", host, err)
	}
	if len(ips) == 0 {
		return netip.Addr{}, fmt.Errorf("sysmon: resolve %s: no addresses", host)
	}
	return ips[0].Unmap(), nil
}

// isLocalAddr reports whether ip is assigned to one of this machine's
// interfaces. Traffic to such an address is delivered internally.
func isLocalAddr(ip netip.Addr) bool {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	ip = ip.WithZone("")
	for _, a := range addrs {
		var raw net.IP
		switch v := a.(type) {
		case *net.IPNet:
			raw = v.IP
		case *net.IPAddr:
			raw = v.IP
		}
		if x, ok := netip.AddrFromSlice(raw); ok && x.Unmap() == ip {
			return true
		}
	}
	return false
}

func loopbackInterface() (net.Interface, bool) {
	ifs, err := net.Interfaces()
	if err != nil {
		return net.Interface{}, false
	}
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagLoopback != 0 && ifc.Flags&net.FlagUp != 0 {
			return ifc, true
		}
	}
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagLoopback != 0 {
			return ifc, true
		}
	}
	return net.Interface{}, false
}

// sourceAddr asks the OS which local address it would use to reach ip by
// connecting a UDP socket. Connecting a UDP socket sends nothing.
func sourceAddr(ip netip.Addr, port int) (netip.Addr, error) {
	c, err := net.DialUDP("udp", nil, net.UDPAddrFromAddrPort(netip.AddrPortFrom(ip, uint16(port))))
	if err != nil {
		return netip.Addr{}, err
	}
	defer c.Close()
	la, ok := c.LocalAddr().(*net.UDPAddr)
	if !ok {
		return netip.Addr{}, errors.New("sysmon: unexpected local address type")
	}
	a := la.AddrPort().Addr().Unmap()
	if !a.IsValid() || a.IsUnspecified() {
		return netip.Addr{}, errors.New("sysmon: no source address")
	}
	return a, nil
}

// interfaceByAddr finds the interface that owns local. A link-local
// address's zone names the interface directly.
func interfaceByAddr(local netip.Addr) (net.Interface, error) {
	if z := local.Zone(); z != "" {
		if ifc, err := net.InterfaceByName(z); err == nil {
			return *ifc, nil
		}
	}
	local = local.WithZone("")
	ifs, err := net.Interfaces()
	if err != nil {
		return net.Interface{}, err
	}
	for _, ifc := range ifs {
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok {
				if x, ok := netip.AddrFromSlice(n.IP); ok && x.Unmap() == local {
					return ifc, nil
				}
			}
		}
	}
	return net.Interface{}, fmt.Errorf("sysmon: no interface owns %s", local)
}

// routeBySource is the portable route lookup: the interface that owns the
// source address the OS picked.
func routeBySource(local netip.Addr) (idx int, name string, loopback bool, err error) {
	if !local.IsValid() {
		return 0, "", false, errors.New("sysmon: no source address")
	}
	ifc, err := interfaceByAddr(local)
	if err != nil {
		return 0, "", false, err
	}
	return ifc.Index, ifc.Name, ifc.Flags&net.FlagLoopback != 0, nil
}
