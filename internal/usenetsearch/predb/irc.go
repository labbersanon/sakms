package predb

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net"
	"strings"
	"time"
)

// IRCConfig configures the optional PreDB announce feed. Password is the
// NickServ password (decrypted by the caller; never logged or persisted here).
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
)

// IRCClient is a minimal one-connection IRC client: NICK/USER, NickServ
// IDENTIFY, JOIN, PING/PONG, and channel-line parsing into the local Store.
// It never logs message bodies or credentials.
type IRCClient struct {
	Cfg   IRCConfig
	Store *Store

	// Dial overrides the transport (tests); nil dials Cfg over TCP/TLS.
	Dial func(ctx context.Context) (net.Conn, error)
	// Backoff overrides the reconnect delay schedule start (tests).
	Backoff time.Duration

	parsed int
}

func (c *IRCClient) dial(ctx context.Context) (net.Conn, error) {
	if c.Dial != nil {
		return c.Dial(ctx)
	}
	port := c.Cfg.Port
	if port <= 0 {
		port = 6697
	}
	addr := net.JoinHostPort(c.Cfg.Server, fmt.Sprint(port))
	d := &net.Dialer{Timeout: 20 * time.Second}
	if c.Cfg.TLS {
		return (&tls.Dialer{NetDialer: d, Config: &tls.Config{ServerName: c.Cfg.Server, MinVersion: tls.VersionTLS12}}).DialContext(ctx, "tcp", addr)
	}
	return d.DialContext(ctx, "tcp", addr)
}

// Run connects and reconnects (exponential backoff) until ctx is cancelled.
func (c *IRCClient) Run(ctx context.Context) {
	if c.Cfg.Server == "" || c.Cfg.Nick == "" || c.Store == nil {
		log.Printf("usenetsearch: predb irc disabled (server/nick not configured)")
		return
	}
	backoff := c.Backoff
	if backoff <= 0 {
		backoff = ircMinBackoff
	}
	delay := backoff
	for ctx.Err() == nil {
		conn, err := c.dial(ctx)
		if err == nil {
			log.Printf("usenetsearch: predb irc connected")
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
	w := bufio.NewWriter(conn)
	send := func(format string, args ...any) error {
		if _, err := fmt.Fprintf(w, format+"\r\n", args...); err != nil {
			return err
		}
		return w.Flush()
	}
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
		case "433": // nick in use
			c.Cfg.Nick += "_"
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
