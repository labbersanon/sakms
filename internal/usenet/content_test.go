package usenet

import (
	"errors"
	"fmt"
	"testing"
)

// TestContentClassification is the "classification at source" acceptance test:
// unpack failures (and PAR2-fail with no resulting video) wrap ErrContentUnusable;
// ErrUnpackToolMissing does not; ErrTransport does not.
// PAR2-alone no longer wraps at the PAR2 site when unpack extracts a video.
func TestContentClassification(t *testing.T) {
	someRepairErr := fmt.Errorf("par2: file corrupt")
	someUnpackErr := fmt.Errorf("unrar: bad archive")

	for _, tc := range []struct {
		name        string
		err         error
		wantContent bool // errors.Is(err, ErrContentUnusable)
		wantTool    bool // errors.Is(err, ErrUnpackToolMissing)
		wantTransp  bool // errors.Is(err, ErrTransport)
	}{
		{
			name:        "PAR2 repair error wraps ErrContentUnusable",
			err:         fmt.Errorf("%w: %w", ErrContentUnusable, someRepairErr),
			wantContent: true,
		},
		{
			name:        "unpack error wraps ErrContentUnusable",
			err:         fmt.Errorf("%w: %w", ErrContentUnusable, someUnpackErr),
			wantContent: true,
		},
		{
			name:     "ErrUnpackToolMissing passes through unwrapped — not content",
			err:      ErrUnpackToolMissing,
			wantTool: true,
		},
		{
			name:       "ErrTransport is not content",
			err:        fmt.Errorf("%w: dial timeout", ErrTransport),
			wantTransp: true,
		},
		{
			name: "ErrArticleNotFound (430) is not content",
			err:  fmt.Errorf("segment: %w", ErrArticleNotFound),
		},
		{
			name: "ErrArticleRemoved (451) is not content",
			err:  fmt.Errorf("segment: %w", ErrArticleRemoved),
		},
		{
			name: "context.Canceled is not content",
			err:  fmt.Errorf("download: %w", errors.New("context canceled")),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := errors.Is(tc.err, ErrContentUnusable); got != tc.wantContent {
				t.Errorf("errors.Is(ErrContentUnusable) = %v, want %v", got, tc.wantContent)
			}
			if got := errors.Is(tc.err, ErrUnpackToolMissing); got != tc.wantTool {
				t.Errorf("errors.Is(ErrUnpackToolMissing) = %v, want %v", got, tc.wantTool)
			}
			if got := errors.Is(tc.err, ErrTransport); got != tc.wantTransp {
				t.Errorf("errors.Is(ErrTransport) = %v, want %v", got, tc.wantTransp)
			}
		})
	}
}

// TestErrUnpackToolMissingIsNotContent asserts ErrUnpackToolMissing is not
// content-classified (a missing unrar/7z must not burn alternate-release slots).
// Gate-order coverage for PAR2→unpack lives in finalize_assembled_test.go.
func TestErrUnpackToolMissingIsNotContent(t *testing.T) {
	// Regression: if ErrUnpackToolMissing were wrapped with ErrContentUnusable,
	// a missing unrar/7z would burn 3 downloads per request. It must NOT be
	// content-classified.
	err := ErrUnpackToolMissing
	if errors.Is(err, ErrContentUnusable) {
		t.Error("ErrUnpackToolMissing must not satisfy errors.Is(ErrContentUnusable)")
	}
}
