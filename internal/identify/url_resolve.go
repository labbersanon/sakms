package identify

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// ResolveSceneURLResult is the outcome of resolving a pasted URL for Adult repick.
type ResolveSceneURLResult struct {
	Item    *SceneCandidate
	Message string // set when Item is nil — operator-facing, not an HTTP 500
}

// ResolveSceneFromURL resolves a catalog scene/movie URL directly, or fetches an
// arbitrary studio/tube URL and uses AI to extract metadata before catalog search.
func (id *Identifier) ResolveSceneFromURL(ctx context.Context, httpClient *http.Client, rawURL string) (ResolveSceneURLResult, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return ResolveSceneURLResult{Message: "URL is required"}, nil
	}
	if id.Boxes == nil {
		return ResolveSceneURLResult{Message: "no adult metadata database is configured"}, nil
	}

	if box, sceneID, isMovie, ok := ParseCatalogSceneURL(rawURL); ok {
		match, err := id.Boxes.ResolveCatalogRef(ctx, box, sceneID, isMovie)
		if err != nil {
			return ResolveSceneURLResult{}, err
		}
		if match == nil || match.SceneID == "" {
			return ResolveSceneURLResult{Message: "catalog entry not found for that URL"}, nil
		}
		c := sceneCandidateFromMatch(match)
		return ResolveSceneURLResult{Item: &c}, nil
	}

	if id.AI == nil {
		return ResolveSceneURLResult{Message: "AI is required to parse non-catalog URLs — configure an AI provider in Settings"}, nil
	}

	if id.Throttle != nil {
		if err := id.Throttle.Wait(ctx, "url_resolve"); err != nil {
			return ResolveSceneURLResult{}, err
		}
	}
	page, err := FetchPageSnippet(ctx, httpClient, rawURL)
	if err != nil {
		if errorsIsURLBlocked(err) {
			return ResolveSceneURLResult{Message: "that URL points to a private or internal address"}, nil
		}
		return ResolveSceneURLResult{Message: fmt.Sprintf("could not fetch URL: %v", err)}, nil
	}

	if id.Throttle != nil {
		if err := id.Throttle.Wait(ctx, "ai"); err != nil {
			return ResolveSceneURLResult{}, err
		}
	}
	grounded, err := ExtractFromURL(ctx, id.AI, page)
	if err != nil {
		return ResolveSceneURLResult{}, err
	}
	if grounded.Title == "" || grounded.Studio == "" {
		return ResolveSceneURLResult{Message: "could not identify studio and title from that page"}, nil
	}

	match, err := id.catalogMatchFromGrounded(ctx, grounded)
	if err != nil {
		return ResolveSceneURLResult{}, err
	}
	if match == nil || match.SceneID == "" {
		return ResolveSceneURLResult{Message: "identified content but no matching catalog entry was found — try text search instead"}, nil
	}
	c := sceneCandidateFromMatch(match)
	return ResolveSceneURLResult{Item: &c}, nil
}

func (id *Identifier) catalogMatchFromGrounded(ctx context.Context, grounded GroundedExtraction) (*MatchResult, error) {
	return id.reSearchAfterGrounding(ctx, grounded)
}

func sceneCandidateFromMatch(m *MatchResult) SceneCandidate {
	return SceneCandidate{
		Box:             m.Box,
		SceneID:         m.SceneID,
		Title:           m.Title,
		Studio:          m.Studio,
		Date:            m.Date,
		ImageURL:        m.Image,
		DurationSeconds: m.RuntimeSeconds,
	}
}

func errorsIsURLBlocked(err error) bool {
	return errors.Is(err, ErrURLNotAllowed)
}
