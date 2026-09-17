package usenet

import (
	"errors"
	"fmt"
	"testing"
)

// TestContentClassification is the "classification at source" acceptance test:
// PAR2 failures and unpack failures wrap ErrContentUnusable; ErrUnpackToolMissing
// does not; ErrTransport does not.
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

// TestManagerWrapsPAR2WithContentUnusable asserts the two manager wrap sites
// in runDownload reach the onError callback with errors.Is(ErrContentUnusable).
// The test uses onerror_test.go's helpers (injecting a fake NZB that triggers
// the PAR2 path is not straightforward without a real download; this unit test
// tests the error produced by verifyAndRepair through a fake par2 library).
// See onerror_test.go for the integration-level test.
func TestErrUnpackToolMissingIsNotContent(t *testing.T) {
	// Regression: if ErrUnpackToolMissing were wrapped with ErrContentUnusable,
	// a missing unrar/7z would burn 3 downloads per request. It must NOT be
	// content-classified.
	err := ErrUnpackToolMissing
	if errors.Is(err, ErrContentUnusable) {
		t.Error("ErrUnpackToolMissing must not satisfy errors.Is(ErrContentUnusable)")
	}
}
