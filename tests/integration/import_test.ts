import { assertEquals, assertRejects } from "@std/assert";
import { MainDatabase } from "../../src/db/database.ts";
import { importDatabase } from "../../src/db/import.ts";
import { CANONICAL_SCHEMA } from "../../src/db/schema.ts";
import { ContentAddressedMediaStore } from "../../src/ai/media_store.ts";

async function fixture(root: string): Promise<string> {
  const path = `${root}/source.db`;
  const source = new MainDatabase(path);
  try {
    source.raw.exec(
      CANONICAL_SCHEMA.join("\n").replaceAll("ts_schema_versions", "schema_migrations"),
    );
    source.raw.exec(
      "DROP TABLE legacy_coc_attrs; DROP TABLE import_archives; DROP TABLE traditional_facts; DROP TABLE ai_run_context; DROP TABLE album_files; DROP TABLE album_index;",
    );
    for (let version = 1; version <= 11; version++) {
      source.run(
        "INSERT INTO schema_migrations VALUES(?,?,?,?)",
        version,
        `source-${version}`,
        `checksum-${version}`,
        1,
      );
    }
    source.raw.exec(
      "CREATE TABLE character_attrs(user_id INTEGER,attr_name TEXT,attr_value TEXT); INSERT INTO character_attrs VALUES(7,'STR','80');",
    );
    source.raw.exec(
      `CREATE TABLE ai_media_groups(chat_id INTEGER,media_group_id TEXT,first_seen_at INTEGER,updated_at INTEGER,expires_at INTEGER,photo_only INTEGER,PRIMARY KEY(chat_id,media_group_id));
CREATE TABLE ai_media_group_photos(chat_id INTEGER,media_group_id TEXT,msg_id INTEGER,sent_at INTEGER,user_id INTEGER,username TEXT,atable_username TEXT,caption TEXT,telegram_file_id TEXT,PRIMARY KEY(chat_id,msg_id));
INSERT INTO ai_media_groups VALUES(-100,'album',1,1,9999999999,1);
INSERT INTO ai_media_group_photos VALUES(-100,'album',9,1,7,'user',NULL,'historical caption','album-file');`,
    );
    source.run(
      "INSERT INTO saved_pics(file_uid,file_id,bot_rate,rand_key) VALUES('u','f',6,?)",
      191315037158113842n,
    );
    source.run("INSERT INTO saved_pics_rating VALUES('u',7,2)");
    source.run(
      "INSERT INTO ai_sessions(chat_id,chat_name,chat_type,provider,model,created_at,updated_at,total_input_tokens,total_output_tokens) VALUES(-100,'group','supergroup','gemini','model',1,1,11,5)",
    );
    source.run(
      "INSERT INTO ai_session_provider_state VALUES(1,'gemini',1,?,1)",
      JSON.stringify({ geminiPreviousInteractionID: "remote" }),
    );
    const store = new ContentAddressedMediaStore(`${root}/source-media`);
    const media = await store.put(new Uint8Array([1, 2, 3]), "image/jpeg");
    source.run(
      "INSERT INTO media_objects VALUES(?,?,?,?,1)",
      media.sha256,
      media.relativePath,
      media.byteSize,
      media.mimeType,
    );
    source.run("INSERT INTO ai_messages VALUES(-100,10,1,7,'user',NULL,'text','request',NULL)");
    source.run("INSERT INTO ai_session_messages VALUES(1,0,-100,10,'user',NULL,0)");
    source.run(
      "INSERT INTO ai_runs(session_id,request_chat_id,request_msg_id,status,provider,model,requested_at,input_tokens,output_tokens,raw_payload,assistant_payload,assistant_payload_format) VALUES(1,-100,10,'generated','gemini','model',1,11,5,?,?,?)",
      new Uint8Array([0, 255]),
      new Uint8Array([0, 128]),
      "gemini-content-v1",
    );
    source.raw.exec("PRAGMA wal_checkpoint(TRUNCATE)");
  } finally {
    source.close();
  }
  return path;
}

Deno.test("Go V11 import preserves rows/tokens/payload/media/votes and leaves source unchanged", async () => {
  const root = await Deno.makeTempDir({ prefix: "ytyan-import-" });
  try {
    const source = await fixture(root);
    const before = await Deno.readFile(source);
    const manifest = await importDatabase({
      source,
      output: `${root}/target`,
      media: `${root}/source-media`,
    });
    assertEquals(await Deno.readFile(source), before);
    assertEquals(manifest.tokens, { input: 11, output: 5, cached: 0 });
    const target = new MainDatabase(`${root}/target/dataset/main.db`, { readOnly: true });
    try {
      assertEquals(target.validate().version, 5);
      assertEquals((manifest.albumMapping as { groups: number }).groups, 1);
      assertEquals((manifest.albumMapping as { files: number }).files, 1);
      assertEquals(target.get("SELECT topic_id,group_id,file_id FROM album_files"), {
        topic_id: -1,
        group_id: "album",
        file_id: "album-file",
      });
      assertEquals(
        target.all<{ payload: string }>(
          "SELECT payload FROM import_archives WHERE source_table='ai_media_group_photos'",
        )[0]!.payload.includes("historical caption"),
        true,
      );
      const key = target.raw.prepare("SELECT rand_key FROM saved_pics");
      key.setReadBigInts(true);
      assertEquals(key.get()!.rand_key, 191315037158113842n);
      assertEquals(target.get("SELECT user_rating_sum,rate_user_count,user_rate FROM saved_pics"), {
        user_rating_sum: 2,
        rate_user_count: 1,
        user_rate: 2,
      });
      assertEquals(target.get("SELECT * FROM legacy_coc_attrs"), {
        user_id: 7,
        attr_name: "STR",
        attr_value: "80",
      });
      assertEquals(
        target.get<{ raw_payload: Uint8Array }>("SELECT raw_payload FROM ai_runs")!.raw_payload,
        new Uint8Array([0, 255]),
      );
      assertEquals(target.all("SELECT * FROM ai_session_provider_state"), []);
      assertEquals(
        target.get<{ count: number }>(
          "SELECT COUNT(*) AS count FROM import_archives WHERE source_table='ai_session_provider_state'",
        )!.count,
        1,
      );
    } finally {
      target.close();
    }
    await assertRejects(
      () => importDatabase({ source, output: `${root}/target`, media: `${root}/source-media` }),
      Deno.errors.AlreadyExists,
    );
  } finally {
    await Deno.remove(root, { recursive: true });
  }
});

Deno.test("corrupt media aborts publication and removes only newly created staging", async () => {
  const root = await Deno.makeTempDir({ prefix: "ytyan-import-" });
  try {
    const source = await fixture(root);
    const db = new MainDatabase(source, { readOnly: true });
    const row = db.get<{ relative_path: string }>("SELECT relative_path FROM media_objects")!;
    db.close();
    await Deno.writeFile(`${root}/source-media/${row.relative_path}`, new Uint8Array([9]));
    await assertRejects(
      () => importDatabase({ source, output: `${root}/target`, media: `${root}/source-media` }),
      Error,
      "corrupt",
    );
    await assertRejects(() => Deno.stat(`${root}/target`), Deno.errors.NotFound);
  } finally {
    await Deno.remove(root, { recursive: true });
  }
});
