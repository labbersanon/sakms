package usenet

import "regexp"

// Claude 2026-10-22: trailer/sample files are not payload.
// Reason: NZBmegasearch skipped subjects matching [\.\-]sample when tallying
//
//	NZB files (PAR2/NFO vs archive). OVER was counting Movie.sample.mkv as
//	payload, which inflated payload_bytes / occupancy and could steal a
//	namefix BODY slot as a fake first-volume mkv.
//
// Troubleshooting: completeness_bp pulled down by a missing sample, or BODY
//
//	fetching *.sample.mkv → this predicate missed the name. Do not match
//	"Sampler" / "samples" (need a separator after sample).
//
// Review if: a real release title is the word Sample with a scene tag.
var reSampleName = regexp.MustCompile(`(?i)(?:^|[.\-_ ])sample(?:[.\-_ ]|$|\.[a-z0-9]{2,4}$)`)

// IsSampleName reports a trailer/sample file that is not the release payload.
func IsSampleName(s string) bool {
	return reSampleName.MatchString(s)
}
