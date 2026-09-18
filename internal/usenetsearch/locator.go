package usenetsearch

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

const locatorScheme = "sakms-nntp"
const locatorVersion = "v1"

// EncodeLocator builds a stable sakms-nntp:v1 URI for retry/relaunch provenance.
func EncodeLocator(group, seedMsgID, name string, segCount int, wireBytes int64) string {
	q := url.Values{}
	q.Set("g", group)
	q.Set("m", base64.RawURLEncoding.EncodeToString([]byte(seedMsgID)))
	q.Set("n", base64.RawURLEncoding.EncodeToString([]byte(name)))
	q.Set("s", strconv.Itoa(segCount))
	q.Set("b", strconv.FormatInt(wireBytes, 10))
	return locatorScheme + ":" + locatorVersion + "?" + q.Encode()
}

// LocatorParts is the decoded locator.
type LocatorParts struct {
	Group     string
	SeedMsgID string
	Name      string
	SegCount  int
	WireBytes int64
}

// DecodeLocator parses a sakms-nntp: URI. Returns ok=false for other schemes.
func DecodeLocator(raw string) (LocatorParts, bool, error) {
	if !strings.HasPrefix(raw, locatorScheme+":") {
		return LocatorParts{}, false, nil
	}
	rest := strings.TrimPrefix(raw, locatorScheme+":")
	u, err := url.Parse(rest)
	if err != nil {
		return LocatorParts{}, true, fmt.Errorf("usenetsearch: bad locator: %w", err)
	}
	if u.Opaque != "" && u.Path == "" {
		// sakms-nntp:v1?… parses with Opaque sometimes; handle opaque+raw query
	}
	path := u.Path
	if path == "" {
		path = u.Opaque
	}
	if path != "" && path != locatorVersion && !strings.HasPrefix(path, locatorVersion) {
		// allow v1 as opaque
	}
	q := u.Query()
	if len(q) == 0 && strings.Contains(rest, "?") {
		_, query, _ := strings.Cut(rest, "?")
		q, err = url.ParseQuery(query)
		if err != nil {
			return LocatorParts{}, true, err
		}
	}
	g := q.Get("g")
	mEnc := q.Get("m")
	nEnc := q.Get("n")
	if g == "" || mEnc == "" {
		return LocatorParts{}, true, fmt.Errorf("usenetsearch: locator missing g/m")
	}
	mBytes, err := base64.RawURLEncoding.DecodeString(mEnc)
	if err != nil {
		return LocatorParts{}, true, fmt.Errorf("usenetsearch: locator m: %w", err)
	}
	var name string
	if nEnc != "" {
		nb, err := base64.RawURLEncoding.DecodeString(nEnc)
		if err != nil {
			return LocatorParts{}, true, fmt.Errorf("usenetsearch: locator n: %w", err)
		}
		name = string(nb)
	}
	seg, _ := strconv.Atoi(q.Get("s"))
	wire, _ := strconv.ParseInt(q.Get("b"), 10, 64)
	return LocatorParts{
		Group:     g,
		SeedMsgID: string(mBytes),
		Name:      name,
		SegCount:  seg,
		WireBytes: wire,
	}, true, nil
}

// IsLocator reports whether s is a native article-set locator.
func IsLocator(s string) bool {
	return strings.HasPrefix(s, locatorScheme+":")
}
