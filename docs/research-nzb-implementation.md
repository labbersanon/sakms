# NZB Implementation Research Notes

Reference material collected before writing SAK's native Usenet downloader.

---

## nzbunity (github.com/tumblfeed/nzbunity)

TypeScript browser extension, actively maintained (last push 2026-04). Controls
external NZBGet/SABnzbd instances via HTTP API. **No native NNTP, yEnc, NZB
parsing, or par2 code** — not useful as a code base to borrow from for native
downloading. Useful for:

### SABnzbd API (currently unsupported by SAK)

All calls are `GET {url}?output=json&apikey={key}&mode={operation}&...params`.

| Operation | Params | Notes |
|---|---|---|
| `fullstatus?skip_dashboard=1` | — | Health check |
| `queue` | — | Overall status; `status`, `speed` (formatted string), `speedlimit_abs`, `sizeleft`, `timeleft`, `slots[]` |
| `addurl` | `name={url}`, `cat`, `nzbname` | Returns `{nzo_ids: ["nzo_xxx"]}` — take `[0]` |
| `addfile` | POST multipart, field `nzbfile`, content-type `application/x-nzb` | Returns same `nzo_ids` shape |
| `queue&name=pause&value={id}` | — | Pause item |
| `queue&name=resume&value={id}` | — | Resume item |
| `queue&name=delete&value={id}` | — | Remove item |
| `pause` / `resume` | — | Queue-wide pause/resume |
| `config&name=speedlimit&value={N}K` | — | Speed limit in KB; `0` = no limit |
| `get_cats` | — | Returns `string[]`; filter out `"*"` |

Queue item fields: `nzo_id`, `status`, `filename`, `cat`, `mb` (float, MB),
`mbleft` (float, MB), `timeleft` (formatted string). Speed comes back as a
formatted string (`"1.5 MB"`) — parse with `/([\d\.]+)\s+(\w+)/i`, then
multiply by `K=1024`, `M=1048576`, `G=1073741824`.

### NZBGet API (confirms + extends SAK's existing internal/nzbget)

`append` param order (positional JSON-RPC array):
```
[NZBFilename, NZBContent_base64, Category, Priority, AddToTop, AddPaused,
 DupeKey, DupeScore, DupeMode, PPParameters]
```
`NZBContent` must be base64-encoded NZB XML.

`editqueue` param order: `[Command, Param, IDs_array]`  
Command strings: `GroupPause`, `GroupResume`, `GroupDelete`

Categories: call `config`, filter entries where `Name` matches `Category\d+\.Name`,
value is the category name.

Priority values (NZBGet-specific):
```
default = -100
paused  = -2
low     = -1
normal  =  0
high    =  1
force   =  2
```

Post-processing values:
```
default        = -1
none           =  0
repair         =  1
repair_unpack  =  2
repair_unpack_delete = 3
```

### DirectNZB / X-DNZB-* headers

When an NZB indexer (Newznab, NZBGeek, etc.) serves an NZB file via a download
link, it returns metadata in `X-DNZB-*` HTTP response headers alongside the NZB
XML body. SAK should read these when fetching an NZB from Prowlarr's download
URL — they give accurate metadata without a separate API call:

| Header | Field | Notes |
|---|---|---|
| `X-DNZB-RCode` | `rcode` | Response code (e.g. `200`, `400`) |
| `X-DNZB-RText` | `rtext` | Human-readable response text |
| `X-DNZB-Name` | `name` | Release name |
| `X-DNZB-Category` | `category` | Indexer category string |
| `X-DNZB-Moreinfo` | `moreinfo` | URL for more info |
| `X-DNZB-NFO` | `nfo` | URL to NFO file |
| `X-DNZB-Propername` | `propername` | Clean title (e.g. "Show Name") |
| `X-DNZB-Episodename` | `episodename` | Episode title |
| `X-DNZB-Year` | `year` | Release year |
| `X-DNZB-Details` | `details` | Indexer detail page URL |
| `X-DNZB-Failure` | `failure` | Error message if `rcode` != 200 |

Check `rcode` / `failure` before trusting the body — a `400` with a `failure`
header means the indexer rejected the download request (e.g. daily limit hit),
even though the HTTP status may be 200.

### Normalized queue model (reference for SAK's internal DTO)

```
NZBQueue {
  status:        string   // "Downloading", "Paused", "Idle"
  speedBytes:    int64    // bytes/sec
  maxSpeedBytes: int64    // 0 = unlimited
  sizeRemaining: string   // formatted
  timeRemaining: string   // formatted, "∞" when speed=0
  categories:    []string
  items:         []NZBQueueItem
}

NZBQueueItem {
  id:                string
  status:            string
  name:              string
  category:          string
  sizeBytes:         int64
  sizeRemainingBytes int64
  percentage:        int     // 0-100
  timeRemaining:     string
}
```

This model works for both SABnzbd and NZBGet and is a good target shape for a
unified `internal/nzbdownloader` queue — the same way `internal/downloader`'s
`aria2.Download` wraps aria2c's native output into SAK's own type.

---

## Library candidates (all abandoned ~2015-2016)

| Library | Last commit | Notes |
|---|---|---|
| `strider-/go-usenet` | 2015 | NNTP+yEnc+NZB+par2 but dead |
| `gjrtimmer/nzb` | 2016 | NZB parsing only, dead |
| `matthiassb/go-usenet` | 2016 | gonzbee fork, dead |
| `andrewstuart/yenc` | 2024 | Single-file yEnc only |

---

## go-pugleaf (github.com/go-while/go-pugleaf) — REFERENCE ONLY (GPL-2.0)

Usenet NNTP server/gateway written in Go. 14 stars, last push 2026-01-13.
**GPL-2.0 — cannot copy code into SAK.** Use as a reference implementation only.

The `internal/nntp` package is the best real-world Go NNTP client code found.
Study it for implementation patterns; write SAK's equivalent from scratch.

### BackendConn + Pool pattern

```go
type BackendConn struct {
    conn     net.Conn
    textConn *textproto.Conn   // stdlib net/textproto — the right foundation
    writer   *bufio.Writer
    // ... auth state, timestamps
}

type Pool struct {
    connections chan *BackendConn  // channel as semaphore + queue
    maxConns    int
    idleTimeout time.Duration     // DefaultConnExpire = 25s
    // stats: totalCreated, totalClosed
}
```

Pool pattern: `Get()` acquires a `BackendConn` from the channel (or dials a
new one up to `maxConns`); caller does the NNTP operation; `Put()` returns
it; on any error, close+discard the connection (never put a broken conn back).

### NNTP response codes to handle

| Code | Meaning |
|---|---|
| 200/201 | Welcome (server ready, posting allowed/forbidden) |
| 281 | Auth success |
| 381 | Auth requires password (send AUTHINFO PASS) |
| 220 | Article follows (full article) |
| 221 | Head follows |
| 222 | Body follows |
| 223 | Article exists (STAT) |
| 430 | No such article |
| 451 | Article removed (DMCA) |

### Commands SAK needs for downloading

- `AUTHINFO USER {user}` / `AUTHINFO PASS {pass}` — auth sequence
- `STAT <message-id>` → 223 (exists) or 430/451
- `BODY <message-id>` → 222 + multiline yEnc body (most efficient — skip headers)
- `ARTICLE <message-id>` → 220 + headers + body (if headers needed)
- `GROUP {newsgroup}` → confirm group exists before downloading

### Connection setup (from BackendConfig)

```go
type BackendConfig struct {
    Host     string
    Port     int
    SSL      bool          // crypto/tls for TLS connections
    Username string
    Password string
    MaxConns int
    ConnectTimeout time.Duration
    // Proxy: SOCKS4/5 support (optional for SAK's initial cut)
}
```

For TLS: dial `net.Conn`, then `tls.Client(conn, &tls.Config{ServerName: host})`.
Wrap in `textproto.NewConn(conn)` to get the line-oriented NNTP reader/writer.

### Error sentinel values

Define package-level errors for clean handling at the caller:
```go
var ErrArticleNotFound = fmt.Errorf("article not found")   // 430
var ErrArticleRemoved  = fmt.Errorf("article removed (DMCA)") // 451
```
Callers can `errors.Is()` against these to decide whether to try a fallback
server or mark the article permanently unavailable.

---

## par2cron (github.com/desertwitch/par2cron) — MIT, code borrowable

PAR2 integrity & self-repair engine. 25 stars, last push 2026-07-02. Go 1.26.
**MIT license — `internal/par2` and `internal/verify` code can be borrowed directly.**

### What it does natively (pure Go, borrowable)

`internal/par2` — PAR2 file parser. Reads `.par2` binary packet format into
typed Go structs without calling any external binary:

```go
type Hash [16]byte  // MD5

type FileSet struct {
    Files      []File
    SetsMerged []Set
}

type Set struct {
    SetID             Hash
    MainPacket        *MainPacket
    RecoverySet       []FilePacket   // files the par2 protects
    NonRecoverySet    []FilePacket   // auxiliary files
    MissingRecoveryPackets    []Hash // what's absent
    MissingNonRecoveryPackets []Hash
}

type MainPacket struct {
    SetID       Hash
    SliceSize   uint64  // recovery slice size
    RecoveryIDs []Hash
}

type FilePacket struct {
    FileID      Hash
    Name        string
    Size        int64
    Hash        Hash    // MD5 of entire file
    Hash16k     Hash    // MD5 of first 16 KB (fast check)
    FromUnicode bool
}
```

`internal/verify` — verifies whether files on disk match the PAR2 checksums,
using the parsed `FileSet`. Pure Go, no binary needed. Can tell SAK whether
a download is intact before attempting repair.

### What it shells out for (Galois field repair math)

`internal/repair` wraps a `schema.CommandRunner` that calls `par2cmdline -r`
(or equivalent). The struct carries `Par2Args []string` — extra flags forwarded
to the binary. The repair arithmetic (GF(2^16)) is not implemented in Go.

**For SAK:** native par2 parsing + verification is free (MIT, borrow the
`internal/par2` code). Repair still requires embedding a `par2cmdline` binary
— same `//go:embed` pattern as aria2c. The SAK flow would be:

1. Parse `.par2` file(s) natively → `FileSet`
2. Verify files on disk against checksums → need repair?
3. If yes: `exec.Command("par2cmdline", "r", par2file)` (embedded binary)
4. Verify again after repair

---

## go-yEnc-FPE (pkg.go.dev/github.com/Tensai75/go-yEnc-FPE) — NOT USEFUL

**Not a yEnc decoder.** It is an AES-FF1 Format-Preserving Encryption library
for already-encoded yEnc blocks — it encrypts/decrypts yEnc structure while
preserving format compatibility. No yEnc encode or decode is performed. Zero
relevance for NZB downloading.

---

## Revised native implementation stack

| Layer | Approach | Source |
|---|---|---|
| NNTP client + pool | Build from scratch using `net/textproto` | Reference: go-pugleaf `internal/nntp` (GPL — read, don't copy) |
| NZB XML parsing | `encoding/xml` stdlib | Trivial; no library needed |
| yEnc decoding | Build from scratch | Spec: https://www.yenc.org/yenc-draft.1.3.txt — small, well-defined |
| PAR2 parse + verify | Borrow from par2cron `internal/par2` + `internal/verify` | MIT — copy directly |
| PAR2 repair | Embed `par2cmdline` binary | Same `//go:embed` + `cmd/download-par2cmdline` pattern as aria2c |
| X-DNZB-* headers | Read from HTTP response when fetching NZB URL | See nzbunity section above |

---

## Open questions before implementation

- External first or native first? NZBGet already works; SABnzbd is a thin port.
  Native is weeks of work even with the above references.
- Multi-server support from day one, or single-server first?
- par2 repair: optional (skip if no .par2 files) or always-required gate?
