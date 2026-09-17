package usenet

import "errors"

// Claude 2026-09-17: content-unusable sentinels, distinct from transport errors.
// Reason: the api layer needs a typed predicate to decide "try a different
//   release" vs "retry the same release later". Transport errors wrap ErrTransport;
//   content failures wrap ErrContentUnusable (PAR2, unpack) or library.ErrNoVideoFile.
//   ErrUnpackToolMissing is an environment fault — a different release cannot fix it.
// Troubleshooting: journal "par2 repair … failing download" or "unpack … no video".
// Review if: a fourth content-failure kind is added that warrants a different route.
// Related files: internal/api/usenetcontent.go (routing), manager.go (wrap sites).

// ErrContentUnusable marks a failure of the DELIVERED RELEASE rather than of
// the connection or the article: the archive would not unpack, PAR2 could not
// repair it, or the assembly produced no usable video.
var ErrContentUnusable = errors.New("usenet: the downloaded release is unusable")

// ErrUnpackToolMissing is an ENVIRONMENT fault — no unrar or 7z in the image.
// A different release cannot fix it, so it must NOT be content-classified.
var ErrUnpackToolMissing = errors.New("usenet: no unpacker available")
