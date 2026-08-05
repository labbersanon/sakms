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
	if SignalsPass(sig, 0, 0, nil, 5) {
		t.Fatal("unknown candidate year must not count as corroboration")
	}
}

func TestSignalsPass_YearMismatchDurationOverride(t *testing.T) {
	// Wrong filename year, duration within ±5% → pass
	sig := FileSignals{Year: 2007, DurationSec: 97 * 60}
	if !SignalsPass(sig, 1986, 97, nil, 5) {
		t.Fatal("duration should override wrong year")
	}
	// Actor alone must not override year
	sig = FileSignals{Year: 2007, Actor: "Richard Pryor"}
	if SignalsPass(sig, 1986, 0, []string{"Richard Pryor"}, 5) {
		t.Fatal("actor must not override wrong year")
	}
	// Wrong year + duration outside tolerance → fail
	sig = FileSignals{Year: 2007, DurationSec: 120 * 60}
	if SignalsPass(sig, 1986, 97, nil, 5) {
		t.Fatal("bad duration must not override year")
	}
}

func TestSignalsPass_YearMatchNotVetoedByDuration(t *testing.T) {
	// Exact year + duration outside ±5% (Austin Powers-style TMDB runtime short)
	sig := FileSignals{Year: 1997, DurationSec: 95 * 60}
	if !SignalsPass(sig, 1997, 89, nil, 5) {
		t.Fatal("exact year must pass even when duration disagrees")
	}
}

func TestSignalsCorroborate_PreferYearOverDurationOverride(t *testing.T) {
	sig := FileSignals{Year: 1947, DurationSec: 92 * 60}
	// Wrong-year remake with matching runtime → Weak
	if got := SignalsCorroborate(sig, 1985, 92, nil, 5); got != CorroborationWeak {
		t.Fatalf("wrong year+duration override: got %v want Weak", got)
	}
	// Correct year → Strong (even if duration disagrees)
	if got := SignalsCorroborate(sig, 1947, 80, nil, 5); got != CorroborationStrong {
		t.Fatalf("exact year: got %v want Strong", got)
	}
}

func pickPreferredCorroboration(ranks []Corroboration) Corroboration {
	var weak Corroboration
	for _, r := range ranks {
		if r == CorroborationStrong {
			return CorroborationStrong
		}
		if r == CorroborationWeak && weak == CorroborationNone {
			weak = CorroborationWeak
		}
	}
	return weak
}

func TestPickPreferredCorroboration_StrongBeatsEarlierWeak(t *testing.T) {
	// Copacabana ordering: 1985 Weak before 1947 Strong
	got := pickPreferredCorroboration([]Corroboration{
		CorroborationNone,
		CorroborationWeak,
		CorroborationNone,
		CorroborationStrong,
	})
	if got != CorroborationStrong {
		t.Fatalf("got %v", got)
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
	sig = FileSignals{Actor: "Richard Pryor Autobiography"}
	if !SignalsPass(sig, 0, 0, []string{"Richard Pryor"}, 5) {
		t.Fatal("credit containing cast name should pass")
	}
	// Year+actor file, credits unavailable: year alone must still corroborate.
	sig = FileSignals{Year: 1977, Actor: "Richard Pryor"}
	if !SignalsPass(sig, 1977, 0, nil, 5) {
		t.Fatal("empty cast must ignore actor when year matches")
	}
}

func TestSignalsPass_DurationTolerance(t *testing.T) {
	sig := FileSignals{DurationSec: 100 * 60} // 100 min
	if !SignalsPass(sig, 0, 100, nil, 5) {
		t.Fatal("exact duration should pass")
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
