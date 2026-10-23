# Native NNTP discovery — removed

Claude 2026-10-23: the native `OVER` crawler, header index, namefix, and
`sakms-nntp:` locators are gone. Searchable ~4500 after PAR2/NFO/PreDB was
not an indexer catalog. ngPost random filenames never joined PreDB.

**Grab catalog:** Newznab (`newznab` + optional `newznab2`, recommended
NZBFinder for obfuscated NZBs) then Prowlarr.

**Download pipe:** Eweka NNTP subscriptions via `internal/usenet` `AddNZB`.
`cmd/nntpprobe` stays as a standalone probe.

Do not scrape indexer HTML. Do not restore `usenet_nntp_*` index tables.
In-flight `sakms-nntp:` grab URLs cannot relaunch after this drop.
