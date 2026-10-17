package usenetsearch

import "sort"

// Claude 2026-10-15: strict payload completeness (approved plan
// nntp-gap-fill.md D4), shared by the index recompute pass and search-side
// assembleFromRows so both sides agree on one definition.
// Reason: the old lenient rules (want := max part seen, SegmentCount/50
// tolerance, HasPAR2 && <=2% override) admitted releases that could never
// repair and ignored whole missing files.
// Troubleshooting: release invisible although it "looks" complete → check
//   gate_reason on usenet_nntp_releases ('undeclared' = a payload file has no
//   part_m; 'no_payload' = only PAR2/NFO/SFV seen).
// Review if: PAR2-index-driven file lists replace the [k/N] estimate.

// Gate defaults and bounds for the strict completeness threshold.
const (
	DefaultMinCompletenessPct = 95
	minCompletenessPctFloor   = 80
	minCompletenessPctCeil    = 100
)

// Gate reasons stored on usenet_nntp_releases.gate_reason.
const (
	gateReasonOK         = ""
	gateReasonUndeclared = "undeclared"
	gateReasonNoPayload  = "no_payload"
	gateReasonBelow      = "below_gate"
)

// ClampMinCompletenessPct maps 0/unset to the default and clamps to 80-100.
func ClampMinCompletenessPct(p int) int {
	if p <= 0 {
		return DefaultMinCompletenessPct
	}
	if p < minCompletenessPctFloor {
		return minCompletenessPctFloor
	}
	if p > minCompletenessPctCeil {
		return minCompletenessPctCeil
	}
	return p
}

// fileAgg is one file's declared/seen segment accounting.
type fileAgg struct {
	Meta  bool
	PartM int          // declared segment count (max seen); 0 = undeclared
	Seen  map[int]bool // distinct part numbers seen
	yenc  int64        // first yEnc file size seen; counted once into payload
}

// addFilePayload adds one segment of agg's file to payload: the yEnc file size
// once per file, or the segment's wire bytes less yEnc overhead when the
// subject carried no yEnc size.
//
// Claude 2026-10-16: yEnc size is the whole file, not the segment.
// Reason: summing yenc on every part made one pending_name set report ~688GB
// and the unnamed cap read 8.5 TiB, so skip_obf_cap fired on every core.
// Troubleshooting: payload_bytes ≈ file count × yenc, not segments × yenc.
// Review if: yEnc size is stored per-segment instead of copied from the subject.
func addFilePayload(payload *int64, agg *fileAgg, yenc, bytes int64) {
	if yenc > 0 {
		if agg.yenc == 0 {
			agg.yenc = yenc
			*payload += yenc
		}
		return
	}
	*payload += int64(float64(bytes) / 1.37)
}

// completeness is the strict-gate accounting for one release.
type completeness struct {
	Declared   int
	Seen       int
	Undeclared bool
	NoPayload  bool
}

// computeCompleteness applies the D4 definition. fileM is the "[k/N]" total
// (0 when unknown); files includes meta files so they count toward "files
// seen" without contributing to declared/seen.
func computeCompleteness(files []fileAgg, fileM int) completeness {
	// Claude 2026-10-16: ignore unique-ID yEnc (n/m) / [k/N] totals.
	// Reason: see maxYencPartM; 967646-part "files" made every flood article 0% complete.
	// Troubleshooting: completeness_bp=0 with seen=1 declared≈1e6 → this clamp.
	// Review if: maxYencPartM changes.
	if fileM > maxYencPartM {
		fileM = 0
	}
	var c completeness
	var payloadM []int
	for _, f := range files {
		if f.Meta {
			continue
		}
		if f.PartM <= 0 || f.PartM > maxYencPartM {
			c.Undeclared = true
			continue
		}
		c.Declared += f.PartM
		payloadM = append(payloadM, f.PartM)
		for n := range f.Seen {
			if n >= 1 && n <= f.PartM {
				c.Seen++
			}
		}
	}
	if len(payloadM) == 0 && !c.Undeclared {
		c.NoPayload = true
		return c
	}
	if missing := fileM - len(files); missing > 0 && len(payloadM) > 0 {
		sort.Ints(payloadM)
		median := payloadM[len(payloadM)/2]
		c.Declared += missing * median
	}
	return c
}

// BP is seen/declared in basis points (floor). 0 when declared is unknown/zero.
func (c completeness) BP() int {
	if c.Undeclared || c.NoPayload || c.Declared <= 0 {
		return 0
	}
	return c.Seen * 10000 / c.Declared
}

// Meets reports whether seen/declared >= gatePct percent. Cross-multiplied so
// 94.9% and 95.0% never collapse through integer truncation.
func (c completeness) Meets(gatePct int) bool {
	if c.Undeclared || c.NoPayload || c.Declared <= 0 {
		return false
	}
	return c.Seen*100 >= ClampMinCompletenessPct(gatePct)*c.Declared
}

// Reason is the stored gate_reason for the given threshold.
func (c completeness) Reason(gatePct int) string {
	switch {
	case c.Undeclared:
		return gateReasonUndeclared
	case c.NoPayload:
		return gateReasonNoPayload
	case !c.Meets(gatePct):
		return gateReasonBelow
	default:
		return gateReasonOK
	}
}

// Pct is seen/declared as a percentage (0 when unknown).
func (c completeness) Pct() float64 {
	if c.Undeclared || c.NoPayload || c.Declared <= 0 {
		return 0
	}
	return float64(c.Seen) * 100 / float64(c.Declared)
}

// Missing is declared-seen (estimated segments of wholly-missing files included).
func (c completeness) Missing() int {
	if c.Undeclared || c.NoPayload {
		return 0
	}
	if m := c.Declared - c.Seen; m > 0 {
		return m
	}
	return 0
}
