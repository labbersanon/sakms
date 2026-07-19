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

## rapidyenc (github.com/mnightingale/rapidyenc) — MIT, use directly

High-performance Go yEnc decoder/encoder. 7 stars, last push 2026-07-11.
**MIT license. Works with `CGO_ENABLED=0`.** This is the yEnc layer for SAK.

### CGO situation — not a problem

The library has three decode implementations selected by build tags:

| Build tag | Implementation |
|---|---|
| CGO enabled | C library via cgo (fastest — AVX2/SIMD) |
| `!cgo && goexperiment.simd && amd64` | Experimental Go SIMD (AVX2 port) |
| `!cgo && !(goexperiment.simd && amd64)` | Pure Go generic scalar |

SAK builds with `CGO_ENABLED=0` and no `GOEXPERIMENT` — it gets the pure Go
generic scalar path automatically. The build compiles clean, zero config needed.
The SIMD path can be unlocked later if throughput becomes a bottleneck.

### API

```go
// Streaming decode from an NNTP body reader (handles dot-unstuffing + ".\r\n")
dec := rapidyenc.NewDecoder(r)
response, err := dec.Next()
// response.Data: decoded bytes
// response.Metadata: Meta{FileName, FileSize, PartNumber, TotalParts, Offset, PartSize}
// Errors: ErrDataMissing, ErrDataCorruption, ErrCrcMismatch

// When the NNTP status line (e.g. "222 Body follows") is already consumed:
dec := rapidyenc.NewDecoder(r, rapidyenc.WithStatusLineAlreadyRead())

// Memory pool for concurrent downloading (reduces GC pressure):
dec := rapidyenc.NewDecoder(r, rapidyenc.WithDataFunc(func() []byte {
    return pool.Get().([]byte)
}))
```

### Meta struct

```go
type Meta struct {
    FileName   string
    FileSize   int64   // total file size across all parts
    PartNumber int64
    TotalParts int64
    Offset     int64   // byte offset within file (for io.WriterAt reassembly)
    PartSize   int64   // size of this part's decoded data
}
```

`Offset` + `PartSize` is exactly what's needed to write each decoded article
segment to the right position in a pre-allocated file via `io.WriterAt` — no
in-memory assembly of parts.

### Error handling

```go
var (
    ErrDataMissing    = errors.New("no binary data")        // article had no yEnc body
    ErrDataCorruption = errors.New("data corruption detected") // truncated before =yend
    ErrCrcMismatch    = errors.New("crc32 mismatch")        // corruption detected
)
```

CRC mismatch means the segment is corrupt — try a fallback server or mark as
failed. `ErrDataMissing` means the article was text-only or not yEnc-encoded.

### Dependencies

`golang.org/x/sync` and `testify` only. No native/platform libs in the pure-Go
path — the pre-compiled `.a` blobs are only linked when CGO is enabled.

---

## go-yEnc-FPE (pkg.go.dev/github.com/Tensai75/go-yEnc-FPE) — NOT USEFUL

**Not a yEnc decoder.** It is an AES-FF1 Format-Preserving Encryption library
for already-encoded yEnc blocks — it encrypts/decrypts yEnc structure while
preserving format compatibility. No yEnc encode or decode is performed. Zero
relevance for NZB downloading. (The `yenc-encryption-standards` repo from the
same author is the spec document for this encryption scheme — equally irrelevant
for standard NZB downloading.)

---

## Tensai75/nntp (github.com/Tensai75/nntp) — BSD-style, use as NNTP client layer

Go NNTP client. Zero stars, last push 2026-05-01, zero dependencies, Go 1.16.
**BSD-style license** (file header: "Copyright 2009 The Go Authors … BSD-style
license"). GitHub reports NOASSERTION because there is no `LICENSE` file, but
the source header is unambiguous.

This is essentially the old `code.google.com/p/nntp` package — the NNTP client
that was once part of the extended Go standard library — with minor additions.
Tensai75 has kept it updated. The code is well-known and well-tested.

### What it provides

Single `*Conn` type wrapping a `net.Conn` + `*bufio.Reader`. All NNTP commands
SAK needs for downloading are present:

| Method | Notes |
|---|---|
| `Dial(network, addr)` | Plain TCP connection |
| `DialTLS(network, addr, config)` | TLS connection |
| `Authenticate(user, pass)` | AUTHINFO USER/PASS sequence |
| `ModeReader()` | Switches mode-switching servers to reader mode |
| `Group(name)` | `GROUP` command — returns number/low/high |
| `Body(id)` → `io.Reader` | `BODY <msg-id>` — most efficient for yEnc download |
| `Article(id)` → `*Article` | `ARTICLE` — headers + body |
| `Head(id)` → `*Article` | `HEAD` — headers only |
| `Overview(begin, end)` | `OVER/XOVER` with fallback — article metadata range |
| `Stat(id)` | Existence check, no data transfer |
| `Quit()` | Closes connection |

`Body()` is what SAK needs: issue `BODY <message-id>`, get back an `io.Reader`
that dot-unstuffs and terminates on `.CRLF`, pipe directly into `rapidyenc.NewDecoder`.

### What it lacks (write on top)

- **Connection pool:** single `*Conn`, no concurrency. Write a pool using a
  buffered channel (go-pugleaf's `BackendConn + Pool` pattern, section above)
  wrapping `Tensai75/nntp.Conn`.
- **Automatic reconnect:** broken connections must be detected and discarded;
  never `Put()` a failed conn back. Already the natural pattern with a channel pool.
- **Error sentinels:** `Conn.cmd()` returns `Error{Code, Msg}` on bad response
  codes. Wrap 430 → `ErrArticleNotFound`, 451 → `ErrArticleRemoved`.

### Usage pattern

```go
import "github.com/Tensai75/nntp"

c, err := nntp.DialTLS("tcp", "news.example.com:563", nil)
if err != nil { ... }
if err := c.Authenticate(user, pass); err != nil { ... }
c.ModeReader() // ignore error — not all servers need it

body, err := c.Body("<" + messageID + ">")
// body is io.Reader — pipe directly to rapidyenc.NewDecoder(body)
```

### vs. building from scratch

Using `Tensai75/nntp` instead of `net/textproto` from scratch saves ~200 lines
of protocol parsing (response code parsing, dot-unstuffing, header key/value
folding). The pool + error-sentinel layer must be written either way. For SAK,
**use this library as the NNTP wire layer and write only the pool on top.**

---

## go-newsgroups/par2 (github.com/go-newsgroups/par2) — BSD-3-Clause, use directly

Pure-Go PAR2: parse + verify + repair, all `CGO_ENABLED=0`. 0 stars, last push
**2026-07-11** (actively maintained). BSD-3-Clause, Go 1.26.4. One dependency:
`github.com/go-erasure/reedsolomon` (GF(2^16) math — no CGO).

**This replaces both `par2cron` (parse/verify) AND the `par2cmdline` binary
embed (repair).** No external binary needed for any part of the PAR2 pipeline.

### API

```go
// Parse one or more .par2 blobs (concatenated packets from possibly multiple
// .par2 files — pass all of them as separate args or a single concatenated blob).
rs, err := par2.Parse(blob1, blob2, ...)
// rs.Files     []FileSpec      — recovery-set files, in Main-packet order
// rs.Recovery  []RecoverySlice — available recovery slices
// rs.SliceSize uint64

// Hash-based verification (MD5+CRC32 per slice — independent of Reed-Solomon).
files := map[string][]byte{
    "archive.nfo": nfoBytes,
    "archive.rar": rarBytes,
    // missing files are simply absent from the map
}
result, err := rs.Verify(files)
// result.Complete   bool   — all files present and correct
// result.Repairable bool   — missing/damaged slices <= available recovery slices
// result.Files      []FileStatus

// Reed-Solomon repair over GF(2^16).
if !result.Complete && result.Repairable {
    repaired, err := rs.Repair(files)
    // repaired: map[filename][]byte — only the reconstructed files
    for name, data := range repaired {
        os.WriteFile(name, data, 0o644)
    }
}
```

### Key types

```go
type FileSpec struct {
    ID      [16]byte
    Name    string
    Length  uint64
    FullMD5 [16]byte
    Slices  []SliceChecksum // per-slice MD5+CRC32 from IFSC packet
}

type VerifyResult struct {
    Complete   bool
    Files      []FileStatus
    Repairable bool
}

type FileStatus struct {
    Name          string
    Present       bool
    Damaged       bool
    MissingSlices []int // global slice indices
}

// Errors:
var ErrNoMainPacket  = errors.New("par2: no main packet found")
var ErrNotRepairable = errors.New("par2: not enough recovery slices to repair")
```

### Repair implementation

`Repair()` calls `Verify()` internally to identify missing/damaged slices, then
solves a u×u system (u = number of missing slices) via Gauss-Jordan elimination
over GF(2^16). This is the Vandermonde RS scheme from the PAR2 spec. The
coefficient matrix is built from the recovery slice exponents.

### Important caveat — interoperability not yet validated

The library is validated by self-consistent round-trip (`Create → damage →
Repair` → bytes match originals), but **byte-exact interoperability with
recovery data produced by `par2cmdline` or QuickPar has NOT been tested against
those tools.** Real-oracle validation is on the project's planned follow-up list.

For SAK this means: a par2 repair path built on this library should be treated
as best-effort until someone validates it against a real NZB download's `.par2`
files. The verify path (hash-based MD5+CRC32) is spec-correct and safe; the
repair path carries a small unknown risk on real-world data until tested.

Mitigation strategy: keep the repair step optional and not a hard gate for the
initial implementation. If repair fails (`ErrNotRepairable` or produces an
output that still fails `Verify`), report the failure clearly rather than
silently accepting corrupt output.

### vs. par2cron + par2cmdline binary embed

| | par2cron + par2cmdline embed | go-newsgroups/par2 |
|---|---|---|
| Parse/verify | MIT, borrow `internal/par2` + `internal/verify` | ✓ included |
| Repair | embed `par2cmdline` binary (~2 MB), platform-specific | ✓ pure Go, no binary |
| CGO | none needed | none needed |
| License | MIT + GPL (par2cmdline) | BSD-3-Clause throughout |
| Interoperability | proven (par2cmdline is the reference) | unvalidated on real files |
| `//go:embed` binary | required | not required |

**Recommendation:** use `go-newsgroups/par2` as the primary path; its elimination
of the binary embed is a meaningful simplification. The interop caveat is a
known gap to validate before calling native PAR2 repair production-ready.

---

## Revised native implementation stack

| Layer | Approach | Source |
|---|---|---|
| NNTP client | `github.com/Tensai75/nntp` (wire layer) + write connection pool on top | BSD-style (Go Authors); pool pattern from go-pugleaf (GPL — read, don't copy) |
| NZB XML parsing | `encoding/xml` stdlib | Trivial; no library needed |
| **yEnc decoding** | **rapidyenc** | **MIT, `CGO_ENABLED=0` safe, use directly** |
| **PAR2 parse + verify + repair** | **go-newsgroups/par2** | **BSD-3-Clause, pure Go, no binary embed; interop caveat — see section above** |
| X-DNZB-* headers | Read from HTTP response when fetching NZB URL | See nzbunity section above |

*Previous stack used par2cron (MIT) + par2cmdline binary embed. Replaced by
go-newsgroups/par2, eliminating the binary embed entirely.*

---

## Open questions before implementation

- External first or native first? NZBGet already works; SABnzbd is a thin port.
  Native is weeks of work even with the above references.
- Multi-server support from day one, or single-server first?
- par2 repair: optional (skip if no .par2 files) or always-required gate?
- go-newsgroups/par2 real-oracle validation: test against a real Usenet download's
  .par2 files before treating repair as production-ready.
