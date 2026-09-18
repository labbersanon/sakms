// Package usenetsearch is SAK's native NNTP discovery backend: an incremental
// OVER crawler over operator-chosen newsgroups, a local SQLite header index,
// and Candidate → *usenet.NZB synthesis for the existing download engine.
//
// Import discipline: stdlib + modernc.org/sqlite + internal/usenet only.
// Must NOT import internal/api, internal/grabs, internal/prowlarr, or
// internal/mode — adapters live on the api side.
//
// Claude 2026-09-17: feature-flagged; default off. See docs/usenet-nntp-native-spike.md.
package usenetsearch
