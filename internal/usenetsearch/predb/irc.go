package predb

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"time"
)

// Default public PRE feed (synIRC). NickServ is not required to join.
const (
	DefaultIRCServer  = "irc.synirc.net"
	DefaultIRCPort    = 6697
	DefaultIRCChannel = "#PreNNTmux"
	// Claude 2026-10-19: second live PRE network (nZEDb IRCScraper "corrupt").
	// Reason: independent of synIRC; public JOIN without a channel key (probed
	//   2026-10-19). Line shape is PRE: [SECTION] Title-GRP.
	// Troubleshooting: no "predb irc connected" for this host → outbound 6697
	//   or the channel went +k/+i; o2cli grep "predb irc".
	// Review if: JOIN returns 473/475/477.
	DefaultCorruptServer  = "irc.corrupt-net.org"
	DefaultCorruptChannel = "#pre"
)

// IRCConfig configures the optional PreDB announce feed. Password is the
// NickServ password (decrypted by the caller; never logged or persisted here).
// Empty Nick is filled with a random password-style identity at Run().
type IRCConfig struct {
	Server   string   `json:"server"`
	Port     int      `json:"port"`
	TLS      bool     `json:"tls"`
	Nick     string   `json:"nick,omitempty"`
	Password string   `json:"-"`
	Channels []string `json:"channels"`
}

type extraFeedJSON struct {
	Server   string   `json:"server"`
	Port     int      `json:"port"`
	TLS      *bool    `json:"tls"`
	Channels []string `json:"channels"`
}

// DefaultExtraIRCFeeds is the Corrupt-net #pre feed.
func DefaultExtraIRCFeeds() []IRCConfig {
	return []IRCConfig{{
		Server:   DefaultCorruptServer,
		Port:     DefaultIRCPort,
		TLS:      true,
		Channels: []string{DefaultCorruptChannel},
	}}
}

// ParseExtraIRCFeeds reads usenet_nntp_predb_irc_extra. Empty uses the Corrupt-net
// default. "[]" / "none" / "-" disables extras. NickServ password is never set here.
func ParseExtraIRCFeeds(raw string) ([]IRCConfig, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultExtraIRCFeeds(), nil
	}
	if raw == "[]" || raw == "-" || strings.EqualFold(raw, "none") {
		return nil, nil
	}
	var in []extraFeedJSON
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		return nil, err
	}
	out := make([]IRCConfig, 0, len(in))
	for _, f := range in {
		cfg := IRCConfig{
			Server: strings.TrimSpace(f.Server),
			Port:   f.Port,
			TLS:    f.TLS == nil || *f.TLS,
		}
		if cfg.Port <= 0 {
			cfg.Port = DefaultIRCPort
		}
		for _, ch := range f.Channels {
			ch = strings.TrimSpace(ch)
			if ch != "" {
				cfg.Channels = append(cfg.Channels, ch)
			}
		}
		if cfg.Server == "" || len(cfg.Channels) == 0 {
			continue
		}
		out = append(out, cfg)
	}
	return out, nil
}

const (
	ircReadTimeout = 15 * time.Minute
	ircMinBackoff  = 5 * time.Second
	ircMaxBackoff  = 5 * time.Minute
	// ircHeartbeat is how often we PING the server so NAT/idle drops do not
	// silently kill the socket (server PINGs alone can be minutes apart).
	ircHeartbeat = 45 * time.Second
	ircNickLen   = 16
	ircNickAlpha = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	ircNickRest  = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
)

// IRCClient is a minimal one-connection IRC client: NICK/USER, optional
// NickServ IDENTIFY, JOIN, server PONG + client heartbeat PING, and
// channel-line parsing into the local Store.
// It never logs message bodies or credentials.
type IRCClient struct {
	Cfg   IRCConfig
	Store *Store

	// Dial overrides the transport (tests); nil dials Cfg over TCP/TLS.
	Dial func(ctx context.Context) (net.Conn, error)
	// Backoff overrides the reconnect delay schedule start (tests).
	Backoff time.Duration
	// Heartbeat overrides ircHeartbeat (tests). Zero uses the default.
	Heartbeat time.Duration

	parsed int
	tagged int
}

// RandomNick returns a 16-character letter-prefixed alphanumeric nick.
// Claude 2026-10-18: unregistered synIRC identity; no NickServ mail.
// Reason: Gmail/iCloud/Proton never received synIRC confirm mail; #PreNNTmux
// allows unregistered JOIN. A high-entropy nick is hard to squat.
// Troubleshooting: 433 nick-in-use → generate another, do not IDENTIFY.
// Review if: the channel bans password-looking nicks (nZEDb scraper warns
// "pick a normal name"); then switch to a word+digits shape.
func RandomNick() (string, error) {
	b := make([]byte, ircNickLen)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[0] = ircNickAlpha[int(b[0])%len(ircNickAlpha)]
	for i := 1; i < len(b); i++ {
		b[i] = ircNickRest[int(b[i])%len(ircNickRest)]
	}
	return string(b), nil
}

func (c *IRCClient) dial(ctx context.Context) (net.Conn, error) {
	var conn net.Conn
	var err error
	if c.Dial != nil {
		conn, err = c.Dial(ctx)
	} else {
		port := c.Cfg.Port
		if port <= 0 {
			port = DefaultIRCPort
		}
		addr := net.JoinHostPort(c.Cfg.Server, fmt.Sprint(port))
		d := &net.Dialer{Timeout: 20 * time.Second, KeepAlive: 30 * time.Second}
		if c.Cfg.TLS {
			conn, err = (&tls.Dialer{NetDialer: d, Config: &tls.Config{ServerName: c.Cfg.Server, MinVersion: tls.VersionTLS12}}).DialContext(ctx, "tcp", addr)
		} else {
			conn, err = d.DialContext(ctx, "tcp", addr)
		}
	}
	if err != nil {
		return nil, err
	}
	setTCPKeepAlive(conn)
	return conn, nil
}

func setTCPKeepAlive(c net.Conn) {
	switch c := c.(type) {
	case *net.TCPConn:
		_ = c.SetKeepAlive(true)
		_ = c.SetKeepAlivePeriod(30 * time.Second)
	case *tls.Conn:
		setTCPKeepAlive(c.NetConn())
	}
}

// Run connects and reconnects (exponential backoff) until ctx is cancelled.
func (c *IRCClient) Run(ctx context.Context) {
	if c.Cfg.Server == "" || c.Store == nil {
		log.Printf("usenetsearch: predb irc disabled (server not configured)")
		return
	}
	if strings.TrimSpace(c.Cfg.Nick) == "" {
		n, err := RandomNick()
		if err != nil {
			log.Printf("usenetsearch: predb irc nick: %v", err)
			return
		}
		c.Cfg.Nick = n
	}
	backoff := c.Backoff
	if backoff <= 0 {
		backoff = ircMinBackoff
	}
	delay := backoff
	for ctx.Err() == nil {
		conn, err := c.dial(ctx)
		if err == nil {
			log.Printf("usenetsearch: predb irc connected server=%s nick=%s", c.Cfg.Server, c.Cfg.Nick)
			c.parsed = 0
			registered, serr := c.session(ctx, conn)
			conn.Close()
			log.Printf("usenetsearch: predb irc disconnected server=%s parsed=%d", c.Cfg.Server, c.parsed)
			if registered {
				delay = backoff
			}
			if serr != nil && ctx.Err() == nil && !errors.Is(serr, errIRCClosed) {
				log.Printf("usenetsearch: predb irc session ended: %v", sanitizeErr(serr, c.Cfg.Password))
			}
		} else if ctx.Err() == nil {
			log.Printf("usenetsearch: predb irc dial failed: %v", sanitizeErr(err, c.Cfg.Password))
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		if delay *= 2; delay > ircMaxBackoff {
			delay = ircMaxBackoff
		}
	}
}

var errIRCClosed = errors.New("irc: connection closed")

func sanitizeErr(err error, secret string) string {
	s := err.Error()
	if secret != "" {
		s = strings.ReplaceAll(s, secret, "***")
	}
	return s
}

// session runs one connection. registered reports whether the server
// completed registration (001), used to reset the reconnect backoff.
func (c *IRCClient) session(ctx context.Context, conn net.Conn) (registered bool, err error) {
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-done:
		}
	}()
	var mu sync.Mutex
	w := bufio.NewWriter(conn)
	send := func(format string, args ...any) error {
		mu.Lock()
		defer mu.Unlock()
		if _, err := fmt.Fprintf(w, format+"\r\n", args...); err != nil {
			return err
		}
		return w.Flush()
	}
	hb := c.Heartbeat
	if hb <= 0 {
		hb = ircHeartbeat
	}
	go func() {
		t := time.NewTicker(hb)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-t.C:
				if err := send("PING :sakms"); err != nil {
					_ = conn.Close()
					return
				}
			}
		}
	}()
	if err := send("NICK %s", c.Cfg.Nick); err != nil {
		return false, err
	}
	if err := send("USER %s 0 * :%s", c.Cfg.Nick, c.Cfg.Nick); err != nil {
		return false, err
	}
	r := bufio.NewReaderSize(conn, 8192)
	for {
		_ = conn.SetReadDeadline(time.Now().Add(ircReadTimeout))
		line, rerr := r.ReadString('\n')
		if rerr != nil {
			if errors.Is(rerr, errIRCClosed) || ctx.Err() != nil {
				return registered, errIRCClosed
			}
			return registered, rerr
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "PING") {
			if err := send("PONG %s", strings.TrimSpace(strings.TrimPrefix(line, "PING"))); err != nil {
				return registered, err
			}
			continue
		}
		_, cmd, params := splitIRC(line)
		switch cmd {
		case "001":
			registered = true
			if c.Cfg.Password != "" {
				if err := send("PRIVMSG NickServ :IDENTIFY %s", c.Cfg.Password); err != nil {
					return registered, err
				}
			}
			if err := joinIRCChannels(send, c.Cfg.Channels); err != nil {
				return registered, err
			}
		case "376", "422":
			// Claude 2026-10-19: JOIN again after MOTD.
			// Reason: some IRCds drop JOIN sent on 001; live synIRC stored 0
			//   tagged [TT:]/[FN:] lines while #pre PRIVMSG titles kept arriving.
			// Troubleshooting: no "predb irc names-end" after connected → JOIN
			//   still not accepted. Review if: 005 JOIN is required instead.
			if err := joinIRCChannels(send, c.Cfg.Channels); err != nil {
				return registered, err
			}
		case "473", "475", "477", "479":
			// Claude 2026-10-19: log JOIN denials.
			// Reason: connected=true with parsed=0 and empty IRC filenames looked
			//   like a live feed while +k/+i left us outside #PreNNTmux.
			// Troubleshooting: o2cli grep "predb irc join failed".
			// Review if: the channel uses a key (JOIN #chan key).
			log.Printf("usenetsearch: predb irc join failed cmd=%s %s", cmd, params)
		case "366":
			log.Printf("usenetsearch: predb irc names-end %s", params)
		case "433": // nick in use — mint another unregistered identity
			n, nerr := RandomNick()
			if nerr != nil {
				c.Cfg.Nick += "_"
			} else {
				c.Cfg.Nick = n
			}
			if err := send("NICK %s", c.Cfg.Nick); err != nil {
				return registered, err
			}
		case "PRIVMSG", "NOTICE":
			// Claude 2026-10-19: TmuxPREBot hashed [FN:] lines are NOTICE.
			// Reason: live synIRC connected 1775 IRC rows, all filename="" size=0
			//   pre_at=0 — #pre PRE: PRIVMSG titles only. hash_hits=0 because
			//   ByFilename never saw a hash.part1.rar from #PreNNTmux.
			// Troubleshooting: irc rows with filename still 0 after NOTICE →
			//   bot is not on this nick's channel view (need +v / log 473).
			// Review if: TmuxPREBot switches back to PRIVMSG-only.
			c.handlePrivmsg(ctx, params)
		}
	}
}

// splitIRC splits ":prefix CMD params..." into its parts.
func splitIRC(line string) (prefix, cmd, params string) {
	if strings.HasPrefix(line, ":") {
		sp := strings.IndexByte(line, ' ')
		if sp < 0 {
			return line[1:], "", ""
		}
		prefix, line = line[1:sp], line[sp+1:]
	}
	if sp := strings.IndexByte(line, ' '); sp >= 0 {
		return prefix, strings.ToUpper(line[:sp]), line[sp+1:]
	}
	return prefix, strings.ToUpper(line), ""
}

func joinIRCChannels(send func(string, ...any) error, channels []string) error {
	for _, ch := range channels {
		if ch = strings.TrimSpace(ch); ch != "" {
			if err := send("JOIN %s", ch); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *IRCClient) handlePrivmsg(ctx context.Context, params string) {
	i := strings.Index(params, " :")
	if i < 0 || !strings.Contains(params[:i], "#") {
		return
	}
	body := params[i+2:]
	a, ok := ParseAnnounce(body)
	if strings.Contains(body, "[TT:") {
		c.tagged++
		if c.tagged <= 5 {
			log.Printf("usenetsearch: predb irc tagged parsed=%t fn_set=%t", ok, ok && a.Entry.Filename != "")
		}
	}
	if !ok {
		return
	}
	switch a.Kind {
	case "NEW":
		if err := c.Store.Upsert(ctx, a.Entry, SourceIRC); err == nil {
			c.parsed++
		}
	case "NUKE", "MODNUKE":
		_ = c.Store.SetNuked(ctx, a.Entry.Title, true)
		c.parsed++
	case "UNNUKE":
		_ = c.Store.SetNuked(ctx, a.Entry.Title, false)
		c.parsed++
	}
}
