# Native NNTP discovery — removed

Claude 2026-10-23: the native `OVER` crawler, header index, namefix, and
`sakms-nntp:` locators are gone. Searchable ~4500 after PAR2/NFO/PreDB was
not an indexer catalog. ngPost random filenames never joined PreDB.

**Grab catalog:** Newznab (`newznab` + optional `newznab2`) then Prowlarr.
Host 2 is NZB.life: base `https://api.nzb.life/` (not `nzb.su`). Catalog
`t=search` is cat-only; `q=*` only on missing parameter (helpapi q=Required
is the wrong-domain 200). Limit 1–100.

**Overlap index:** background `t=search` pages of cat 2000/5000/6000 into
`newznab_catalog` at 1 page / 5s per host, 100/page (Dock card). Grab search
is a separate client at 1s (NZB.life: wait 1–2s; 429 Retry-After 5s).
Lighthouse (Discover) does not filter on this table yet. Enclosure URLs are
not stored.

**Download pipe:** Eweka NNTP subscriptions via `internal/usenet` `AddNZB`.
`cmd/nntpprobe` stays as a standalone probe.

Do not scrape indexer HTML. Do not restore `usenet_nntp_*` index tables.
In-flight `sakms-nntp:` grab URLs cannot relaunch after this drop.
