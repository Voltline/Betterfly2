package utils

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// DefaultTrustedProxyCIDRs 是回环与内网段，覆盖k8s ingress和compose网络。
const DefaultTrustedProxyCIDRs = "127.0.0.0/8,::1/128,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,fc00::/7"

// ParseTrustedProxies 解析逗号分隔的CIDR，忽略无效项。
func ParseTrustedProxies(raw string) []netip.Prefix {
	var prefixes []netip.Prefix
	for _, item := range strings.Split(raw, ",") {
		if prefix, err := netip.ParsePrefix(strings.TrimSpace(item)); err == nil {
			prefixes = append(prefixes, prefix.Masked())
		}
	}
	return prefixes
}

func isTrustedProxy(addr netip.Addr, trusted []netip.Prefix) bool {
	for _, prefix := range trusted {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// ClientIP returns the peer address, or the forwarded client address when the
// peer is a trusted proxy (the ingress). Forwarded headers from anyone else are ignored.
// A trusted peer without usable headers (e.g. a Docker port-forward gateway) yields "",
// so per-IP limits are skipped instead of lumping every client into one bucket.
func ClientIP(r *http.Request, trusted []netip.Prefix) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	peer = peer.Unmap()
	if !isTrustedProxy(peer, trusted) {
		return peer.String()
	}
	// Walk X-Forwarded-For from the right: the first untrusted hop is the one our proxies saw.
	hops := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		addr, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			break
		}
		addr = addr.Unmap()
		if !isTrustedProxy(addr, trusted) {
			return addr.String()
		}
	}
	if addr, err := netip.ParseAddr(strings.TrimSpace(r.Header.Get("X-Real-IP"))); err == nil {
		return addr.Unmap().String()
	}
	return ""
}
