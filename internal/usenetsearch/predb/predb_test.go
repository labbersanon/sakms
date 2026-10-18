package predb

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/dbtest"
)

func TestParseAnnounce(t *testing.T) {
	cases := []struct {
		line   string
		kind   string
		title  string
		cat    string
		size   int64
		files  int
		nuked  bool
		reason string
		ok     bool
	}{
		{line: "NEW: [MOVIES-X264] Some.Movie.2021.1080p.BluRay.x264-GRP", kind: "NEW", title: "Some.Movie.2021.1080p.BluRay.x264-GRP", cat: "MOVIES-X264", ok: true},
		{line: "\x02\x0304NEW:\x0f [TV-X264] Some.Show.S01E02.720p.HDTV.x264-GRP [700MB - 12F]", kind: "NEW", title: "Some.Show.S01E02.720p.HDTV.x264-GRP", cat: "TV-X264", size: 700 << 20, files: 12, ok: true},
		{line: "NUKE: Some.Movie.2021.1080p.BluRay.x264-GRP [dupe.2021-05-01]", kind: "NUKE", title: "Some.Movie.2021.1080p.BluRay.x264-GRP", nuked: true, reason: "dupe.2021-05-01", ok: true},
		{line: "UNNUKE: Some.Movie.2021.1080p.BluRay.x264-GRP [fixed]", kind: "UNNUKE", title: "Some.Movie.2021.1080p.BluRay.x264-GRP", reason: "fixed", ok: true},
		{line: "hello channel, anyone here?"},
		{line: "NEW: [MOVIES] short"},
		{line: "INFO: [2026-10-18 22:13:34 This message is to confirm I am still active.]"},
		{
			line: "[DT: 2026-10-18 22:13:34] [TT: Some.Show.S01E02.720p.HDTV.x264-GRP] [SC: #a.b.teevee] [CT: TV-X264] [RQ: N/A] [SZ: 700MB] [FL: 12] [FN: N/A]",
			kind: "NEW", title: "Some.Show.S01E02.720p.HDTV.x264-GRP", cat: "TV-X264", size: 700 << 20, files: 12, ok: true,
		},
		{
			line: "[DT: 2026-10-18 22:13:34] [TT: Some.Movie.2021.1080p.BluRay.x264-GRP] [SC: srrdb] [CT: MOVIES] [RQ: N/A] [SZ: N/A] [FL: N/A] [NUKED: dupe.2021-05-01]",
			kind: "NUKE", title: "Some.Movie.2021.1080p.BluRay.x264-GRP", cat: "MOVIES", nuked: true, reason: "dupe.2021-05-01", ok: true,
		},
	}
	for _, c := range cases {
		a, ok := ParseAnnounce(c.line)
		if ok != c.ok {
			t.Fatalf("%q ok=%v want %v", c.line, ok, c.ok)
		}
		if !ok {
			continue
		}
		if a.Kind != c.kind || a.Entry.Title != c.title || a.Entry.Category != c.cat || a.Entry.SizeBytes != c.size ||
			a.Entry.Files != c.files || a.Entry.Nuked != c.nuked || a.Reason != c.reason {
			t.Fatalf("%q = %+v", c.line, a)
		}
	}
}

func newStore(t *testing.T) *Store {
	t.Helper()
	return NewStore(dbtest.New(t))
}

func TestStore_HashLookupMissCacheAndNoMissHit(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	const title = "Another.Movie.2020.1080p.WEB-DL.x264-GRP"
	m, sh := HashesOf(title)
	if err := s.Upsert(ctx, Entry{Title: title, SizeBytes: 4 << 30, Files: 50}, SourceIRC); err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{m, sh, strings.ToUpper(m)} {
		e, ok, err := s.ByHash(ctx, h)
		if err != nil || !ok || e.Title != title || e.SizeBytes != 4<<30 {
			t.Fatalf("ByHash(%s) = %+v ok=%v err=%v", h, e, ok, err)
		}
	}
	if _, ok, _ := s.ByHash(ctx, "abc"); ok {
		t.Fatal("bad-length hash must not match")
	}
	if err := s.RecordMiss(ctx, "Nope.2020.1080p-X"); err != nil {
		t.Fatal(err)
	}
	if miss, _ := s.RecentMiss(ctx, "Nope.2020.1080p-X", MissTTL); !miss {
		t.Fatal("fresh miss not honored")
	}
	if miss, _ := s.RecentMiss(ctx, "Nope.2020.1080p-X", -time.Second); miss {
		t.Fatal("expired miss still honored")
	}
	if _, ok, _ := s.ByTitle(ctx, "Nope.2020.1080p-X"); ok {
		t.Fatal("a cached miss must never be a hit")
	}
	// A miss never clobbers a real record.
	if err := s.RecordMiss(ctx, title); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.ByTitle(ctx, title); !ok {
		t.Fatal("RecordMiss overwrote a real record")
	}
	// A real record replaces a miss.
	if err := s.Upsert(ctx, Entry{Title: "Nope.2020.1080p-X"}, SourceHTTP); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.ByTitle(ctx, "Nope.2020.1080p-X"); !ok {
		t.Fatal("upsert did not replace the miss")
	}
}

func TestHTTPClient_HitMissAndFailures(t *testing.T) {
	var calls atomic.Int32
	mode := "hit"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Query().Get("type") != "pre" {
			t.Errorf("type=%q", r.URL.Query().Get("type"))
		}
		switch mode {
		case "hit":
			fmt.Fprint(w, `{"status":"success","results":2,"data":[
{"release":"Some.Movie.2021.1080p.BluRay.x264-GRP.DUBBED","size":1.0,"files":3,"section":"X264-HD","pretime":1,"status":0},
{"release":"some.movie.2021.1080p.bluray.x264-grp","size":4096.5,"files":50,"section":"X264-HD","pretime":1700000000,"status":0}]}`)
		case "miss":
			fmt.Fprint(w, `{"status":"success","results":0,"message":"No results found."}`)
		case "500":
			http.Error(w, "boom", http.StatusInternalServerError)
		case "malformed":
			fmt.Fprint(w, `<html>not json`)
		case "errstatus":
			fmt.Fprint(w, `{"status":"error","message":"rate limited"}`)
		}
	}))
	defer srv.Close()
	h := NewHTTPClient(srv.URL)
	h.Interval = 30 * time.Millisecond
	ctx := context.Background()

	e, found, err := h.Lookup(ctx, "Some.Movie.2021.1080p.BluRay.x264-GRP")
	if err != nil || !found || e.Title != "some.movie.2021.1080p.bluray.x264-grp" || e.SizeBytes != int64(4096.5*(1<<20)) || e.Files != 50 {
		t.Fatalf("hit = %+v found=%v err=%v (exact filter must skip the .DUBBED neighbor)", e, found, err)
	}
	mode = "miss"
	if _, found, err := h.Lookup(ctx, "X.2021.1080p-Y"); err != nil || found {
		t.Fatalf("miss found=%v err=%v", found, err)
	}
	for _, m := range []string{"500", "malformed", "errstatus"} {
		mode = m
		if _, found, err := h.Lookup(ctx, "X.2021.1080p-Y"); err == nil || found {
			t.Fatalf("%s: want error and no hit (found=%v err=%v)", m, found, err)
		}
	}
	if _, _, err := NewHTTPClient("").Lookup(ctx, "x"); err != ErrDisabled {
		t.Fatalf("empty url err = %v, want ErrDisabled", err)
	}
}

func TestHTTPClient_RateLimiterSpacesRequests(t *testing.T) {
	var mu sync.Mutex
	var stamps []time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		stamps = append(stamps, time.Now())
		mu.Unlock()
		fmt.Fprint(w, `{"status":"success","results":0}`)
	}))
	defer srv.Close()
	h := NewHTTPClient(srv.URL)
	h.Interval = 120 * time.Millisecond
	for i := 0; i < 3; i++ {
		if _, _, err := h.Lookup(context.Background(), fmt.Sprintf("n%d.2021.1080p-x", i)); err != nil {
			t.Fatal(err)
		}
	}
	for i := 1; i < len(stamps); i++ {
		if gap := stamps[i].Sub(stamps[i-1]); gap < 100*time.Millisecond {
			t.Fatalf("requests %d/%d only %v apart, want >= ~120ms", i-1, i, gap)
		}
	}
	if MinHTTPInterval < 2*time.Second {
		t.Fatalf("default interval %v exceeds the 30 req/min provider limit", MinHTTPInterval)
	}
}

func TestRandomNick_LetterPrefixedAlnum(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 8; i++ {
		n, err := RandomNick()
		if err != nil {
			t.Fatal(err)
		}
		if len(n) != ircNickLen {
			t.Fatalf("len=%d want %d (%q)", len(n), ircNickLen, n)
		}
		if n[0] < 'A' || (n[0] > 'Z' && n[0] < 'a') || n[0] > 'z' {
			t.Fatalf("nick %q must start with a letter", n)
		}
		for _, r := range n {
			if r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' {
				continue
			}
			t.Fatalf("nick %q has non-alnum %q", n, r)
		}
		if seen[n] {
			t.Fatalf("duplicate nick %q", n)
		}
		seen[n] = true
	}
}

func TestIRCClient_ConnectsIdentifiesJoinsParsesAndReconnects(t *testing.T) {
	const pw = "s3cr3t-nickserv-pw"
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	var mu sync.Mutex
	var seen []string
	var conns atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			n := conns.Add(1)
			go func(c net.Conn, n int32) {
				defer c.Close()
				r := bufio.NewReader(c)
				for {
					c.SetReadDeadline(time.Now().Add(3 * time.Second))
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					line = strings.TrimRight(line, "\r\n")
					mu.Lock()
					seen = append(seen, line)
					mu.Unlock()
					switch {
					case strings.HasPrefix(line, "USER "):
						fmt.Fprint(c, ":srv 001 pbot :Welcome\r\n")
					case strings.HasPrefix(line, "JOIN "):
						fmt.Fprint(c, "PING :tick\r\n")
					case strings.HasPrefix(line, "PONG "):
						if n == 1 {
							fmt.Fprint(c, ":pre!x@y PRIVMSG #pre :\x0303NEW:\x0f [MOVIES] Fed.Movie.2022.1080p.WEB.h264-GRP [3GB - 40F]\r\n")
							fmt.Fprint(c, ":pre!x@y PRIVMSG #pre :chatter that is not an announce\r\n")
							time.Sleep(150 * time.Millisecond)
							return // drop -> client must reconnect
						}
					}
				}
			}(c, n)
		}
	}()

	store := newStore(t)
	cl := &IRCClient{
		Cfg:     IRCConfig{Server: "x", Nick: "pbot", Password: pw, Channels: []string{"#pre"}},
		Store:   store,
		Backoff: 20 * time.Millisecond,
		Dial: func(ctx context.Context) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", ln.Addr().String())
		},
	}
	var logBuf bytes.Buffer
	orig := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(orig)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { cl.Run(ctx); close(done) }()

	deadline := time.Now().Add(4 * time.Second)
	for conns.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if conns.Load() < 2 {
		t.Fatalf("client did not reconnect after the drop (conns=%d)", conns.Load())
	}
	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done

	e, ok, err := store.ByTitle(context.Background(), "Fed.Movie.2022.1080p.WEB.h264-GRP")
	if err != nil || !ok || e.Category != "MOVIES" || e.SizeBytes != 3<<30 || e.Files != 40 {
		t.Fatalf("irc entry = %+v ok=%v err=%v", e, ok, err)
	}
	mu.Lock()
	joined := strings.Join(seen, "\n")
	mu.Unlock()
	for _, want := range []string{"NICK pbot", "PRIVMSG NickServ :IDENTIFY " + pw, "JOIN #pre", "PONG :tick"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("server never saw %q; saw:\n%s", want, joined)
		}
	}
	logs := logBuf.String()
	if strings.Contains(logs, pw) {
		t.Fatalf("NickServ password leaked into logs:\n%s", logs)
	}
	for _, want := range []string{"predb irc connected", "predb irc disconnected parsed=1"} {
		if !strings.Contains(logs, want) {
			t.Fatalf("logs missing %q:\n%s", want, logs)
		}
	}
	if strings.Contains(logs, "Fed.Movie") {
		t.Fatalf("message bodies must not be logged:\n%s", logs)
	}
}

func TestIRCClient_DialErrorNeverLeaksPassword(t *testing.T) {
	const pw = "another-secret-value"
	var logBuf bytes.Buffer
	orig := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(orig)
	cl := &IRCClient{
		Cfg:     IRCConfig{Server: "x", Nick: "n", Password: pw},
		Store:   newStore(t),
		Backoff: 10 * time.Millisecond,
		Dial:    func(context.Context) (net.Conn, error) { return nil, fmt.Errorf("dial failed using %s", pw) },
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	cl.Run(ctx)
	if strings.Contains(logBuf.String(), pw) {
		t.Fatalf("password leaked:\n%s", logBuf.String())
	}
}

func TestIRCClient_RandomNickHeartbeatNoNickServ(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	var mu sync.Mutex
	var seen []string
	gotPing := make(chan struct{}, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		r := bufio.NewReader(c)
		sentLine := false
		for {
			c.SetReadDeadline(time.Now().Add(3 * time.Second))
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			mu.Lock()
			seen = append(seen, line)
			mu.Unlock()
			switch {
			case strings.HasPrefix(line, "USER "):
				fmt.Fprint(c, ":srv 001 guest :Welcome\r\n")
			case strings.HasPrefix(line, "PING :sakms"):
				select {
				case gotPing <- struct{}{}:
				default:
				}
				if !sentLine {
					sentLine = true
					fmt.Fprint(c, ":pre!x@y PRIVMSG #PreNNTmux :[DT: 2026-10-18 22:13:34] [TT: Heart.Beat.2022.1080p.WEB.h264-GRP] [SC: srrdb] [CT: MOVIES] [RQ: N/A] [SZ: 2GB] [FL: 20]\r\n")
				}
			}
		}
	}()

	store := newStore(t)
	cl := &IRCClient{
		Cfg:       IRCConfig{Server: "x", Channels: []string{"#PreNNTmux"}},
		Store:     store,
		Heartbeat: 40 * time.Millisecond,
		Backoff:   time.Hour,
		Dial: func(ctx context.Context) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", ln.Addr().String())
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { cl.Run(ctx); close(done) }()

	select {
	case <-gotPing:
	case <-time.After(2 * time.Second):
		t.Fatal("client never sent heartbeat PING :sakms")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if e, ok, _ := store.ByTitle(context.Background(), "Heart.Beat.2022.1080p.WEB.h264-GRP"); ok && e.Category == "MOVIES" && e.Files == 20 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done

	e, ok, err := store.ByTitle(context.Background(), "Heart.Beat.2022.1080p.WEB.h264-GRP")
	if err != nil || !ok || e.Category != "MOVIES" || e.SizeBytes != 2<<30 || e.Files != 20 {
		t.Fatalf("irc entry = %+v ok=%v err=%v", e, ok, err)
	}
	if cl.Cfg.Nick == "" || len(cl.Cfg.Nick) != ircNickLen {
		t.Fatalf("generated nick %q", cl.Cfg.Nick)
	}
	mu.Lock()
	joined := strings.Join(seen, "\n")
	mu.Unlock()
	if strings.Contains(joined, "NickServ") {
		t.Fatalf("NickServ IDENTIFY must not run without a password:\n%s", joined)
	}
	if !strings.Contains(joined, "JOIN #PreNNTmux") {
		t.Fatalf("missing JOIN:\n%s", joined)
	}
}
