import { TelegramMediaStore } from "./ai/media_store.ts";
import { createApplication } from "./app.ts";
import { configPath, ConfigStore, loadConfig } from "./config.ts";
import { MainDatabase } from "./db/database.ts";
import { importDatabase } from "./db/import.ts";
import { SQL } from "./db/queries.ts";

export const VERSION = "0.1.0";

function option(name: string): string | undefined {
  const index = Deno.args.indexOf(name);
  return index >= 0 ? Deno.args[index + 1] : undefined;
}

function parent(path: string): string {
  const index = path.lastIndexOf("/");
  return index > 0 ? path.slice(0, index) : ".";
}

async function exists(path: string): Promise<boolean> {
  try {
    await Deno.stat(path);
    return true;
  } catch (error) {
    if (error instanceof Deno.errors.NotFound) return false;
    throw error;
  }
}

async function initializeDatabase(path: string): Promise<void> {
  if (await exists(path)) throw new Error(`refusing to overwrite existing database: ${path}`);
  await Deno.mkdir(parent(path), { recursive: true });
  const db = new MainDatabase(path);
  try {
    db.initialize();
    const result = db.validate();
    console.log(JSON.stringify({ database: path, ...result }));
  } finally {
    db.close();
  }
}

async function checkDatabase(path: string, mediaPath?: string): Promise<void> {
  const db = new MainDatabase(path, { readOnly: true });
  try {
    const result = db.validate();
    let missingMedia = 0;
    if (mediaPath) {
      const store = new TelegramMediaStore(mediaPath);
      for (
        const row of db.all<{ file_unique_id: string }>(
          "SELECT file_unique_id FROM media_objects ORDER BY file_unique_id",
        )
      ) {
        if (!await store.verify(row.file_unique_id)) missingMedia++;
      }
    }
    const output = { database: path, ...result, mediaChecked: Boolean(mediaPath), missingMedia };
    console.log(JSON.stringify(output));
    if (missingMedia > 0) {
      throw new Error(`${missingMedia} AI media objects are missing or corrupt`);
    }
  } finally {
    db.close();
  }
}

async function serve(): Promise<void> {
  const path = configPath();
  const initial = await loadConfig(path);
  const configs = new ConfigStore(initial, path);
  const checked = new MainDatabase(initial.databasePath, { readOnly: true });
  try {
    checked.validateSchema();
  } finally {
    checked.close();
  }
  const db = new MainDatabase(initial.databasePath);
  const abort = new AbortController();
  const stop = () => abort.abort(new DOMException("shutdown", "AbortError"));
  Deno.addSignalListener("SIGINT", stop);
  Deno.addSignalListener("SIGTERM", stop);
  try {
    const app = await createApplication(db, configs);
    const watcher = configs.watch(
      abort.signal,
      (error) => console.error("configuration reload rejected", error),
    );
    try {
      await app.serve(abort.signal);
    } finally {
      if (!abort.signal.aborted) abort.abort();
      await watcher;
      app.flush();
    }
  } finally {
    Deno.removeSignalListener("SIGINT", stop);
    Deno.removeSignalListener("SIGTERM", stop);
    db.close();
  }
}

async function main(): Promise<void> {
  const command = Deno.args[0] ?? "serve";
  if (command === "--version" || command === "version") {
    console.log(`ytyanbot ${VERSION} (Deno ${Deno.version.deno})`);
    return;
  }
  if (command === "db-init") {
    const path = option("--database");
    if (!path) throw new Error("db-init requires --database <path>");
    await initializeDatabase(path);
    return;
  }
  if (command === "db-upgrade") {
    const path = option("--database");
    if (!path || !await exists(path)) throw new Error("db-upgrade requires an existing --database");
    const db = new MainDatabase(path);
    try {
      db.upgrade();
      console.log(JSON.stringify({ database: path, ...db.validate() }));
    } finally {
      db.close();
    }
    return;
  }
  if (command === "db-check") {
    const path = option("--database");
    if (!path) throw new Error("db-check requires --database <path>");
    await checkDatabase(path, option("--media"));
    return;
  }
  if (command === "db-import") {
    const source = option("--source");
    const output = option("--output");
    if (!source || !output) {
      throw new Error(
        "db-import requires --source <readonly.db> --output <new-directory> [--media <source-media>]",
      );
    }
    console.log(JSON.stringify(await importDatabase({ source, output, media: option("--media") })));
    return;
  }
  if (command === "media-gc") {
    const path = option("--database"), media = option("--media");
    const grace = Number(option("--grace-hours"));
    if (!path || !media || !Number.isFinite(grace) || grace <= 0) {
      throw new Error(
        "media-gc requires --database, --media and positive --grace-hours; stop the runtime before --apply",
      );
    }
    const db = new MainDatabase(path, { readOnly: true });
    try {
      db.validate();
      const retained = new Set(
        db.all<{ file_unique_id: string }>(SQL.BackupMedia).map((row) => row.file_unique_id),
      );
      const dryRun = !Deno.args.includes("--apply");
      const objects = await new TelegramMediaStore(media).collectGarbage(
        retained,
        grace * 3600_000,
        dryRun,
      );
      console.log(
        JSON.stringify({
          dryRun,
          count: objects.length,
          bytes: objects.reduce((sum, object) => sum + object.byteSize, 0),
          objects,
        }),
      );
    } finally {
      db.close();
    }
    return;
  }
  if (command !== "serve") throw new Error(`unknown command: ${command}`);
  await serve();
}

if (import.meta.main) {
  try {
    await main();
  } catch (error) {
    console.error(error instanceof Error ? error.stack ?? error.message : String(error));
    Deno.exit(1);
  }
}
