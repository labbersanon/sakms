// Package usenetsearch is SAK's native NNTP discovery backend: an incremental
// OVER crawler over operator-chosen newsgroups, a local Postgres header index,
// and Candidate → *usenet.NZB synthesis for the existing download engine.
//
// Import discipline: stdlib + database/sql (app Postgres) + internal/usenet,
// plus — as of the crawl-time quality admission feature (admission.go) —
// internal/release, internal/quality, internal/autograb, and
// internal/searchterm for title-token parsing and quality-tier/resolution
// gating. None of those four import internal/api, internal/grabs,
// internal/prowlarr, or internal/mode, so the layering below still holds.
// The predb subpackage (HTTP lookup, IRC feed, local store) imports only
// stdlib + database/sql; it is wired in from internal/api, which owns settings
// and secrets, via the PreDBLookup interface defined here.
// Must NOT import modernc.org/sqlite (banned from cmd/sakms), internal/api,
// internal/grabs, internal/prowlarr, or internal/mode — adapters live on the api side.
//
// Feature-flagged; default off. See docs/usenet-nntp-native.md.
package usenetsearch
