package api

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/labbersanon/sakms/internal/apidto"
)

// Claude 2026-09-30: in-memory GUID → enclosure cache for grab SSRF.
// Reason: POST /search/grab and AutoGrabRequest previously took a client
//   downloadUrl and fetched it server-side (authenticated SSRF). Prowlarr NZB
//   URLs are often LAN, so a private-IP deny on fetchNZB would break grabs.
//   Bind the URL at serialize time and look it up by opaque guid instead.
// Troubleshooting: grab 400 "unknown or expired release guid" → re-run Search.
// Review if: grab provenance is persisted with a server-side ticket store.
// Related files: search.go, autograb.go, autograb_batch.go, rss_feeds.go,
//   discover_availability.go, adultdiscover.go, dto.go

const releaseGUIDTTL = 30 * time.Minute

// errUnknownReleaseGUID is returned when grab/autograb is asked to dispatch a
// guid that was never remembered (or whose TTL expired).
var errUnknownReleaseGUID = errors.New("unknown or expired release guid — search again")

type releaseHandle struct {
	DownloadURL string
	Protocol    string
	expires     time.Time
}

type releaseGUIDCache struct {
	mu      sync.Mutex
	entries map[string]releaseHandle
	ttl     time.Duration
	now     func() time.Time
}

func newReleaseGUIDCache(ttl time.Duration) *releaseGUIDCache {
	return &releaseGUIDCache{
		entries: make(map[string]releaseHandle),
		ttl:     ttl,
		now:     time.Now,
	}
}

var grabReleaseCache = newReleaseGUIDCache(releaseGUIDTTL)

func opaqueReleaseGUID(downloadURL, protocol string) string {
	sum := sha256.Sum256([]byte(protocol + "\x00" + downloadURL))
	return hex.EncodeToString(sum[:16])
}

func (c *releaseGUIDCache) remember(guid, downloadURL, protocol string) string {
	downloadURL = strings.TrimSpace(downloadURL)
	protocol = strings.TrimSpace(protocol)
	if downloadURL == "" {
		return strings.TrimSpace(guid)
	}
	guid = strings.TrimSpace(guid)
	if guid == "" {
		guid = opaqueReleaseGUID(downloadURL, protocol)
	}
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pruneLocked(now)
	c.entries[guid] = releaseHandle{
		DownloadURL: downloadURL,
		Protocol:    protocol,
		expires:     now.Add(c.ttl),
	}
	return guid
}

func (c *releaseGUIDCache) lookup(guid string) (releaseHandle, bool) {
	guid = strings.TrimSpace(guid)
	if guid == "" {
		return releaseHandle{}, false
	}
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pruneLocked(now)
	h, ok := c.entries[guid]
	if !ok || now.After(h.expires) {
		delete(c.entries, guid)
		return releaseHandle{}, false
	}
	return h, true
}

func (c *releaseGUIDCache) pruneLocked(now time.Time) {
	for k, h := range c.entries {
		if now.After(h.expires) {
			delete(c.entries, k)
		}
	}
}

func rememberSearchRelease(r *apidto.SearchReleaseResult) {
	if r == nil {
		return
	}
	r.GUID = grabReleaseCache.remember(r.GUID, r.DownloadURL, r.Protocol)
}

func rememberAutoGrabCandidate(c *apidto.AutoGrabCandidate) {
	if c == nil {
		return
	}
	c.GUID = grabReleaseCache.remember(c.GUID, c.DownloadURL, c.Protocol)
}

func rememberAvailabilityCandidate(c *apidto.AvailabilityCandidate) {
	if c == nil {
		return
	}
	c.GUID = grabReleaseCache.remember(c.GUID, c.DownloadURL, c.Protocol)
}

func rememberRssFeedItem(it *apidto.RssFeedItem) {
	if it == nil {
		return
	}
	it.GUID = grabReleaseCache.remember(it.GUID, it.DownloadURL, it.Protocol)
}

func rememberAdultDiscoverItem(it *apidto.AdultDiscoverItem) {
	if it == nil {
		return
	}
	it.GUID = grabReleaseCache.remember(it.GUID, it.DownloadURL, it.Protocol)
}

func rememberAdultNewestReleaseItem(it *apidto.AdultNewestReleaseItem) {
	if it == nil {
		return
	}
	it.GUID = grabReleaseCache.remember(it.GUID, it.DownloadURL, it.Protocol)
}

func rememberAdultScene(s *adultScene) {
	if s == nil {
		return
	}
	s.GUID = grabReleaseCache.remember(s.GUID, s.DownloadURL, s.Protocol)
}

// resolveClientEnclosure replaces a client-supplied AutoGrabRequest enclosure
// with the URL remembered for req.GUID. json:"-" on AutoGrabRequest.DownloadURL
// already drops a forged wire-format URL; a non-empty guid is the browser path
// and must resolve or the grab is rejected. An empty guid leaves DownloadURL
// unchanged so the Adult persisted-release feeder and in-process callers still
// work.
func resolveClientEnclosure(req *apidto.AutoGrabRequest) error {
	guid := strings.TrimSpace(req.GUID)
	if guid == "" {
		return nil
	}
	h, ok := grabReleaseCache.lookup(guid)
	if !ok {
		return errUnknownReleaseGUID
	}
	req.DownloadURL = h.DownloadURL
	req.DownloadProtocol = h.Protocol
	return nil
}
