package tool

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// DecodeWebFetchArgs extracts url/method/body from a web_fetch payload.
// A decode error is returned only when args are not a JSON object;
// missing fields are zero values. Empty method means GET.
func DecodeWebFetchArgs(args json.RawMessage) (fetchURL, method, body string, err error) {
	var a webFetchArgs
	if len(args) == 0 {
		return "", "", "", nil
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", "", "", err
	}
	return strings.TrimSpace(a.URL), strings.TrimSpace(a.Method), a.Body, nil
}

// NormalizeWebFetchMethod returns the HTTP method web_fetch will use.
// Empty input defaults to GET.
func NormalizeWebFetchMethod(method string) string {
	method = strings.ToUpper(strings.TrimSpace(method))
	if method == "" {
		return "GET"
	}
	return method
}

// PublicWebFetchURLError returns a descriptive error when raw is not a
// public http(s) URL. It does not resolve DNS; hostnames are allowed
// and IP literals are checked for loopback / private / link-local /
// multicast / unspecified. Used by the sub-agent execution gate so
// child agents cannot probe internal networks.
func PublicWebFetchURLError(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Errorf("url is required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid url: %w", err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("only http:// and https:// URLs are supported")
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "" {
		return fmt.Errorf("url host is required")
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || host == "0.0.0.0" {
		return fmt.Errorf("fetching from loopback addresses is not allowed")
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return nil
	}
	if !isPublicIP(ip) {
		return fmt.Errorf("fetching from non-public addresses is not allowed")
	}
	return nil
}

func isPublicIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	return !ip.IsLoopback() &&
		!ip.IsPrivate() &&
		!ip.IsLinkLocalUnicast() &&
		!ip.IsLinkLocalMulticast() &&
		!ip.IsMulticast() &&
		!ip.IsUnspecified()
}
