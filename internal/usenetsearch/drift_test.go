package usenetsearch

import (
	"context"
	"strings"
	"testing"

	"github.com/labbersanon/sakms/internal/dbtest"
	"github.com/labbersanon/sakms/internal/settings"
)

func TestFixturesHold_EmptyExtras(t *testing.T) {
	ok, why := fixturesHold(nil)
	if !ok {
		t.Fatalf("golden fixtures must hold with no extras: %s", why)
	}
}

func TestCompileOneExtra_RejectsUnanchoredAndPCRE(t *testing.T) {
	if _, err := compileOneExtra(extraPattern{Kind: "strip_prefix", Pattern: `foo`}); err == nil {
		t.Fatal("unanchored pattern must be rejected")
	}
	if _, err := compileOneExtra(extraPattern{Kind: "lead_token", Pattern: `^(?<=x)(y)`}); err == nil {
		t.Fatal("lookbehind must be rejected")
	}
	if _, err := compileOneExtra(extraPattern{Kind: "lead_token", Pattern: `^abc`}); err == nil {
		t.Fatal("lead_token without capture must be rejected")
	}
	if _, err := compileOneExtra(extraPattern{Kind: "strip_prefix", Pattern: `(?i)^\{\d{1,5}\s+of\s+\d{1,5}\}\s*`}); err != nil {
		t.Fatalf("valid strip_prefix: %v", err)
	}
}

func TestExtraStripPrefix_MergesBraceFileCount(t *testing.T) {
	a := `{01 of 22} Show.S01E01.720p.HDTV.x264-GRP - "a.part01.rar" yEnc (1/5)`
	b := `{22 of 22} Show.S01E01.720p.HDTV.x264-GRP - "a.vol00+01.par2" yEnc (1/1)`
	if ParseSubject(a).Name == ParseSubject(b).Name && ParseSubject(a).Name != "" {
		t.Fatal("fixture subjects already share a stem; pick a shape ParseSubject still splits")
	}
	c, err := compileOneExtra(extraPattern{Kind: "strip_prefix", Pattern: `(?i)^\{\d{1,5}\s+of\s+\d{1,5}\}\s*`})
	if err != nil {
		t.Fatal(err)
	}
	extra := []compiledExtra{c}
	ok, why := fixturesHold(extra)
	if !ok {
		t.Fatalf("brace extra broke fixtures: %s", why)
	}
	pa := parseSubjectWith(a, extra)
	pb := parseSubjectWith(b, extra)
	if !pa.OK || pa.Name != pb.Name || pa.Name != "Show.S01E01.720p.HDTV.x264-GRP" {
		t.Fatalf("stems a=%q b=%q", pa.Name, pb.Name)
	}
}

func TestFixturesHold_RejectsStemEatingPattern(t *testing.T) {
	c, err := compileOneExtra(extraPattern{Kind: "strip_prefix", Pattern: `(?i)^(?:\[[0-9]+/[0-9]+\]\s*)?R\.J\.Decker`})
	if err != nil {
		t.Fatal(err)
	}
	ok, why := fixturesHold([]compiledExtra{c})
	if ok {
		t.Fatal("pattern that eats Decker must fail fixtures")
	}
	if !strings.Contains(why, "decker") {
		t.Fatalf("why=%s", why)
	}
}

func TestParseSeederPHP_ExtractsPairs(t *testing.T) {
	php := []byte(`
        [
            'group_regex' => '^alt\\.binaries\\.teevee$',
            'regex' => '/^\\[#a\\.b\\.teevee\\] (?P<name>[\\w.-]+)/i',
            'status' => 1,
        ],
        [
            'group_regex' => '^alt\\.binaries\\.moovee$',
            'regex' => '/^(?P<hash>[a-zA-Z0-9]{16,}) - \\[\\d+\\/\\d+\\]/',
            'status' => 1,
        ],
`)
	got := parseSeederPHP(php)
	if len(got) != 2 {
		t.Fatalf("entries=%d %+v", len(got), got)
	}
	if !strings.Contains(got[0].Group, "teevee") || !strings.Contains(got[0].Regex, "teevee") {
		t.Fatalf("entry0=%+v", got[0])
	}
	if !strings.Contains(got[1].Regex, "hash") {
		t.Fatalf("entry1=%+v", got[1])
	}
}

type scriptedDriftAI struct {
	resp map[string]any
	err  error
	n    int
}

func (s *scriptedDriftAI) ChatJSON(context.Context, string) (map[string]any, error) {
	s.n++
	return s.resp, s.err
}

func TestRunCollectionDrift_AppliesWhenFixturesHold(t *testing.T) {
	sqlDB := dbtest.New(t)
	idx, err := OpenIndex(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	ctx := context.Background()
	const g = "alt.binaries.hdtv.x264"
	err = idx.Upsert(ctx, []HeaderRow{
		{
			Group: g, MsgNum: 1, MsgID: "a@x", From: "p@x", PostedAt: 1, Bytes: 100,
			Subject:     `{01 of 22} Show.S01E01.720p.HDTV.x264-GRP - "a.part01.rar" yEnc (1/5)`,
			ReleaseName: "{01 of 22} Show.S01E01.720p.HDTV.x264-GRP",
			PartN:       1, PartM: 5, FileN: 1, FileM: 22, Filename: "a.part01.rar",
		},
		{
			Group: g, MsgNum: 2, MsgID: "b@x", From: "p@x", PostedAt: 1, Bytes: 100,
			Subject:     `{22 of 22} Show.S01E01.720p.HDTV.x264-GRP - "a.vol00+01.par2" yEnc (1/1)`,
			ReleaseName: "{22 of 22} Show.S01E01.720p.HDTV.x264-GRP",
			PartN:       1, PartM: 1, FileN: 22, FileM: 22, Filename: "a.vol00+01.par2",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	st := settings.New(sqlDB)
	if err := st.SetBool(ctx, KeyCollectionRewriteDone, true); err != nil {
		t.Fatal(err)
	}
	ai := &scriptedDriftAI{resp: map[string]any{
		"action":  "add",
		"kind":    "strip_prefix",
		"pattern": `(?i)^\{\d{1,5}\s+of\s+\d{1,5}\}\s*`,
		"reason":  "brace file counters",
	}}
	s := NewService(nil, sqlDB)
	s.idx = idx
	s.cfg.CollectionDriftEnabled = true
	s.SetDriftAI(func(context.Context) DriftAI { return ai })
	t.Cleanup(func() { publishExtra(nil) })

	s.runCollectionDrift(ctx)
	if ai.n != 1 {
		t.Fatalf("ChatJSON calls=%d", ai.n)
	}
	raw, err := st.Get(ctx, KeyCollectionExtraPatterns)
	if err != nil || !strings.Contains(raw, "strip_prefix") {
		t.Fatalf("extras not saved: %q err=%v", raw, err)
	}
	a := `{01 of 22} Show.S01E01.720p.HDTV.x264-GRP - "a.part01.rar" yEnc (1/5)`
	b := `{22 of 22} Show.S01E01.720p.HDTV.x264-GRP - "a.vol00+01.par2" yEnc (1/1)`
	if ParseSubject(a).Name != ParseSubject(b).Name || ParseSubject(a).Name != "Show.S01E01.720p.HDTV.x264-GRP" {
		t.Fatalf("live extras not published: %q vs %q", ParseSubject(a).Name, ParseSubject(b).Name)
	}
}

func TestRunCollectionDrift_RejectsFixtureBreak(t *testing.T) {
	sqlDB := dbtest.New(t)
	idx, err := OpenIndex(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	ctx := context.Background()
	err = idx.Upsert(ctx, []HeaderRow{
		{Group: "g", MsgNum: 1, MsgID: "a@x", From: "p@x", PostedAt: 1, Bytes: 1,
			Subject:     `{01 of 22} Show.S01E01.720p.HDTV.x264-GRP - "a.rar" yEnc (1/2)`,
			ReleaseName: "one", FileM: 22, PartM: 2},
		{Group: "g", MsgNum: 2, MsgID: "b@x", From: "p@x", PostedAt: 1, Bytes: 1,
			Subject:     `{22 of 22} Show.S01E01.720p.HDTV.x264-GRP - "b.rar" yEnc (1/2)`,
			ReleaseName: "two", FileM: 22, PartM: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	st := settings.New(sqlDB)
	if err := st.SetBool(ctx, KeyCollectionRewriteDone, true); err != nil {
		t.Fatal(err)
	}
	ai := &scriptedDriftAI{resp: map[string]any{
		"action":  "add",
		"kind":    "strip_prefix",
		"pattern": `(?i)^(?:\[[0-9]+/[0-9]+\]\s*)?R\.J\.Decker`,
		"reason":  "bad",
	}}
	s := NewService(nil, sqlDB)
	s.idx = idx
	s.cfg.CollectionDriftEnabled = true
	s.SetDriftAI(func(context.Context) DriftAI { return ai })
	t.Cleanup(func() { publishExtra(nil) })
	s.runCollectionDrift(ctx)
	if _, err := st.Get(ctx, KeyCollectionExtraPatterns); err == nil {
		t.Fatal("fixture-breaking pattern must not be saved")
	}
}

func TestRunCollectionDrift_SeederSkipMarksSeen(t *testing.T) {
	sqlDB := dbtest.New(t)
	idx, err := OpenIndex(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	ctx := context.Background()
	st := settings.New(sqlDB)
	if err := st.SetBool(ctx, KeyCollectionRewriteDone, true); err != nil {
		t.Fatal(err)
	}
	php := []byte(`['group_regex' => '^alt\\.binaries\\.teevee$', 'regex' => '/^\\[#a\\.b\\.teevee\\]/i']`)
	entries := parseSeederPHP(php)
	if len(entries) != 1 {
		t.Fatalf("entries=%d", len(entries))
	}
	ai := &scriptedDriftAI{resp: map[string]any{"action": "skip", "reason": "already covered"}}
	s := NewService(nil, sqlDB)
	s.idx = idx
	s.cfg.CollectionDriftEnabled = true
	s.SetDriftAI(func(context.Context) DriftAI { return ai })
	s.SetDriftFetch(func(context.Context, string) ([]byte, error) { return php, nil })
	s.runCollectionDrift(ctx)
	if ai.n != 1 {
		t.Fatalf("ChatJSON calls=%d", ai.n)
	}
	seen := loadSeederSeen(ctx, st)
	if !seen[entries[0].ID] {
		t.Fatalf("seeder id %s not marked seen: %v", entries[0].ID, seen)
	}
	s.driftLastAt = s.driftLastAt.Add(-driftMinInterval - 1)
	s.runCollectionDrift(ctx)
	if ai.n != 1 {
		t.Fatalf("second pass should not re-prompt seen regex, calls=%d", ai.n)
	}
}

func TestRunCollectionDrift_NoAIIsNoop(t *testing.T) {
	sqlDB := dbtest.New(t)
	idx, err := OpenIndex(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	ctx := context.Background()
	st := settings.New(sqlDB)
	if err := st.SetBool(ctx, KeyCollectionRewriteDone, true); err != nil {
		t.Fatal(err)
	}
	s := NewService(nil, sqlDB)
	s.idx = idx
	s.cfg.CollectionDriftEnabled = true
	s.runCollectionDrift(ctx)
	if _, err := st.Get(ctx, KeyCollectionExtraPatterns); err == nil {
		t.Fatal("nil AI must not write extras")
	}
}
