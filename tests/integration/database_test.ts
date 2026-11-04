import { assertEquals, assertRejects } from "@std/assert";
import { MainDatabase } from "../../src/db/database.ts";
import { CANONICAL_SCHEMA, MIGRATIONS, REQUIRED_TABLES } from "../../src/db/schema.ts";
import { temporaryDatabase } from "./helpers.ts";

Deno.test("TS-owned database contains exact migrations and required objects", async () => {
  const temporary = await temporaryDatabase();
  try {
    assertEquals(temporary.db.validate().version, 6);
    assertEquals(
      temporary.db.all<{ version: number; checksum: string }>(
        "SELECT version,checksum FROM ts_schema_versions ORDER BY version",
      ),
      MIGRATIONS.map(({ version, checksum }) => ({ version, checksum })),
    );
    for (const table of REQUIRED_TABLES) {
      assertEquals(
        temporary.db.get<{ count: number }>(
          "SELECT COUNT(*) AS count FROM sqlite_master WHERE type='table' AND name=?",
          table,
        )?.count,
        1,
      );
    }
  } finally {
    await temporary.close();
  }
});

Deno.test("db validation refuses checksum drift", async () => {
  const temporary = await temporaryDatabase();
  const path = temporary.path;
  temporary.db.run("UPDATE ts_schema_versions SET checksum='bad' WHERE version=1");
  temporary.db.close();
  const reopened = new MainDatabase(path);
  try {
    await assertRejects(
      () => Promise.resolve().then(() => reopened.validate()),
      Error,
      "checksum mismatch",
    );
  } finally {
    reopened.close();
    await Deno.remove(path.slice(0, path.lastIndexOf("/")), { recursive: true });
  }
});

Deno.test("v2 upgrade adds indexed request lookup without rewriting history and refuses altered checksums", async () => {
  const temp = await temporaryDatabase();
  try {
    temp.db.raw.exec("PRAGMA foreign_keys=OFF");
    for (
      const table of temp.db.all<{ name: string }>(
        "SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'",
      )
    ) temp.db.raw.exec(`DROP TABLE "${table.name}"`);
    temp.db.raw.exec("PRAGMA foreign_keys=ON");
    for (const schema of CANONICAL_SCHEMA.slice(0, 2)) temp.db.raw.exec(schema);
    for (const migration of MIGRATIONS.slice(0, 2)) {
      temp.db.run(
        "INSERT INTO ts_schema_versions VALUES(?,?,?,1)",
        migration.version,
        migration.name,
        migration.checksum,
      );
    }
    temp.db.run(
      "INSERT INTO ai_sessions(chat_id,topic_id,chat_name,chat_type,provider,model,created_at,updated_at) VALUES(-100,0,'test','supergroup','gemini','test',1,1)",
    );
    const before = temp.db.all<{ id: number; chat_id: number; provider: string; model: string }>(
      "SELECT id,chat_id,provider,model FROM ai_sessions",
    );
    const versions = temp.db.all("SELECT * FROM ts_schema_versions");
    temp.db.run("UPDATE ts_schema_versions SET checksum='changed' WHERE version=1");
    await assertRejects(
      () => Promise.resolve().then(() => temp.db.upgrade()),
      Error,
      "checksum mismatch",
    );
    assertEquals(
      temp.db.get("SELECT name FROM sqlite_master WHERE name='idx_ai_runs_request'"),
      undefined,
    );
    temp.db.run("UPDATE ts_schema_versions SET checksum=? WHERE version=1", MIGRATIONS[0].checksum);
    temp.db.upgrade({ offlineArchive: true });
    temp.db.upgrade(); // Re-running an accepted upgrade is harmless.
    assertEquals(temp.db.validate().version, 6);
    assertEquals(
      temp.db.all("SELECT id,chat_id,provider,model FROM ai_sessions"),
      before.map(({ id, chat_id, provider, model }) => ({ id, chat_id, provider, model })),
    );
    assertEquals(temp.db.all("SELECT * FROM ts_schema_versions WHERE version<=2"), versions);
    const { SQL } = await import("../../src/db/queries.ts");
    const plan = temp.db.all<{ detail: string }>(
      "EXPLAIN QUERY PLAN " + SQL.RunForRequest,
      -100,
      999999999,
    );
    assertEquals(
      plan.some((row) =>
        row.detail.includes("SEARCH") && row.detail.includes("idx_ai_runs_request")
      ),
      true,
    );
    assertEquals(plan.some((row) => row.detail.includes("SCAN ai_runs")), false);
  } finally {
    await temp.close();
  }
});

Deno.test("database probes distinguish inline SQL and transaction phases without retaining parameters", async () => {
  const temp = await temporaryDatabase();
  const { traceUpdate, latencySnapshot } = await import("../../src/shared/latency.ts");
  try {
    await traceUpdate(940001, "command", undefined, () => {
      temp.db.transaction(() => {
        temp.db.get("SELECT ? AS value", "secret-query-parameter");
        temp.db.get("SELECT ? AS value", "another-secret-parameter");
        temp.db.get("SELECT 42 AS value");
      });
      return Promise.resolve();
    });
    const trace = latencySnapshot().recent.find((r) => r.update_id === 940001)!;
    const queries = Object.keys(trace.stages).filter((n) => n.startsWith("db.get.inline_"));
    assertEquals(queries.length, 2);
    assertEquals(queries.some((n) => trace.stages[n]!.calls === 2), true);
    assertEquals(trace.stages["db.transaction"]!.calls, 1);
    assertEquals(Object.keys(trace.stages).some((n) => n.startsWith("db.begin.")), true);
    assertEquals(Object.keys(trace.stages).some((n) => n.startsWith("db.commit.")), true);
    assertEquals(JSON.stringify(trace).includes("secret"), false);
    assertEquals(JSON.stringify(trace).includes("SELECT"), false);
  } finally {
    await temp.close();
  }
});
