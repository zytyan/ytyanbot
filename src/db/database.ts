import { createHash } from "node:crypto";
import { DatabaseSync } from "node:sqlite";
import { SQL } from "./queries.ts";
import { backgroundTiming, latencySyncStage } from "../shared/latency.ts";
import { logger } from "../logging.ts";

import {
  CANONICAL_SCHEMA,
  MIGRATIONS,
  REQUIRED_INDEXES,
  REQUIRED_TABLES,
  REQUIRED_TRIGGERS,
} from "./schema.ts";

const inlineQueryNames = new Map<string, string>();
function queryName(sql: string): string {
  const known = queryNames.get(sql);
  if (known) return known;
  let name = inlineQueryNames.get(sql);
  if (!name) {
    name = `inline_${createHash("sha256").update(sql).digest("hex").slice(0, 12)}`;
    if (inlineQueryNames.size >= 128) {
      inlineQueryNames.delete(inlineQueryNames.keys().next().value!);
    }
    inlineQueryNames.set(sql, name);
  }
  return name;
}
const queryNames = new Map(Object.entries(SQL).map(([name, sql]) => [sql as string, name]));

type SqlValue = null | number | bigint | string | Uint8Array;

export interface DatabaseCheck {
  readonly version: number;
  readonly integrity: string;
  readonly foreignKeyIssues: number;
  readonly mediaReferences: number;
}

export class MainDatabase {
  readonly raw: DatabaseSync;

  constructor(path: string, options: { readOnly?: boolean } = {}) {
    this.raw = new DatabaseSync(path, { readOnly: options.readOnly ?? false });
    this.raw.exec("PRAGMA foreign_keys=ON; PRAGMA busy_timeout=5000;");
    if (!options.readOnly) {
      this.raw.exec(
        "PRAGMA journal_mode=WAL; PRAGMA synchronous=NORMAL; PRAGMA wal_autocheckpoint=1000; PRAGMA mmap_size=67108864;",
      );
    }
  }

  close(): void {
    this.raw.close();
  }

  transaction<T>(operation: () => T): T {
    return backgroundTiming("db.transaction", () =>
      latencySyncStage("db.transaction", () => {
        this.#execute("begin", "BEGIN IMMEDIATE", () => this.raw.exec("BEGIN IMMEDIATE"));
        try {
          const result = operation();
          this.#execute("commit", "COMMIT", () => this.raw.exec("COMMIT"));
          return result;
        } catch (error) {
          this.#execute("rollback", "ROLLBACK", () => this.raw.exec("ROLLBACK"));
          throw error;
        }
      }));
  }

  run(sql: string, ...params: SqlValue[]): void {
    this.#execute("run", sql, () => this.raw.prepare(sql).run(...params));
  }

  get<T extends object>(sql: string, ...params: SqlValue[]): T | undefined {
    return this.#execute("get", sql, () => this.raw.prepare(sql).get(...params)) as T | undefined;
  }

  all<T extends object>(sql: string, ...params: SqlValue[]): T[] {
    return this.#execute("all", sql, () => this.raw.prepare(sql).all(...params)) as T[];
  }

  #execute<T>(operation: string, sql: string, query: () => T): T {
    const name = `db.${operation}.${queryName(sql)}`;
    const start = performance.now();
    try {
      return backgroundTiming(name, () => latencySyncStage(name, query));
    } finally {
      const elapsed = performance.now() - start;
      if (elapsed >= 100) {
        // Do not log SQL, parameters, payloads or user text.
        logger("runtime").warn("slow database operation", {
          stage: name,
          elapsed_ms: Math.round(elapsed),
        });
      }
    }
  }

  initialize(version: number = MIGRATIONS.length): void {
    if (!Number.isInteger(version) || version < 1 || version > MIGRATIONS.length) {
      throw new Error("invalid TS schema version");
    }
    const count = this.get<{ count: number }>(
      "SELECT COUNT(*) AS count FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'",
    )?.count ?? 0;
    if (count !== 0) throw new Error(`database is not empty: found ${count} schema objects`);
    this.transaction(() => {
      for (const schema of CANONICAL_SCHEMA.slice(0, version)) this.raw.exec(schema);
      const insert = this.raw.prepare(
        "INSERT INTO ts_schema_versions(version,name,checksum,applied_at) VALUES(?,?,?,unixepoch())",
      );
      for (const migration of MIGRATIONS.slice(0, version)) {
        insert.run(migration.version, migration.name, migration.checksum);
      }
    });
  }

  /** Apply only unapplied TS migrations; never accept an unknown or altered source history. */
  upgrade(options: { offlineArchive?: boolean } = {}): void {
    this.transaction(() => {
      const applied = this.all<{ version: number; name: string; checksum: string }>(
        "SELECT version,name,checksum FROM ts_schema_versions ORDER BY version",
      );
      if (applied.length === 0 || applied.length > MIGRATIONS.length) {
        throw new Error("database contains an unknown or incomplete migration set");
      }
      for (let i = 0; i < applied.length; i++) {
        const expected = MIGRATIONS[i]!;
        const actual = applied[i]!;
        if (
          actual.version !== expected.version || actual.name !== expected.name ||
          actual.checksum !== expected.checksum
        ) {
          throw new Error("database migration checksum mismatch or unknown version");
        }
      }
      if (
        applied.length < 6 && !options.offlineArchive &&
        (this.get<{ n: number }>("SELECT COUNT(*) AS n FROM ai_sessions")!.n ||
          this.get<{ n: number }>("SELECT COUNT(*) AS n FROM media_objects")!.n ||
          this.get<{ n: number }>("SELECT COUNT(*) AS n FROM ai_run_context")!.n ||
          this.get<{ n: number }>("SELECT COUNT(*) AS n FROM import_archives")!.n)
      ) {
        throw new Error(
          "populated pre-v6 database requires offline db-import to archive omitted data",
        );
      }
      for (let i = applied.length; i < MIGRATIONS.length; i++) {
        this.raw.exec(CANONICAL_SCHEMA[i]!);
        const migration = MIGRATIONS[i]!;
        this.run(
          "INSERT INTO ts_schema_versions(version,name,checksum,applied_at) VALUES(?,?,?,unixepoch())",
          migration.version,
          migration.name,
          migration.checksum,
        );
      }
    });
  }

  validate(): DatabaseCheck {
    for (const migration of MIGRATIONS) {
      const row = this.get<{ name: string; checksum: string }>(
        "SELECT name,checksum FROM ts_schema_versions WHERE version=?",
        migration.version,
      );
      if (!row) throw new Error(`database migration ${migration.version} is missing`);
      if (row.name !== migration.name || row.checksum !== migration.checksum) {
        throw new Error(`database migration ${migration.version} checksum mismatch`);
      }
    }
    const versions = this.get<{ count: number; maximum: number }>(
      "SELECT COUNT(*) AS count,MAX(version) AS maximum FROM ts_schema_versions",
    );
    if (versions?.count !== MIGRATIONS.length || versions.maximum !== MIGRATIONS.at(-1)!.version) {
      throw new Error("database contains an unknown or incomplete migration set");
    }
    for (const name of REQUIRED_TABLES) this.#requireObject("table", name);
    for (const name of REQUIRED_INDEXES) this.#requireObject("index", name);
    for (const name of REQUIRED_TRIGGERS) this.#requireObject("trigger", name);
    for (
      const retired of [
        "gemini_sessions",
        "gemini_contents",
        "gemini_system_prompt",
        "gemini_memories",
        "gemini_messages",
        "gemini_session_migrations",
        "ai_chat_models",
        "ai_session_meta",
        "ai_message_meta",
        "chat_attr",
        "chat_topics",
        "saved_msgs",
        "raw_update",
        "edit_history",
        "meili_wal",
        "album_index",
      ]
    ) {
      const exists = this.get<{ found: number }>(
        "SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name=?) AS found",
        retired,
      )?.found;
      if (exists) throw new Error(`retired table remains: ${retired}`);
    }
    const integrity =
      this.get<{ integrity_check: string }>("PRAGMA integrity_check")?.integrity_check ?? "unknown";
    if (integrity !== "ok") throw new Error(`integrity_check: ${integrity}`);
    const foreignKeyIssues = this.all<Record<string, SqlValue>>("PRAGMA foreign_key_check").length;
    if (foreignKeyIssues !== 0) {
      throw new Error(`foreign_key_check returned ${foreignKeyIssues} rows`);
    }
    const mediaReferences =
      this.get<{ count: number }>("SELECT COUNT(*) AS count FROM media_objects")?.count ?? 0;
    return { version: MIGRATIONS.at(-1)!.version, integrity, foreignKeyIssues, mediaReferences };
  }

  #requireObject(type: string, name: string): void {
    const exists = this.get<{ found: number }>(
      "SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type=? AND name=?) AS found",
      type,
      name,
    )?.found;
    if (!exists) throw new Error(`required ${type} is missing: ${name}`);
  }
}
