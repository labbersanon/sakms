// Package usenetsearch is SAK's native NNTP discovery backend: an incremental
// OVER crawler over operator-chosen newsgroups, a local Postgres header index,
// and Candidate → *usenet.NZB synthesis for the existing download engine.
//
// Import discipline: stdlib + database/sql (app Postgres) + internal/usenet only.
// Must NOT import modernc.org/sqlite (banned from cmd/sakms), internal/api,
// internal/grabs, internal/prowlarr, or internal/mode — adapters live on the api side.
//
// Feature-flagged; default off. See docs/usenet-nntp-native.md.
package usenetsearch
