# Repository agent guide

These instructions apply to the whole repository.

## SQL and database work

- Keep canonical schemas and business queries under `sql/`.
- Add or change business SQL through sqlc. The project uses `/usr/local/bin/sqlc` v1.31.1.
- Run `sqlc generate` after every schema or query change. Never edit generated sqlc files by hand.
- Versioned DDL, connection PRAGMAs, SQLite Backup API operations, `VACUUM INTO`, and integrity checks are infrastructure exceptions; keep them in the migration or operations layer, not in handlers or business logic.
- Register main-database migrations in `globalcfg/migrations.go` with a strictly increasing version, stable source checksum, and transactional runner. Mark large rewrites as offline so normal startup refuses to apply them.
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
