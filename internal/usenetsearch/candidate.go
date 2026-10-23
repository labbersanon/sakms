package usenetsearch

import (
	"context"
	"time"

	"github.com/labbersanon/sakms/internal/usenet"
)

// Query is a backend-neutral search request. Mode translation happens in api.
type Query struct {
	Terms       []string
	AnyOf       [][]string
	Exclude     []string
	Groups      []string // empty = all indexed groups
	PostedAfter time.Time
	MinBytes    int64
	MaxBytes    int64
	Limit       int
	// GrabReady keeps only Complete candidates (autograb / NZB). Manual search
	// omits it so ident-named titles below the completeness gate still match.
	GrabReady bool
}

// Candidate is one reconstructed release from the local header index.
type Candidate struct {
	Name         string
	Group        string
	Poster       string
	PostedAt     time.Time
	PayloadBytes int64 // scorer size (yEnc-corrected)
	WireBytes    int64 // sum of OVER :bytes
	FileCount    int
	SegmentCount int
	// Complete means CompletenessPct >= the strict gate (usenet_nntp_min_completeness_pct).
	Complete bool
	// CompletenessPct is seen/declared payload segments (0 when part_m is undeclared).
	CompletenessPct float64
	MissingParts    int
	HasPAR2         bool
	Files           []CandidateFile
	// SeedMsgID is the first segment message-id (locator anchor).
	SeedMsgID string
}

// CandidateFile is one file within a release.
type CandidateFile struct {
	Subject  string
	Filename string
	Segs     []CandidateSeg
}

// CandidateSeg is one NNTP article.
type CandidateSeg struct {
	Bytes  int64
	Number int
	MsgID  string
}

// Readiness is the fail-closed gate for the native phase.
type Readiness struct {
	Ready  bool
	State  string // unknown|ok|degraded|unsupported
	Detail string
}

// Backend is the search half.
type Backend interface {
	Ready(ctx context.Context) (Readiness, error)
	Search(ctx context.Context, q Query) ([]Candidate, error)
}

// Assembler is the dispatch half.
type Assembler interface {
	ToNZB(c Candidate) (*usenet.NZB, error)
}
