package usenet

import (
	"errors"
	"fmt"
)

// Claude 2026-09-17: content-unusable sentinels, distinct from transport errors.
// Reason: the api layer needs a typed predicate to decide "try a different
//   release" vs "retry the same release later". Transport errors wrap ErrTransport;
//   content failures wrap ErrContentUnusable (PAR2, unpack) or library.ErrNoVideoFile.
//   ErrUnpackToolMissing is an environment fault — a different release cannot fix it.
// Troubleshooting: journal "par2 repair … continuing to unpack" then
//   "unpack … failing download", or "par2 … no video after unpack".
// Review if: a fourth content-failure kind is added that warrants a different route.
// Related files: internal/api/usenetcontent.go (routing), manager.go (wrap sites).

// ErrContentUnusable marks a failure of the DELIVERED RELEASE rather than of
// the connection or the article: the archive would not unpack, PAR2 could not
// repair a non-archive staging dir, or the assembly produced no usable video.
var ErrContentUnusable = errors.New("usenet: the downloaded release is unusable")

// Claude 2026-09-28: unpack/flat staging with no playable video is its own sentinel.
// Reason: no-archive + no video used to return nil from unpackArchives, then
//
//	finalize marked complete (PAR2-ok path). Import sometimes never parked.
//	Operators saw "Complete" then "no video unpacked". Fail-closed here so
//	applyUsenetFailure parks the next Usenet alternate like a 430.
//
// Troubleshooting: journal "unpack produced no video"; Requests no-usable-video reason.
// Review if: ISO/IMG disc images should count as delivery without a video ext.
var ErrNoVideoUnpacked = fmt.Errorf("%w: unpack produced no video", ErrContentUnusable)

// Claude 2026-09-28: precheck video peek shares this sentinel with unpack.
// Reason: RunAutoGrab / Search & pick already park ErrNoVideoUnpacked as
//
//	"try a different NZB". Returning it from precheckNZB skips BODY+disk.
//
// Troubleshooting: journal "usenet precheck: abort — no usable video".
// Review if: a distinct precheck-only sentinel is needed for operator copy.
// Related: precheck_video.go, IsPrecheckReject.
func IsPrecheckReject(err error) bool {
	return errors.Is(err, ErrArticlesUnavailable) || errors.Is(err, ErrNoVideoUnpacked)
}

// Claude 2026-09-19: typed password-archive sentinel (wraps ErrContentUnusable).
// Reason: operators need a distinct reason + fail-fast path; routing stays on
//
//	the alternate-release park via errors.Is(..., ErrContentUnusable).
//
// Troubleshooting: journal "password-protected archive"; Requests shows password reason.
// Review if: password-file support is added (then this may become retryable same-NZB).
var ErrPasswordProtected = fmt.Errorf("%w: password-protected archive (unsupported)", ErrContentUnusable)

// ErrUnpackToolMissing is an ENVIRONMENT fault — no unrar or 7z in the image.
// A different release cannot fix it, so it must NOT be content-classified.
var ErrUnpackToolMissing = errors.New("usenet: no unpacker available")
