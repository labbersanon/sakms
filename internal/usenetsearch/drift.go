package usenetsearch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/labbersanon/sakms/internal/settings"
)

// Claude 2026-10-16: AI-maintained extra RE2 collection patterns (plan 1C/2A/3B).
// Reason: ParseSubject stays deterministic at ingest; a periodic idle-loop pass
//   samples live split clusters and unseen NNTmux seeder regexes, asks ChatJSON
//   for one RE2 strip_prefix or lead_token, and publishes only if fixturesHold.
// Troubleshooting: o2cli grep "collection drift"; no-op if AI is nil or
//   usenet_nntp_collection_drift_enabled=false. Extra patterns do not kick a
//   header rewrite (new ingest only).
// Review if: a v3 rewrite should re-parse stored headers with extras.
// Related files: fixtures.go, subject.go, docs/usenet-nntp-native.md

const (
	extraStripPrefix extraKind = "strip_prefix"
	extraLeadToken   extraKind = "lead_token"

	maxExtraPatterns   = 48
	maxExtraPatternLen = 200
	maxDriftSubjects   = 8
	seederCacheTTL     = 24 * time.Hour
	driftMinInterval   = 5 * time.Minute
	driftChatTimeout   = 30 * time.Second

	nntmuxSeederURL = "https://raw.githubusercontent.com/NNTmux/newznab-tmux/master/database/seeders/CollectionRegexesTableSeeder.php"
)

type extraKind string

type extraPattern struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Pattern string `json:"pattern"`
	Source  string `json:"source"`
	Reason  string `json:"reason,omitempty"`
}

type compiledExtra struct {
	extraPattern
	re *regexp.Regexp
}

var liveExtra atomic.Pointer[[]compiledExtra]

func extraPatternsLive() []compiledExtra {
	p := liveExtra.Load()
	if p == nil {
		return nil
	}
	return *p
}

func publishExtra(extra []compiledExtra) {
	cp := append([]compiledExtra(nil), extra...)
	liveExtra.Store(&cp)
}

// DriftAI is ChatJSON without importing identify (wired from api).
type DriftAI interface {
	ChatJSON(ctx context.Context, prompt string) (map[string]any, error)
}

// DriftAIFunc returns the current AI client (nil if fallback is off).
type DriftAIFunc func(ctx context.Context) DriftAI

// DriftFetchFunc GETs the NNTmux seeder (injected so tests skip the network).
type DriftFetchFunc func(ctx context.Context, url string) ([]byte, error)

func (s *Service) SetDriftAI(fn DriftAIFunc) {
	s.mu.Lock()
	s.driftAIFn = fn
	s.mu.Unlock()
}

func (s *Service) SetDriftFetch(fn DriftFetchFunc) {
	s.mu.Lock()
	s.driftFetch = fn
	s.mu.Unlock()
}

func (s *Service) loadExtraPatterns(ctx context.Context) {
	if s.sqlDB == nil {
		return
	}
	publishExtra(compileExtraPatterns(loadStoredExtraPatterns(ctx, settings.New(s.sqlDB))))
}

func loadStoredExtraPatterns(ctx context.Context, st *settings.Store) []extraPattern {
	raw, err := st.Get(ctx, KeyCollectionExtraPatterns)
	if err != nil || strings.TrimSpace(raw) == "" {
		return nil
	}
	var pats []extraPattern
	if err := json.Unmarshal([]byte(raw), &pats); err != nil {
		log.Printf("usenetsearch: collection drift extras json: %v", err)
		return nil
	}
	return pats
}

func compileExtraPatterns(pats []extraPattern) []compiledExtra {
	out := make([]compiledExtra, 0, len(pats))
	seen := map[string]bool{}
	for _, p := range pats {
		c, err := compileOneExtra(p)
		if err != nil {
			log.Printf("usenetsearch: collection drift skip extra: %v", err)
			continue
		}
		body := c.Kind + "\n" + c.Pattern
		if seen[c.ID] || seen[body] {
			continue
		}
		seen[c.ID] = true
		seen[body] = true
		out = append(out, c)
		if len(out) >= maxExtraPatterns {
			break
		}
	}
	return out
}

func compileOneExtra(p extraPattern) (compiledExtra, error) {
	kind := extraKind(strings.TrimSpace(p.Kind))
	if kind != extraStripPrefix && kind != extraLeadToken {
		return compiledExtra{}, fmt.Errorf("unknown extra kind %q", p.Kind)
	}
	pat := strings.TrimSpace(p.Pattern)
	if pat == "" || len(pat) > maxExtraPatternLen {
		return compiledExtra{}, fmt.Errorf("invalid extra pattern length")
	}
	if !patternAnchored(pat) {
		return compiledExtra{}, fmt.Errorf("extra pattern must be anchored at ^")
	}
	re, err := regexp.Compile(pat)
	if err != nil {
		return compiledExtra{}, fmt.Errorf("RE2 compile: %w", err)
	}
	if kind == extraLeadToken && re.NumSubexp() < 1 {
		return compiledExtra{}, fmt.Errorf("lead_token must capture group 1")
	}
	p.Kind = string(kind)
	p.Pattern = pat
	if p.ID == "" {
		p.ID = extraID(kind, pat)
	}
	return compiledExtra{extraPattern: p, re: re}, nil
}

func extraID(kind extraKind, pat string) string {
	sum := sha256.Sum256([]byte(string(kind) + "\n" + pat))
	return "e" + hex.EncodeToString(sum[:6])
}

func patternAnchored(pat string) bool {
	p := pat
	for strings.HasPrefix(p, "(?") {
		end := strings.IndexByte(p, ')')
		if end < 0 {
			break
		}
		p = p[end+1:]
	}
	return strings.HasPrefix(p, "^")
}

func (s *Service) runCollectionDrift(ctx context.Context) {
	s.mu.Lock()
	enabled := s.cfg.CollectionDriftEnabled
	idx := s.idx
	sqlDB := s.sqlDB
	aiFn := s.driftAIFn
	fetch := s.driftFetch
	lastAt := s.driftLastAt
	lastCluster := s.driftLastCluster
	s.mu.Unlock()
	if !enabled || sqlDB == nil || idx == nil {
		return
	}
	if time.Since(lastAt) < driftMinInterval && !lastAt.IsZero() {
		return
	}
	st := settings.New(sqlDB)
	rewriteDone, err := st.GetBool(ctx, KeyCollectionRewriteDone, false)
	if err != nil || !rewriteDone {
		return
	}
	stored := loadStoredExtraPatterns(ctx, st)
	current := compileExtraPatterns(stored)
	publishExtra(current)

	var ai DriftAI
	if aiFn != nil {
		ai = aiFn(ctx)
	}
	if ai == nil {
		return
	}

	cluster, err := idx.sampleSplitCluster(ctx, lastCluster)
	if err != nil {
		log.Printf("usenetsearch: collection drift sample: %v", err)
		return
	}
	if cluster != nil {
		s.markDriftAttempt(cluster.key())
		if clusterMerged(*cluster, current) {
			return
		}
		s.driftOnce(ctx, st, ai, current, stored, driftPromptLive(cluster), cluster, "live:"+cluster.key())
		return
	}
	entry, err := s.nextSeederEntry(ctx, st, fetch)
	if err != nil {
		log.Printf("usenetsearch: collection drift seeder: %v", err)
		return
	}
	if entry == nil {
		return
	}
	s.driftOnce(ctx, st, ai, current, stored, driftPromptSeeder(*entry), nil, "nntmux:"+entry.ID)
}

func (s *Service) markDriftAttempt(clusterKey string) {
	s.mu.Lock()
	s.driftLastCluster = clusterKey
	s.mu.Unlock()
}

func (s *Service) driftOnce(ctx context.Context, st *settings.Store, ai DriftAI, current []compiledExtra, stored []extraPattern, prompt string, cluster *splitCluster, source string) {
	s.mu.Lock()
	s.driftLastAt = time.Now()
	s.mu.Unlock()
	markSeen := func() {
		if id, ok := strings.CutPrefix(source, "nntmux:"); ok {
			_ = markSeederSeen(ctx, st, id)
		}
	}

	chatCtx, cancel := context.WithTimeout(ctx, driftChatTimeout)
	defer cancel()
	resp, err := ai.ChatJSON(chatCtx, prompt)
	if err != nil {
		log.Printf("usenetsearch: collection drift chat: %v", err)
		markSeen()
		return
	}
	action := strings.ToLower(jsonString(resp, "action"))
	if action == "" || action == "skip" || action == "none" {
		log.Printf("usenetsearch: collection drift skip source=%s reason=%s", source, jsonString(resp, "reason"))
		markSeen()
		return
	}
	if action != "add" {
		log.Printf("usenetsearch: collection drift unknown action=%q source=%s", action, source)
		return
	}
	cand := extraPattern{
		Kind:    jsonString(resp, "kind"),
		Pattern: jsonString(resp, "pattern"),
		Source:  source,
		Reason:  jsonString(resp, "reason"),
	}
	compiled, err := compileOneExtra(cand)
	if err != nil {
		log.Printf("usenetsearch: collection drift reject source=%s: %v", source, err)
		markSeen()
		return
	}
	proposed := append(append([]compiledExtra(nil), current...), compiled)
	if ok, why := fixturesHold(proposed); !ok {
		log.Printf("usenetsearch: collection drift reject source=%s fixture=%s", source, why)
		markSeen()
		return
	}
	if cluster != nil && !clusterMerged(*cluster, proposed) {
		log.Printf("usenetsearch: collection drift reject source=%s: cluster still split", source)
		return
	}
	if len(current) >= maxExtraPatterns {
		log.Printf("usenetsearch: collection drift extra cap=%d", maxExtraPatterns)
		return
	}
	stored = append(stored, compiled.extraPattern)
	body, err := json.Marshal(stored)
	if err != nil {
		return
	}
	if err := st.Set(ctx, KeyCollectionExtraPatterns, string(body)); err != nil {
		log.Printf("usenetsearch: collection drift save extras: %v", err)
		return
	}
	publishExtra(proposed)
	markSeen()
	log.Printf("usenetsearch: collection drift applied id=%s kind=%s source=%s", compiled.ID, compiled.Kind, source)
}

func clusterKeyFor(c splitCluster, subj string, extra []compiledExtra) string {
	ps := parseSubjectWith(subj, extra)
	if ps.Name != "" && !ps.Obfuscated {
		return "name:" + ps.Name
	}
	return obfKeyWith(c.Group, c.From, subj, extra)
}

func clusterMerged(c splitCluster, extra []compiledExtra) bool {
	if len(c.Subjects) < 2 {
		return false
	}
	first := clusterKeyFor(c, c.Subjects[0], extra)
	if first == "" {
		return false
	}
	for _, subj := range c.Subjects[1:] {
		if clusterKeyFor(c, subj, extra) != first {
			return false
		}
	}
	return true
}

func jsonString(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return strings.TrimSpace(s)
}

type splitCluster struct {
	Group    string
	From     string
	FileM    int
	Names    []string
	Subjects []string
}

func (c splitCluster) key() string {
	return fmt.Sprintf("%s\n%s\n%d", c.Group, c.From, c.FileM)
}

func (idx *Index) sampleSplitCluster(ctx context.Context, skipKey string) (*splitCluster, error) {
	rows, err := idx.db.QueryContext(ctx, `
SELECT group_name, from_addr, file_m
FROM usenet_nntp_headers
WHERE release_name <> '' AND file_m > 1
GROUP BY group_name, from_addr, file_m
HAVING COUNT(DISTINCT release_name) > 1
ORDER BY COUNT(*) DESC
LIMIT 8`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type hit struct {
		group, from string
		fileM       int
	}
	var hits []hit
	for rows.Next() {
		var h hit
		if err := rows.Scan(&h.group, &h.from, &h.fileM); err != nil {
			return nil, err
		}
		hits = append(hits, h)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, h := range hits {
		c := splitCluster{Group: h.group, From: h.from, FileM: h.fileM}
		if c.key() == skipKey {
			continue
		}
		nameRows, err := idx.db.QueryContext(ctx, `
SELECT DISTINCT release_name FROM usenet_nntp_headers
WHERE group_name=? AND from_addr=? AND file_m=? AND release_name <> ''
LIMIT 12`, h.group, h.from, h.fileM)
		if err != nil {
			return nil, err
		}
		for nameRows.Next() {
			var n string
			if err := nameRows.Scan(&n); err != nil {
				nameRows.Close()
				return nil, err
			}
			c.Names = append(c.Names, n)
		}
		nameRows.Close()
		if err := nameRows.Err(); err != nil {
			return nil, err
		}
		subjRows, err := idx.db.QueryContext(ctx, `
SELECT DISTINCT subject FROM usenet_nntp_headers
WHERE group_name=? AND from_addr=? AND file_m=?
LIMIT ?`, h.group, h.from, h.fileM, maxDriftSubjects)
		if err != nil {
			return nil, err
		}
		for subjRows.Next() {
			var subj string
			if err := subjRows.Scan(&subj); err != nil {
				subjRows.Close()
				return nil, err
			}
			c.Subjects = append(c.Subjects, subj)
		}
		subjRows.Close()
		if err := subjRows.Err(); err != nil {
			return nil, err
		}
		if len(c.Subjects) >= 2 && len(c.Names) >= 2 {
			return &c, nil
		}
	}
	return nil, nil
}

func driftPromptLive(c *splitCluster) string {
	var b strings.Builder
	b.WriteString(`You maintain sakms NNTP collection grouping. ParseSubject is deterministic (Go RE2). These subjects from one poster/file-count currently parse to DIFFERENT collection keys and should share one key.

`)
	for i, subj := range c.Subjects {
		key := clusterKeyFor(*c, subj, extraPatternsLive())
		fmt.Fprintf(&b, "Subject %d (key %s):\n%s\n\n", i+1, key, subj)
	}
	b.WriteString(driftPromptRules())
	return b.String()
}

func driftPromptSeeder(e seederEntry) string {
	return fmt.Sprintf(`You convert one NNTmux collection_regexes PCRE into a sakms extra grouping pattern. sakms cannot run PCRE; only group-agnostic RE2 strip_prefix or lead_token is allowed.

NNTmux group_regex: %s
NNTmux regex: %s

Skip unless this is a reusable wrapper prefix or a 16+ character hash-token capture that sakms generic stem still misses. Do not copy group-specific title captures.

%s`, e.Group, e.Regex, driftPromptRules())
}

func driftPromptRules() string {
	return `Kinds:
- strip_prefix: anchored at ^; replaced with empty on the pre-yEnc stem (after [Group]/[#a.b.*] tags).
- lead_token: anchored at ^; capture group 1 is a 16+ char obfuscated token used as the skeleton.

Respond with ONLY JSON:
{"action":"add","kind":"strip_prefix"|"lead_token","pattern":"...","reason":"..."}
or {"action":"skip","reason":"..."}.

Go RE2: no lookahead, no backreferences. Pattern length <= 200. Must not change golden scene stems (R.J.Decker, Bhool.Bhulaiyaa, [#a.b.teevee] Show.S01E01, ah63jk hash tokens). Prefer skip over a risky pattern.`
}

type seederEntry struct {
	ID    string `json:"id"`
	Group string `json:"group"`
	Regex string `json:"regex"`
}

type seederCache struct {
	FetchedAt int64         `json:"fetchedAt"`
	Entries   []seederEntry `json:"entries"`
}

func (s *Service) nextSeederEntry(ctx context.Context, st *settings.Store, fetch DriftFetchFunc) (*seederEntry, error) {
	cache, err := loadSeederCache(ctx, st)
	if err != nil {
		return nil, err
	}
	stale := cache.FetchedAt == 0 || time.Since(time.Unix(cache.FetchedAt, 0)) > seederCacheTTL
	if stale {
		if fetch == nil {
			return nil, nil
		}
		body, err := fetch(ctx, nntmuxSeederURL)
		if err != nil {
			return nil, err
		}
		cache = seederCache{FetchedAt: time.Now().Unix(), Entries: parseSeederPHP(body)}
		raw, _ := json.Marshal(cache)
		if err := st.Set(ctx, KeyCollectionSeederCache, string(raw)); err != nil {
			return nil, err
		}
		log.Printf("usenetsearch: collection drift seeder cached entries=%d", len(cache.Entries))
	}
	seen := loadSeederSeen(ctx, st)
	for i := range cache.Entries {
		e := cache.Entries[i]
		if seen[e.ID] {
			continue
		}
		return &e, nil
	}
	return nil, nil
}

func loadSeederCache(ctx context.Context, st *settings.Store) (seederCache, error) {
	raw, err := st.Get(ctx, KeyCollectionSeederCache)
	if errors.Is(err, settings.ErrNotFound) || strings.TrimSpace(raw) == "" {
		return seederCache{}, nil
	}
	if err != nil {
		return seederCache{}, err
	}
	var c seederCache
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return seederCache{}, nil
	}
	return c, nil
}

func loadSeederSeen(ctx context.Context, st *settings.Store) map[string]bool {
	out := map[string]bool{}
	raw, err := st.Get(ctx, KeyCollectionSeederSeen)
	if err != nil || raw == "" {
		return out
	}
	var ids []string
	if json.Unmarshal([]byte(raw), &ids) != nil {
		return out
	}
	for _, id := range ids {
		out[id] = true
	}
	return out
}

func markSeederSeen(ctx context.Context, st *settings.Store, id string) error {
	if id == "" {
		return nil
	}
	seen := loadSeederSeen(ctx, st)
	if seen[id] {
		return nil
	}
	ids := make([]string, 0, len(seen)+1)
	for k := range seen {
		ids = append(ids, k)
	}
	ids = append(ids, id)
	raw, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	return st.Set(ctx, KeyCollectionSeederSeen, string(raw))
}

var reSeederPair = regexp.MustCompile(`(?s)'group_regex'\s*=>\s*'((?:\\.|[^'\\])*)'.*?'regex'\s*=>\s*'((?:\\.|[^'\\])*)'`)

func parseSeederPHP(body []byte) []seederEntry {
	matches := reSeederPair.FindAllSubmatch(body, 5000)
	out := make([]seederEntry, 0, len(matches))
	seen := map[string]bool{}
	for _, m := range matches {
		if len(m) < 3 {
			continue
		}
		g := phpUnquote(string(m[1]))
		r := phpUnquote(string(m[2]))
		if r == "" {
			continue
		}
		sum := sha256.Sum256([]byte(g + "\n" + r))
		id := hex.EncodeToString(sum[:8])
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, seederEntry{ID: id, Group: g, Regex: r})
	}
	return out
}

func phpUnquote(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, `\\`, `\`), `\'`, `'`)
}
