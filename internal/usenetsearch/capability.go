package usenetsearch

import (
	"context"
	"strings"

	"github.com/labbersanon/sakms/internal/usenet"
)

// Probe runs the fail-closed capability check against the HeaderSource.
func Probe(ctx context.Context, src usenet.HeaderSource, groups []string) Readiness {
	if src == nil {
		return Readiness{Ready: false, State: "unsupported", Detail: "no usenet subscriptions"}
	}
	if len(groups) == 0 {
		return Readiness{Ready: false, State: "degraded", Detail: "no groups configured"}
	}
	caps, err := src.Capabilities(ctx)
	if err != nil {
		return Readiness{Ready: false, State: "degraded", Detail: "CAPABILITIES failed: " + err.Error()}
	}
	if !hasOVER(caps) {
		return Readiness{Ready: false, State: "unsupported", Detail: "server has no OVER/XOVER"}
	}
	g := strings.TrimSpace(groups[0])
	gr, err := src.GroupRange(ctx, g)
	if err != nil {
		return Readiness{Ready: false, State: "unsupported", Detail: "GROUP " + g + ": " + err.Error()}
	}
	if gr.High < gr.Low {
		return Readiness{Ready: false, State: "unsupported", Detail: "GROUP " + g + " empty"}
	}
	from := gr.High - 64
	if from < gr.Low {
		from = gr.Low
	}
	rows, err := src.Overview(ctx, g, from, gr.High)
	if err != nil {
		return Readiness{Ready: false, State: "degraded", Detail: "OVER smoke failed: " + err.Error()}
	}
	if len(rows) == 0 {
		return Readiness{Ready: false, State: "degraded", Detail: "OVER smoke returned no rows"}
	}
	return Readiness{Ready: true, State: "ok", Detail: "OVER ok on " + g}
}

func hasOVER(caps []string) bool {
	for _, line := range caps {
		fields := strings.Fields(strings.ToUpper(line))
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case "OVER", "XOVER":
			return true
		}
		// LIST OVERVIEW.FMT implies overview support
		if fields[0] == "LIST" {
			joined := strings.ToUpper(line)
			if strings.Contains(joined, "OVERVIEW") {
				return true
			}
		}
	}
	// Eweka advertises XOVER as a capability token on its own line
	for _, line := range caps {
		if strings.EqualFold(strings.TrimSpace(line), "XOVER") ||
			strings.EqualFold(strings.TrimSpace(line), "OVER") {
			return true
		}
	}
	return false
}
