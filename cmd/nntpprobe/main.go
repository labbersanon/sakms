// Command nntpprobe measures whether a provider subscription can support the
// planned native Usenet header index (see .omc/plans/usenet-nntp-native-backend.md §8.1).
//
// Claude 2026-09-17: spike-only tool; not wired into the app.
// Reason: SEARCH is not an NNTP command; discovery cost is OVER crawl throughput
// and obfuscation rate, which must be measured against real groups before any
// product surface ships.
// Troubleshooting: refuse to invent capability claims; print numbers or fail.
// Review if: the spike decision gate in the plan is closed (pass or drop).
//
// Usage (credentials via flags/env only — never reads sakms.db):
//
//	NNTP_USER=… NNTP_PASS=… go run ./cmd/nntpprobe \
//	  -host news.eweka.nl -port 563 -tls \
//	  -group alt.binaries.movies \
//	  -range 100000 -conns 2 -sample 50000
package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Tensai75/nntp"
)

func main() {
	host := flag.String("host", envOr("NNTP_HOST", "news.eweka.nl"), "NNTP host")
	port := flag.Int("port", envInt("NNTP_PORT", 563), "NNTP port")
	useTLS := flag.Bool("tls", envBool("NNTP_TLS", true), "use TLS")
	user := flag.String("user", os.Getenv("NNTP_USER"), "username (or NNTP_USER)")
	pass := flag.String("pass", os.Getenv("NNTP_PASS"), "password (or NNTP_PASS)")
	groupsFlag := flag.String("group", "", "comma-separated groups to probe (required)")
	rangeN := flag.Int("range", 100000, "OVER article count from high watermark backward")
	conns := flag.Int("conns", 2, "parallel OVER connections (default 2 = live-budget world; try 12 for MaxConns=50 crawl share)")
	chunk := flag.Int("chunk", 2000, "OVER chunk size (article numbers per request)")
	sample := flag.Int("sample", 50000, "max overview rows to subject-classify (0 = skip)")
	flag.Parse()

	if strings.TrimSpace(*groupsFlag) == "" {
		fmt.Fprintln(os.Stderr, "nntpprobe: -group is required (manual group list; no auto-discovery)")
		os.Exit(2)
	}
	if *conns < 1 {
		fmt.Fprintln(os.Stderr, "nntpprobe: -conns must be >= 1")
		os.Exit(2)
	}
	groups := splitCSV(*groupsFlag)

	fmt.Printf("nntpprobe: host=%s port=%d tls=%v conns=%d range=%d chunk=%d\n",
		*host, *port, *useTLS, *conns, *rangeN, *chunk)

	c0, err := dial(*host, *port, *useTLS, *user, *pass)
	if err != nil {
		fail("dial/auth", err)
	}
	caps, err := c0.Capabilities()
	if err != nil {
		fmt.Printf("CAPABILITIES: error: %v\n", err)
	} else {
		fmt.Printf("CAPABILITIES (%d):\n", len(caps))
		for _, line := range caps {
			fmt.Printf("  %s\n", line)
		}
		fmt.Printf("  has OVER/XOVER family: %v\n", hasCap(caps, "OVER", "XOVER"))
		fmt.Printf("  has HDR/XHDR: %v\n", hasCap(caps, "HDR", "XHDR"))
		fmt.Printf("  has SEARCH: %v (expected false — not in RFC 3977)\n", hasCap(caps, "SEARCH"))
	}
	_ = c0.Quit()

	for _, group := range groups {
		if err := probeGroup(*host, *port, *useTLS, *user, *pass, group, *rangeN, *chunk, *conns, *sample); err != nil {
			fail("group "+group, err)
		}
	}
}

func probeGroup(host string, port int, useTLS bool, user, pass, group string, rangeN, chunk, conns, sample int) error {
	c, err := dial(host, port, useTLS, user, pass)
	if err != nil {
		return err
	}
	defer c.Quit()

	number, low, high, err := c.Group(group)
	if err != nil {
		return fmt.Errorf("GROUP %s: %w", group, err)
	}
	fmt.Printf("\nGROUP %s: number=%d low=%d high=%d span=%d\n", group, number, low, high, high-low+1)
	if high < low {
		return fmt.Errorf("GROUP %s: empty or inverted range", group)
	}

	from := high - rangeN + 1
	if from < low {
		from = low
	}
	to := high
	totalArts := to - from + 1
	fmt.Printf("OVER range %d..%d (%d articles) with %d conn(s), chunk=%d\n", from, to, totalArts, conns, chunk)

	type job struct{ a, b int }
	jobs := make(chan job, conns*2)
	var (
		wg           sync.WaitGroup
		rowsTotal    atomic.Int64
		bytesApprox  atomic.Int64
		pipeErrs     atomic.Int64
		otherErrs    atomic.Int64
		sampleMu     sync.Mutex
		sampleLeft   = sample
		usable       int
		obfuscated   int
		unparsed     int
		firstErr     atomic.Value
	)

	worker := func() {
		defer wg.Done()
		wc, err := dial(host, port, useTLS, user, pass)
		if err != nil {
			otherErrs.Add(1)
			firstErr.CompareAndSwap(nil, err)
			for range jobs {
			}
			return
		}
		defer wc.Quit()
		if _, _, _, err := wc.Group(group); err != nil {
			otherErrs.Add(1)
			firstErr.CompareAndSwap(nil, err)
			for range jobs {
			}
			return
		}
		for j := range jobs {
			ovs, err := wc.Overview(j.a, j.b)
			if err != nil {
				msg := err.Error()
				if strings.Contains(msg, "broken pipe") || strings.Contains(msg, "connection reset") {
					pipeErrs.Add(1)
				} else {
					otherErrs.Add(1)
				}
				firstErr.CompareAndSwap(nil, err)
				continue
			}
			rowsTotal.Add(int64(len(ovs)))
			for _, ov := range ovs {
				// Rough on-disk row cost: subject + msgid + fixed columns.
				bytesApprox.Add(int64(len(ov.Subject) + len(ov.MessageId) + len(ov.From) + 64))
			}
			if sample <= 0 {
				continue
			}
			sampleMu.Lock()
			for _, ov := range ovs {
				if sampleLeft <= 0 {
					break
				}
				sampleLeft--
				switch classifySubject(ov.Subject) {
				case classUsable:
					usable++
				case classObfuscated:
					obfuscated++
				default:
					unparsed++
				}
			}
			sampleMu.Unlock()
		}
	}

	start := time.Now()
	wg.Add(conns)
	for i := 0; i < conns; i++ {
		go worker()
	}
	for a := from; a <= to; a += chunk {
		b := a + chunk - 1
		if b > to {
			b = to
		}
		jobs <- job{a, b}
	}
	close(jobs)
	wg.Wait()
	elapsed := time.Since(start)

	rows := rowsTotal.Load()
	approxBytes := bytesApprox.Load()
	artsPerSec := float64(0)
	if elapsed.Seconds() > 0 {
		artsPerSec = float64(rows) / elapsed.Seconds()
	}
	var bytesPerRow float64
	if rows > 0 {
		bytesPerRow = float64(approxBytes) / float64(rows)
	}

	fmt.Printf("OVER done in %s\n", elapsed.Round(time.Millisecond))
	fmt.Printf("  rows=%d arts/sec=%.0f approx_index_bytes=%d (~%.1f B/row)\n",
		rows, artsPerSec, approxBytes, bytesPerRow)
	fmt.Printf("  broken_pipe_or_reset=%d other_errors=%d\n", pipeErrs.Load(), otherErrs.Load())
	if v := firstErr.Load(); v != nil {
		fmt.Printf("  first_error: %v\n", v)
	}

	// Extrapolations the plan's decision gate needs.
	const windowDays = 14
	// Without a 24h delta we cannot know articles/day; report span-based ceiling.
	span := high - low + 1
	fmt.Printf("  extrapolate (needs a second run 24h later for true articles/day):\n")
	fmt.Printf("    group_span=%d; if span≈retention, rough arts/day ≈ span/retention_days (operator must measure)\n", span)
	if rows > 0 && bytesPerRow > 0 {
		// Assume crawled window equals -range for a lower bound.
		est14d := float64(rows) * (float64(windowDays) / 1.0) // placeholder: 1-day sample = this range is NOT 1 day
		_ = est14d
		fmt.Printf("    this_run_index≈%.1f MiB for %d rows; at %.0f B/row, 20 GiB holds ~%.0f M rows\n",
			float64(approxBytes)/(1024*1024), rows, bytesPerRow, (20.0*1024*1024*1024)/bytesPerRow/1e6)
	}

	if sample > 0 {
		classified := usable + obfuscated + unparsed
		fmt.Printf("  subject sample (n=%d): usable=%d (%.1f%%) obfuscated=%d (%.1f%%) other=%d (%.1f%%)\n",
			classified,
			usable, pct(usable, classified),
			obfuscated, pct(obfuscated, classified),
			unparsed, pct(unparsed, classified),
		)
	}
	return nil
}

type subjectClass int

const (
	classOther subjectClass = iota
	classUsable
	classObfuscated
)

var (
	rePartNM   = regexp.MustCompile(`(?i)\(\s*\d+\s*/\s*\d+\s*\)`)
	reYenc     = regexp.MustCompile(`(?i)yEnc`)
	reHexBlob  = regexp.MustCompile(`(?i)^[a-f0-9]{16,}$`)
	reSceneish = regexp.MustCompile(`[A-Za-z0-9]+[\.\-_][A-Za-z0-9.\-_]+`)
)

func classifySubject(subj string) subjectClass {
	s := strings.TrimSpace(subj)
	if s == "" {
		return classOther
	}
	// Strip common yEnc wrappers for the core token check.
	core := s
	if i := strings.Index(strings.ToLower(core), "yenc"); i > 0 {
		core = strings.TrimSpace(core[:i])
	}
	core = strings.Trim(core, "[]() \"'")
	if reHexBlob.MatchString(core) || (len(core) >= 20 && isMostlyHex(core)) {
		return classObfuscated
	}
	if rePartNM.MatchString(s) && (reYenc.MatchString(s) || reSceneish.MatchString(s)) {
		return classUsable
	}
	if reSceneish.MatchString(s) && (strings.Contains(s, ".") || strings.Contains(s, "-")) {
		return classUsable
	}
	return classOther
}

func isMostlyHex(s string) bool {
	hex := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') {
			hex++
		}
	}
	return float64(hex)/float64(len(s)) >= 0.85
}

func dial(host string, port int, useTLS bool, user, pass string) (*nntp.Conn, error) {
	addr := fmt.Sprintf("%s:%d", host, port)
	var (
		c   *nntp.Conn
		err error
	)
	if useTLS {
		c, err = nntp.DialTLS("tcp", addr, &tls.Config{ServerName: host})
	} else {
		c, err = nntp.Dial("tcp", addr)
	}
	if err != nil {
		return nil, err
	}
	_ = c.ModeReader()
	if user != "" {
		if err := c.Authenticate(user, pass); err != nil {
			c.Quit()
			return nil, fmt.Errorf("auth: %w", err)
		}
	}
	return c, nil
}

func hasCap(caps []string, names ...string) bool {
	want := map[string]struct{}{}
	for _, n := range names {
		want[strings.ToUpper(n)] = struct{}{}
	}
	for _, line := range caps {
		fields := strings.Fields(strings.ToUpper(line))
		if len(fields) == 0 {
			continue
		}
		if _, ok := want[fields[0]]; ok {
			return true
		}
	}
	return false
}

func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	var n int
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
		return def
	}
	return n
}

func envBool(k string, def bool) bool {
	v := strings.ToLower(os.Getenv(k))
	switch v {
	case "1", "true", "yes", "y":
		return true
	case "0", "false", "no", "n":
		return false
	default:
		return def
	}
}

func pct(n, d int) float64 {
	if d == 0 {
		return 0
	}
	return 100 * float64(n) / float64(d)
}

func fail(step string, err error) {
	fmt.Fprintf(os.Stderr, "nntpprobe: %s: %v\n", step, err)
	os.Exit(1)
}
