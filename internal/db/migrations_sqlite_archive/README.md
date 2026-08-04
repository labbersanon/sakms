# SQLite migration archive (pre-Postgres)

These 61 goose SQL files are the historical SQLite schema chain used through
sakms goose version 61. They are **not** embedded in the binary (`//go:embed
migrations/*.sql` only). Retained for reference after the 2026-08-04
Postgres-only cutover (fresh baseline in `../migrations/`).

Do not run these against Postgres.
