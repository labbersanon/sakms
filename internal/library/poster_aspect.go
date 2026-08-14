package library

import (
	"bytes"
	"context"
	"errors"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"strings"

	"github.com/labbersanon/sakms/internal/imageproxy"
)

const (
	PosterAspectVertical   = "vertical"
	PosterAspectHorizontal = "horizontal"
)

// posterAspectTarget is width/height for a 2:3 portrait poster.
const posterAspectTarget = 2.0 / 3.0

// posterAspectTolerance is relative ±5% of 2:3 (spec: catalog poster within
// ~5% of 2:3 → vertical; otherwise horizontal).
const posterAspectTolerance = 0.05

// posterProxy is the process-wide SSRF-guarded fetcher ProbePosterAspect uses.
// cmd/sakms sets it via SetPosterProxy; tests leave it nil and/or install
// posterFetchOverride.
var posterProxy *imageproxy.Proxy

// posterFetchOverride is test-only: after imageproxy.Validate succeeds,
// ProbePosterAspect uses this instead of posterProxy.Fetch so a 2:3 PNG can
// be classified without dialing the public internet. Production stays nil.
var posterFetchOverride func(ctx context.Context, rawURL string) ([]byte, error)

// SetPosterProxy installs the process image proxy used to fetch catalog
// posters for ProbePosterAspect. Called once from cmd/sakms alongside the
// handler proxy — same SSRF guardrail (https-only, private/loopback blocked
// on every redirect).
func SetPosterProxy(p *imageproxy.Proxy) {
	posterProxy = p
}

// SetPosterFetchOverride is a test hook. Production callers must not use it.
func SetPosterFetchOverride(fn func(ctx context.Context, rawURL string) ([]byte, error)) {
	posterFetchOverride = fn
}

// ClassifyPosterAspect returns vertical when width/height is within ±5% of
// 2:3, otherwise horizontal. Zero or negative dimensions (missing poster)
// are horizontal.
func ClassifyPosterAspect(width, height int) string {
	if width <= 0 || height <= 0 {
		return PosterAspectHorizontal
	}
	ratio := float64(width) / float64(height)
	delta := ratio - posterAspectTarget
	if delta < 0 {
		delta = -delta
	}
	if delta/posterAspectTarget <= posterAspectTolerance {
		return PosterAspectVertical
	}
	return PosterAspectHorizontal
}

func normalizePosterAspect(class string) string {
	if strings.TrimSpace(class) == PosterAspectVertical {
		return PosterAspectVertical
	}
	return PosterAspectHorizontal
}

// ParsePosterAspectFilter accepts "", "vertical", or "horizontal". Empty means
// unfiltered (every library_scenes row). Anything else is an error the HTTP
// layer maps to 400.
func ParsePosterAspectFilter(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	switch raw {
	case "":
		return "", nil
	case PosterAspectVertical, PosterAspectHorizontal:
		return raw, nil
	default:
		return "", ErrInvalidPosterAspect
	}
}

// MatchesPosterAspect is true when want is empty (All) or class normalizes
// to want. Empty/unknown stored class is horizontal, matching ProbePosterAspect.
func MatchesPosterAspect(class, want string) bool {
	if strings.TrimSpace(want) == "" {
		return true
	}
	return normalizePosterAspect(class) == want
}

// ProbePosterAspect classifies a catalog poster URL. Empty URL, failed
// imageproxy.Validate (non-https, private/loopback, malformed), fetch
// failure, or undecodable bytes (including WebP until a decoder is added)
// all return horizontal — grab/import must not fail or block on art.
func ProbePosterAspect(ctx context.Context, imageURL string) string {
	if strings.TrimSpace(imageURL) == "" {
		return PosterAspectHorizontal
	}
	if _, err := imageproxy.Validate(ctx, imageURL); err != nil {
		return PosterAspectHorizontal
	}
	body, err := fetchPosterBytes(ctx, imageURL)
	if err != nil {
		return PosterAspectHorizontal
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(body))
	if err != nil {
		return PosterAspectHorizontal
	}
	return ClassifyPosterAspect(cfg.Width, cfg.Height)
}

// sanitizePosterURL keeps a catalog image URL only if imageproxy.Validate
// accepts it (http/https, no SSRF). Empty or rejected URLs store as "".
func sanitizePosterURL(ctx context.Context, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if _, err := imageproxy.Validate(ctx, raw); err != nil {
		return ""
	}
	return raw
}

func fetchPosterBytes(ctx context.Context, imageURL string) ([]byte, error) {
	if posterFetchOverride != nil {
		return posterFetchOverride(ctx, imageURL)
	}
	if posterProxy == nil {
		return nil, errNoPosterProxy
	}
	img, err := posterProxy.Fetch(ctx, imageURL)
	if err != nil {
		return nil, err
	}
	return img.Body, nil
}

var errNoPosterProxy = errors.New("poster proxy not configured")

// ErrInvalidPosterAspect is returned by ParsePosterAspectFilter for any value
// other than "", "vertical", or "horizontal".
var ErrInvalidPosterAspect = errors.New("aspect must be vertical or horizontal")
