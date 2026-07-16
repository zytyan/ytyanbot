# Repository agent guide

These instructions apply to the whole repository.

## SQL and database work

- Keep canonical schemas and business queries under `sql/`.
- Add or change business SQL through sqlc. The project uses `/usr/local/bin/sqlc` v1.31.1.
- Run `sqlc generate` after every schema or query change. Never edit generated sqlc files by hand.
- Versioned DDL, connection PRAGMAs, SQLite Backup API operations, `VACUUM INTO`, and integrity checks are infrastructure exceptions; keep them in the migration or operations layer, not in handlers or business logic.
- Register main-database migration definitions in `globalcfg/migrationdefs` and their transactional runners in `internal/mainmigrations`, with a strictly increasing version and stable source checksum. Mark large rewrites as offline so normal startup refuses to apply them.
- Main-database migration runners live in `internal/mainmigrations`; `globalcfg/migrations.go` is only the runtime adapter. Use `cmd/db-init` for a new canonical database and `cmd/main-db-migrate` for post-V2 offline rewrites. Never initialize or rewrite a production database in place.
- `sql.Canonical()` is the explicit fresh-database schema list. Migration-only legacy schemas belong under `sql/migrate/`; do not restore glob-based `schema_*.sql` loading.
- The legacy message SQLite database, Meilisearch writer/WAL, and search HTTP backend are retired. Preserve their verified offline archive, but do not restore runtime connections or configuration without a new product requirement.
- Main schema V4 retires `chat_attr`, `chat_topics`, and the `web_id`/automatic-OCR/message-archive chat settings. `users` is a lightweight Telegram name dimension keyed directly by `user_id`; do not restore profile-photo or per-user timezone persistence.
- Main schema V5 keeps scalar counters in `chat_stat_daily` and stores per-user and ten-minute data in `chat_stat_user_daily` and `chat_stat_bucket_daily`. Decode legacy Gob values only in the offline runner; runtime writes all three tables in one transaction.
- Main schema V6 indexes `ai_session_messages(chat_id, msg_id, context_only)` for reverse session lookup. Do not add standalone status indexes without a real query consumer.
- Main schema V7 indexes `yt_dl_results(file_id)`; cache refreshes must update file and descriptive metadata from `excluded` values without resetting the existing upload counter.
- Main schema V8 timestamps Bilibili inline contexts. They remain valid for 30 days, are cleaned daily, and expired callbacks must return an explicit user-facing message.
- Configure SQLite connection-wide behavior through the DSN or a connection hook so every pooled connection enables foreign keys and the same timeout/journal settings.
- A SQL change is accepted only after a second `sqlc generate` is idempotent, targeted tests pass, `go test ./...` passes, and the project builds completely.

## Feature acceptance and Git workflow

- Work on `codex/ai-db-v2` until the AI database V2 rollout has passed production acceptance.
- Keep one independently accepted feature in each commit. Do not accumulate several completed features into one commit.
- After a feature passes its acceptance checks, commit it once and immediately push the branch before starting the next feature.
- When a feature changes architecture, commands, migration behavior, or acceptance checks, update this file in the same feature commit.
- Do not fast-forward or merge this branch into `master` until the maintenance-window migration and live acceptance have succeeded.

## Verification and production safety

- Prefer tests against temporary databases and a read-only production backup. Never experiment on the live database.
- Before a database migration, create a restorable backup and record row counts, integrity results, and file checksums.
- Do not stop, restart, or replace the live service without explicit approval for that operation.
- Never commit production configuration, databases, media, logs, secrets, backups, or other runtime files under `build/`.
- Preserve all AI session, message, token, prompt, provider payload, and media data unless a task explicitly authorizes deletion. The legacy AI memory feature and its data are explicitly approved for removal in the AI database V2 work.
- The legacy AI memory table, commands, session cache, and `%MEMORIES%` prompt variable have been retired. Do not reintroduce them without a new explicit product requirement.
- Store AI binary media through `helpers/aimedia`: SHA-256 addressed files are written atomically outside SQLite, verified before use, and garbage collection may delete only objects absent from the database reference set.
- The generic AI V2 schema and business queries live in `sql/schema_ai_v2.sql` and `sql/query_ai_v2.sql`; sqlc generates their dedicated package at `globalcfg/aiq`. Runtime AI persistence must use this package; the main `globalcfg/q` package no longer contains Gemini CRUD queries.
- Persist an AI request before calling a provider, transition its Run from `pending` to `generated` exactly once, and increment aggregate Token counters only on that first transition. Save Telegram delivery and provider-state promotion in one transaction. Delivery failures must retain the generated payload for retry, and usage callbacks must read the immutable Run snapshot.
- Persist provider and model as separate values in chat settings, sessions, and Runs. Do not derive a provider from a model name when reading persisted state.
- `/backupdb` remains database-only by default and must mark that archive as an incomplete AI dataset. `media=1` must enumerate media from the consistent main-database snapshot, verify every hash and size before streaming, include `media-manifest.tsv`, and honor request cancellation plus `GOYTYAN_BACKUP_MAX_DURATION`.
- V1 to V2 conversion must use `cmd/ai-db-migrate`: open the source read-only, create a staging copy with SQLite Backup, read legacy rows through `internal/aidbmigrate/legacyq`, write V2 rows through `globalcfg/aiq`, validate counts/tokens/assistant payloads/media/integrity/foreign keys, then publish a `VACUUM INTO` database, media tree, and Manifest. Never point its output paths at existing production assets.
- Treat `gemini_messages` and `gemini_session_migrations` as retired V0 AI tables. The offline migrator must merge non-duplicate V0 messages into V2 before dropping both tables; do not rely on the `gemini_sessions` foreign-key cascade, because it silently loses V0-only history.
