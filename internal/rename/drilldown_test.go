package rename

import (
	"context"
	"testing"

	"github.com/labbersanon/sakms/internal/mediainfo"
)

func TestExtractFileSignals_YearAndActor(t *testing.T) {
	sig := ExtractFileSignals(context.Background(), "Which Way Is Up - Richard Pryor (1977).mp4", "", nil)
	if sig.Year != 1977 {
		t.Fatalf("year=%d", sig.Year)
	}
	if sig.Actor != "Richard Pryor" {
		t.Fatalf("actor=%q", sig.Actor)
	}
	if !sig.HasAny() {
		t.Fatal("expected HasAny")
	}
}

func TestExtractFileSignals_BareYear(t *testing.T) {
	sig := ExtractFileSignals(context.Background(), "A.Beautiful.Mind.2001.1080p.BluRay", "", nil)
	if sig.Year != 2001 {
		t.Fatalf("year=%d", sig.Year)
	}
}

func TestSignalsPass_YearExact(t *testing.T) {
	sig := FileSignals{Year: 2001}
	if !SignalsPass(sig, 2001, 0, nil, 5) {
		t.Fatal("exact year should pass")
	}
	if SignalsPass(sig, 2002, 0, nil, 5) {
		t.Fatal("wrong year should fail")
	}
}

func TestSignalsPass_ActorCast(t *testing.T) {
	sig := FileSignals{Actor: "Richard Pryor"}
	if !SignalsPass(sig, 0, 0, []string{"Someone", "Richard Pryor"}, 5) {
		t.Fatal("cast hit should pass")
	}
	if SignalsPass(sig, 0, 0, []string{"Someone Else"}, 5) {
		t.Fatal("cast miss should fail")
	}
}

func TestSignalsPass_DurationTolerance(t *testing.T) {
	sig := FileSignals{DurationSec: 100 * 60} // 100 min
	if !SignalsPass(sig, 0, 100, nil, 5) {
		t.Fatal("exact duration should pass")
	}
	if !SignalsPass(sig, 0, 100, nil, 5) {
		t.Fatal("dup")
	}
	// 6% off with 5% tol
	sig.DurationSec = 106 * 60
	if SignalsPass(sig, 0, 100, nil, 5) {
		t.Fatal("outside ±5% should fail")
	}
	if !SignalsPass(sig, 0, 100, nil, 10) {
		t.Fatal("within ±10% should pass")
	}
}

func TestSignalsPass_NoSignals(t *testing.T) {
	if SignalsPass(FileSignals{}, 2001, 100, nil, 5) {
		t.Fatal("empty signals must not pass")
	}
}

type stubProber struct{ d float64 }

func (s stubProber) Probe(context.Context, string) (*mediainfo.Probe, error) {
	return &mediainfo.Probe{Duration: s.d}, nil
}

func TestExtractFileSignals_Duration(t *testing.T) {
	sig := ExtractFileSignals(context.Background(), "Movie (2001)", "/x.mkv", stubProber{d: 7200})
	if sig.DurationSec != 7200 || sig.Year != 2001 {
		t.Fatalf("%+v", sig)
	}
}

func TestMatchConfigNormalize(t *testing.T) {
	c := MatchConfig{CandidateN: 0, DurationTolerancePct: -1}.Normalize()
	if c.CandidateN != DefaultCandidateN || c.DurationTolerancePct != DefaultDurationTolerancePct {
		t.Fatalf("%+v", c)
	}
	exact := MatchConfig{CandidateN: 3, DurationTolerancePct: 0}.Normalize()
	if exact.CandidateN != 3 || exact.DurationTolerancePct != 0 {
		t.Fatalf("0%% tolerance must remain exact: %+v", exact)
	}
}

func TestExtractFileSignals_SeriesEpisodeName(t *testing.T) {
	sig := ExtractFileSignals(context.Background(), "Show.Name.S01E01.mkv", "/tmp/Show.Name.S01E01.mkv", nil)
	t.Logf("%+v HasAny=%v", sig, sig.HasAny())
	if sig.HasAny() {
		t.Fatalf("episode name without year/actor/duration should have no signals: %+v", sig)
	}
}
