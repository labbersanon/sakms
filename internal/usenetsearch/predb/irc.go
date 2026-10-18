package predb

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/tls"
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
)

// IRCConfig configures the optional PreDB announce feed. Password is the
// NickServ password (decrypted by the caller; never logged or persisted here).
// Empty Nick is filled with a random password-style identity at Run().
type IRCConfig struct {
	Server   string
	Port     int
	TLS      bool
	Nick     string
	Password string
	Channels []string
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
			log.Printf("usenetsearch: predb irc connected nick=%s", c.Cfg.Nick)
			c.parsed = 0
			registered, serr := c.session(ctx, conn)
			conn.Close()
			log.Printf("usenetsearch: predb irc disconnected parsed=%d", c.parsed)
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
			for _, ch := range c.Cfg.Channels {
				if ch = strings.TrimSpace(ch); ch != "" {
					if err := send("JOIN %s", ch); err != nil {
						return registered, err
					}
				}
			}
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
		case "PRIVMSG":
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

func (c *IRCClient) handlePrivmsg(ctx context.Context, params string) {
	i := strings.Index(params, " :")
	if i < 0 || !strings.HasPrefix(params, "#") {
		return
	}
	a, ok := ParseAnnounce(params[i+2:])
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
