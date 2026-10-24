package httpx

import (
	"net"
	"net/http"
	"strings"
)

// RemoteIP is the host part of r.RemoteAddr, without the port.
func RemoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ClientIP is the caller's address as far as it can be trusted.
//
// X-Forwarded-For is a list each proxy appends to, so anything the caller put
// at the front is theirs to forge. Only the entries added by our own proxies,
// counted from the right, can be believed: with hops == 1 (one proxy in front,
// e.g. Cloud Run or Caddy) the last entry is the address that proxy saw. With
// hops == 0 no proxy is trusted and the header is ignored. If the list is
// shorter than hops, or the entry is not an IP, the socket address is used.
func ClientIP(r *http.Request, hops int) string {
	peer := RemoteIP(r)
	if hops <= 0 {
		return peer
	}
	var parts []string
	for _, v := range r.Header.Values("X-Forwarded-For") {
		for _, p := range strings.Split(v, ",") {
			parts = append(parts, strings.TrimSpace(p))
		}
	}
	if len(parts) < hops {
		return peer
	}
	if ip := net.ParseIP(parts[len(parts)-hops]); ip != nil {
		return ip.String()
	}
	return peer
}

// RealClientIP replaces r.RemoteAddr with ClientIP, so logs and rate limits
// downstream see the caller. It stands in for chi's RealIP, which believes the
// first X-Forwarded-For entry and so lets any caller choose their own address.
func RealClientIP(hops int) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.RemoteAddr = ClientIP(r, hops)
			next.ServeHTTP(w, r)
		})
	}
}
