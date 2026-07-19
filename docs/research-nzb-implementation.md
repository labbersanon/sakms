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

No maintained Go NZB library exists. Native implementation must be built from
scratch. Stack: NNTP client (small), yEnc decoder (small), NZB XML parser
(trivial), par2 repair (hard — embed `par2cmdline` binary or implement natively).

---

## Open questions before implementation

- External first or native first? NZBGet already works; SABnzbd is a thin port.
  Native is weeks of work with no library to lean on.
- If native: embed `par2cmdline` binary (same pattern as aria2c) or implement
  repair natively?
- Multi-server support from day one, or single-server first?
