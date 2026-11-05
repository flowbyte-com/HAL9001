package websearch

import (
	"net"
	"net/url"
	"strings"
)

// ValidResultURL accepts one public http(s) search-result URL. It does not
// resolve DNS: search results are data we intend to show, not fetch. Literal
// local and special-use addresses are rejected so a provider cannot smuggle
// an internal target into the transcript.
func ValidResultURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 4096 {
		return "", &Error{Code: CodeInvalidURL, Message: "search result URL is empty or too long"}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", &Error{Code: CodeInvalidURL, Message: "search result URL is not valid"}
	}
	if u.User != nil {
		return "", &Error{Code: CodeInvalidURL, Message: "search result URL contains credentials"}
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", &Error{Code: CodeInvalidURL, Message: "search result URL must use http or https"}
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "" {
		return "", &Error{Code: CodeInvalidURL, Message: "search result URL has no host"}
	}
	if isLocalHostname(host) {
		return "", &Error{Code: CodeInvalidURL, Message: "search result URL points to a local host"}
	}
	if ip := net.ParseIP(host); ip != nil {
		if !isPublicIP(ip) {
			return "", &Error{Code: CodeInvalidURL, Message: "search result URL points to a non-public address"}
		}
		return u.String(), nil
	}
	if !strings.Contains(host, ".") {
		return "", &Error{Code: CodeInvalidURL, Message: "search result URL points to a local host"}
	}
	return u.String(), nil
}

func isLocalHostname(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	for _, suffix := range []string{
		".local", ".localdomain", ".internal", ".intranet", ".lan",
		".home.arpa", ".home", ".corp", ".private",
	} {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}

var specialUseNetworks = func() []*net.IPNet {
	cidrs := []string{
		"0.0.0.0/8",
		"100.64.0.0/10",
		"192.0.0.0/24",
		"192.0.2.0/24",
		"198.18.0.0/15",
		"198.51.100.0/24",
		"203.0.113.0/24",
		"240.0.0.0/4",
		"2001:db8::/32",
	}
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, cidr := range cidrs {
		if _, block, err := net.ParseCIDR(cidr); err == nil {
			out = append(out, block)
		}
	}
	return out
}()

func isPublicIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return false
	}
	if !ip.IsGlobalUnicast() {
		return false
	}
	for _, block := range specialUseNetworks {
		if block.Contains(ip) {
			return false
		}
	}
	return true
}
