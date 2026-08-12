package identify

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// ErrURLNotAllowed is returned when a URL targets a private/internal address.
var ErrURLNotAllowed = errors.New("url resolves to a private or internal address")

func blockedIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast()
}

func validatePublicHost(ctx context.Context, host string) error {
	if ip := net.ParseIP(host); ip != nil {
		if blockedIP(ip) {
			return fmt.Errorf("%w: %q", ErrURLNotAllowed, host)
		}
		return nil
	}
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return fmt.Errorf("%w: could not resolve %q: %v", ErrURLNotAllowed, host, err)
	}
	for _, ip := range ips {
		if blockedIP(ip) {
			return fmt.Errorf("%w: %q resolves to %s", ErrURLNotAllowed, host, ip)
		}
	}
	return nil
}

func parsePublicHTTPURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("invalid URL")
	}
	switch u.Scheme {
	case "http", "https":
	default:
		return nil, fmt.Errorf("URL must use http or https")
	}
	return u, nil
}
