import { assertEquals, assertRejects } from "@std/assert";
import { AiService, type IncomingAiMessage } from "../../src/ai/service.ts";
import { AiRepository } from "../../src/ai/repository.ts";
import { mediaPath, TelegramMediaStore } from "../../src/ai/media_store.ts";
import type { AiRequest } from "../../src/ai/types.ts";
import { parseConfig } from "../../src/config.ts";
import { FakeTelegram, temporaryDatabase } from "./helpers.ts";

async function setup() {
  const temp = await temporaryDatabase();
  const store = new TelegramMediaStore(`${temp.path}.media`);
  const telegram = new FakeTelegram();
  const requests: AiRequest[] = [];
  let gets = 0, downloads = 0;
  telegram.getFile = () => {
    gets++;
    return Promise.resolve({
      fileId: "refreshed",
      fileUniqueId: "unique-photo",
      filePath: "fresh/path",
    });
  };
  telegram.downloadFile = () => {
    downloads++;
    return Promise.resolve(telegram.fileBytes);
  };
  const createService = () =>
    new AiService(
      new AiRepository(temp.db),
      (request) => {
        requests.push(request);
        return Promise.resolve({
          displayText: "answer",
          assistantPayload: new TextEncoder().encode("{}"),
          assistantPayloadFormat: "test",
          raw: new Uint8Array(),
          state: {},
          usage: { inputTokens: 1, outputTokens: 1, cachedInputTokens: 0, thinkingTokens: 0 },
          inputMessageCount: request.messages.length,
        });
      },
      telegram,
      store,
      () => parseConfig(`database-path: ${temp.path}\nai-chats: [-100]\n`),
      { id: 99, firstName: "bot", username: "bot" },
    );
  const message = (id: number): IncomingAiMessage => ({
    chatId: -100,
    chatName: "test",
    chatType: "supergroup",
    messageId: id,
    sentAt: 1,
    userId: 7,
    username: "user",
    text: "@new @bot",
    media: [{
      fileId: "photo",
      uniqueId: "unique-photo",
      kind: "image",
      mimeType: "image/jpeg",
      byteSize: 3,
    }],
  });
  return {
    temp,
    store,
    telegram,
    requests,
    createService,
    message,
    counts: () => ({ gets, downloads }),
  };
}

Deno.test("Telegram media cache hits avoid downloads; missing historical media recovers after restart", async () => {
  const f = await setup();
  try {
    let service = f.createService();
    await service.handle(f.message(1));
    await service.handle(f.message(2));
    assertEquals(f.counts(), { gets: 1, downloads: 1 });
    assertEquals(f.temp.db.all("SELECT * FROM media_objects"), [{
      file_unique_id: "unique-photo",
      file_id: "photo",
      byte_size: 3,
      mime_type: "image/jpeg",
    }]);
    await Deno.remove(`${f.store.root}/${mediaPath("unique-photo")}`);
    service = f.createService();
    await service.handle({
      ...f.message(3),
      text: "continue",
      media: [],
      reply: { ...f.message(100), media: [], text: "answer" },
    });
    assertEquals(f.counts(), { gets: 2, downloads: 2 });
    assertEquals(f.requests.at(-1)!.messages[0]!.media[0]!.bytes, f.telegram.fileBytes);
    assertEquals(f.temp.db.get("SELECT file_id FROM media_objects"), { file_id: "refreshed" });
    assertEquals(await f.store.open("unique-photo"), f.telegram.fileBytes);
    assertEquals(
      f.temp.db.get("SELECT name FROM sqlite_master WHERE name='ai_run_context'"),
      undefined,
    );
    assertEquals(
      f.temp.db.all<{ name: string }>("PRAGMA table_info(ai_runs)").some((c) =>
        c.name === "raw_payload"
      ),
      false,
    );
    assertEquals(f.temp.db.get("SELECT response_text FROM ai_runs WHERE request_msg_id=3"), {
      response_text: null,
    });
  } finally {
    await f.temp.close();
  }
});

Deno.test("concurrent chains share a missing download; cancelling one does not cancel the other", async () => {
  const f = await setup();
  let release!: () => void;
  let started!: () => void;
  const ready = new Promise<void>((resolve) => {
    started = resolve;
  });
  const gate = new Promise<void>((resolve) => {
    release = resolve;
  });
  let downloads = 0;
  f.telegram.downloadFile = async (_file, signal) => {
    downloads++;
    started();
    await gate;
    signal?.throwIfAborted();
    return f.telegram.fileBytes;
  };
  try {
    const service = f.createService(), cancel = new AbortController();
    const first = service.handle({ ...f.message(1), signal: cancel.signal }).then(
      () => "ok",
      () => "cancelled",
    );
    await ready;
    const second = service.handle(f.message(2));
    // Let the second chain finish its asynchronous disk lookup and join the in-flight download.
    await new Promise((resolve) => setTimeout(resolve, 20));
    cancel.abort();
    assertEquals(await first, "cancelled");
    release();
    await second;
    assertEquals(downloads, 1);
    assertEquals(f.requests.length, 1);
  } finally {
    release();
    await f.temp.close();
  }
});

Deno.test("identity mismatch, Telegram errors and disk errors never silently omit requested media", async () => {
  for (const failure of ["identity", "telegram", "disk", "size"] as const) {
    const f = await setup();
    try {
      if (failure === "identity") {
        f.telegram.getFile = () => Promise.resolve({ fileId: "photo", fileUniqueId: "wrong" });
      }
      if (failure === "telegram") {
        f.telegram.getFile = () => Promise.reject(new Error("expired file ID"));
      }
      if (failure === "disk") {
        f.store.open = () => Promise.reject(new Deno.errors.PermissionDenied("unreadable cache"));
      }
      if (failure === "size") f.telegram.downloadFile = () => Promise.resolve(new Uint8Array([1]));
      await assertRejects(() => f.createService().handle(f.message(1)));
      assertEquals(f.requests.length, 0);
      assertEquals(f.temp.db.all("SELECT * FROM ai_runs"), []);
    } finally {
      await f.temp.close();
    }
  }
});

Deno.test("Telegram unique IDs yield path-safe independent cache files without content deduplication", async () => {
  const root = await Deno.makeTempDir();
  try {
    const store = new TelegramMediaStore(root), bytes = new Uint8Array([1]);
    for (const fileUniqueId of ["../a", "different"]) {
      await store.put(bytes, {
        fileUniqueId,
        fileId: "download",
        mimeType: "image/jpeg",
        byteSize: 1,
      });
    }
    assertEquals((await Array.fromAsync(Deno.readDir(`${root}/telegram`))).length, 2);
    assertEquals(await store.open("../a"), bytes);
  } finally {
    await Deno.remove(root, { recursive: true });
  }
});

Deno.test("archived historical media explicitly reports unavailability without calling a model", async () => {
  const f = await setup();
  try {
    const service = f.createService();
    await service.handle(f.message(1));
    f.temp.db.run("UPDATE ai_message_media SET file_unique_id=NULL,unavailable=1");
    let notice = "";
    f.telegram.sendMessage = (_chat, text) => {
      notice = text;
      return Promise.resolve({ chatId: -100, messageId: 999, raw: {} });
    };
    await assertRejects(
      () =>
        service.handle({
          ...f.message(2),
          media: [],
          text: "continue",
          reply: { ...f.message(100), media: [], text: "answer" },
        }),
      Error,
      "历史媒体已离线归档",
    );
    assertEquals(notice, "历史媒体已离线归档，无法恢复 Telegram 文件标识");
    assertEquals(f.requests.length, 1);
  } finally {
    await f.temp.close();
  }
});
