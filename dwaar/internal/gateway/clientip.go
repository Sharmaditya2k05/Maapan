package gateway

import (
	"net"
	"net/http"
	"strings"
)

// ClientIP returns the caller's address, trusting only the X-Forwarded-For
// entries appended by our own proxies.
//
// Every proxy in front of dwaar appends the address it received the request
// from, so with trustedProxies hops in front the client is the
// trustedProxies-th entry from the right. Entries further left were written by
// the client and can be anything. trustedProxies = 0 ignores the header and
// uses the TCP peer. When the header has fewer entries than expected (a hop did
// not append), the leftmost entry is used: every entry present then came from
// a trusted hop.
//
// A private or loopback address at that position is one of the platform's own
// internal hops (Render puts a 10.x proxy behind its edge), never a public
// client, so ClientIP keeps moving left past such entries to the first public
// one. Everything it passes was appended by infrastructure, and a public entry
// it stops on was too, so a spoofed first hop still cannot be picked.
//
// Typical values: 1 behind Cloud Run's or Render's front end, 2 behind a
// Google external HTTPS load balancer in front of Cloud Run (it appends
// "<client>, <lb>").
//
// mool/httpx.ClientIP (used by the services) still trusts the first hop; dwaar
// overwrites X-Forwarded-For with this value before proxying, so the services
// see the real client as the first hop.
func ClientIP(r *http.Request, trustedProxies int) string {
	if trustedProxies > 0 {
		var hops []string
		for _, v := range r.Header.Values("X-Forwarded-For") {
			for _, h := range strings.Split(v, ",") {
				if h = strings.TrimSpace(h); h != "" {
					hops = append(hops, h)
				}
			}
		}
		if len(hops) > 0 {
			i := len(hops) - trustedProxies
			if i < 0 {
				i = 0
			}
			for i > 0 && internalIP(hops[i]) {
				i--
			}
			if ip := cleanIP(hops[i]); ip != "" {
				return ip
			}
		}
	}
	return peerIP(r.RemoteAddr)
}

// cleanIP accepts "1.2.3.4", "1.2.3.4:5678", "[::1]:80" and "::1"; anything
// else is rejected so a garbage header cannot become a rate-limit key.
func cleanIP(s string) string {
	if ip := net.ParseIP(s); ip != nil {
		return ip.String()
	}
	if host, _, err := net.SplitHostPort(s); err == nil {
		if ip := net.ParseIP(host); ip != nil {
			return ip.String()
		}
	}
	return ""
}

// internalIP reports whether s is a private, loopback or link-local address.
func internalIP(s string) bool {
	ip := net.ParseIP(cleanIP(s))
	return ip != nil && (ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast())
}

func peerIP(remote string) string {
	if ip := cleanIP(remote); ip != "" {
		return ip
	}
	return strings.Trim(remote, "[]")
}
