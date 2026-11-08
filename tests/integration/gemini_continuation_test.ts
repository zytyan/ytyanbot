import { assert, assertEquals, assertRejects } from "@std/assert";
import { AiRepository } from "../../src/ai/repository.ts";
import { AiService, type IncomingAiMessage } from "../../src/ai/service.ts";
import { createAiGenerator } from "../../src/ai/providers.ts";
import { mediaPath, TelegramMediaStore } from "../../src/ai/media_store.ts";
import { parseConfig } from "../../src/config.ts";
import { FakeTelegram, temporaryDatabase } from "./helpers.ts";

async function fixture() {
  const temp = await temporaryDatabase(), repository = new AiRepository(temp.db);
  const store = new TelegramMediaStore(`${temp.path}.media`), telegram = new FakeTelegram();
  let cache = false, mode: "success" | "expired" | "native" | "auth" = "success";
  const config = () =>
    parseConfig(
      `database-path: ${temp.path}\nai-chats: [-100]\ngemini-key: mock\ngemini-explicit-cache: ${cache}\n`,
    );
  const calls: Array<{ url: string; body: Record<string, unknown> }> = [];
  let downloads = 0, opens = 0, notice = "";
  const getFile = telegram.getFile.bind(telegram), open = store.open.bind(store);
  telegram.getFile = (id) => {
    downloads++;
    return getFile(id);
  };
  store.open = (id) => {
    opens++;
    return open(id);
  };
  telegram.sendMessage = (_chat, text) => {
    notice = text;
    return Promise.resolve({ chatId: -100, messageId: 900, raw: {} });
  };
  const fetcher: typeof fetch = (url, init) => {
    const body = JSON.parse(String(init?.body));
    calls.push({ url: String(url), body });
    if (
      mode === "auth" || (mode === "expired" && body.previous_interaction_id) ||
      (mode === "native" && String(url).endsWith("/interactions"))
    ) {
      return Promise.resolve(
        Response.json({
          error: { message: mode === "auth" ? "invalid key" : "expired interaction" },
        }, { status: mode === "auth" ? 401 : 404 }),
      );
    }
    return Promise.resolve(Response.json(
      String(url).endsWith("/interactions")
        ? {
          id: `remote-${calls.length}`,
          status: "completed",
          steps: [{
            type: "thought",
            signature: "AQID",
            summary: [{ type: "text", text: "thought" }],
          }, { type: "model_output", content: [{ type: "text", text: "answer" }] }],
          usage: { total_input_tokens: 11, total_output_tokens: 3 },
        }
        : {
          candidates: [{
            content: { role: "model", parts: [{ text: "answer", thoughtSignature: "AQID" }] },
            finishReason: "STOP",
          }],
          usageMetadata: { promptTokenCount: 11, candidatesTokenCount: 3 },
        },
    ));
  };
  const create = () =>
    new AiService(
      repository,
      createAiGenerator(config, fetcher, () => Promise.resolve()),
      telegram,
      store,
      config,
      { id: 99, firstName: "bot", username: "bot" },
    );
  const message = (messageId: number): IncomingAiMessage => ({
    chatId: -100,
    chatName: "test",
    chatType: "supergroup",
    messageId,
    userId: 7,
    username: "user",
    sentAt: messageId,
    text: "@bot",
    media: [],
  });
  await create().handle({
    ...message(1),
    media: [{
      fileId: "photo",
      uniqueId: "unique-photo",
      kind: "image",
      mimeType: "image/jpeg",
      byteSize: 3,
    }],
  });
  assertEquals(repository.loadProviderState(1).geminiInteractionId, "remote-1");
  downloads = opens = 0;
  cache = true; // Existing Interactions must remain linked even with explicit caching enabled.
  const followup = { ...message(2), text: "continue", reply: { ...message(100), text: "answer" } };
  return {
    temp,
    repository,
    store,
    telegram,
    calls,
    create,
    followup,
    message,
    setMode: (value: typeof mode) => {
      mode = value;
    },
    counts: () => ({ downloads, opens }),
    notice: () => notice,
    archiveMedia: () =>
      temp.db.run("UPDATE ai_message_media SET file_unique_id=NULL,unavailable=1"),
  };
}

Deno.test("persisted Gemini ID continues after restart without opening archived media or invoking cache APIs", async () => {
  const f = await fixture();
  try {
    f.archiveMedia();
    await f.create().handle(f.followup);
    assertEquals(f.counts(), { downloads: 0, opens: 0 });
    assertEquals(f.calls.length, 2);
    assertEquals(f.calls[1]!.body.previous_interaction_id, "remote-1");
    assertEquals((f.calls[1]!.body.input as unknown[]).length, 1);
    assert(!JSON.stringify(f.calls[1]!.body.input).includes("AQID"));
    assert(typeof f.calls[1]!.body.system_instruction === "string");
    assert(f.calls[1]!.body.system_instruction.includes("name: bot"));
    assertEquals(f.repository.loadProviderState(1).geminiInteractionId, "remote-2");
    assertEquals(f.temp.db.get("SELECT status FROM ai_runs WHERE request_msg_id=2"), {
      status: "delivered",
    });
  } finally {
    await f.temp.close();
  }
});

Deno.test("linked Gemini still uploads newly supplied media while skipping old archived media", async () => {
  const f = await fixture();
  try {
    f.archiveMedia();
    await f.create().handle({
      ...f.followup,
      media: [{
        fileId: "new-photo",
        uniqueId: "unique-new-photo",
        kind: "image",
        mimeType: "image/jpeg",
        byteSize: 3,
      }],
    });
    assertEquals(f.counts().downloads, 1);
    assertEquals(f.calls[1]!.body.previous_interaction_id, "remote-1");
    assert(JSON.stringify(f.calls[1]!.body.input).includes("AQID"));
  } finally {
    await f.temp.close();
  }
});

Deno.test("expired Gemini ID triggers media restoration once before full Interactions/native recovery", async () => {
  for (const mode of ["expired", "native"] as const) {
    const f = await fixture();
    try {
      await Deno.remove(`${f.store.root}/${mediaPath("unique-photo")}`);
      f.setMode(mode);
      await f.create().handle(f.followup);
      assertEquals(f.counts().downloads, 1);
      assertEquals(f.calls[1]!.body.previous_interaction_id, "remote-1");
      assertEquals(f.calls[2]!.body.previous_interaction_id, undefined);
      assert(JSON.stringify(f.calls[2]!.body.input).includes("AQID"));
      assertEquals(f.calls.length, mode === "expired" ? 3 : 4);
      if (mode === "native") assert(JSON.stringify(f.calls[3]!.body.contents).includes("AQID"));
    } finally {
      await f.temp.close();
    }
  }
});

Deno.test("expired ID with irrecoverable media reports the problem without sending incomplete full history", async () => {
  const f = await fixture();
  try {
    f.archiveMedia();
    f.setMode("expired");
    await assertRejects(() => f.create().handle(f.followup), Error, "历史媒体已离线归档");
    assertEquals(f.calls.length, 2); // Initial turn and the rejected delta, no text-only rebuild.
    assertEquals(f.notice(), "历史媒体已离线归档，无法恢复 Telegram 文件标识");
  } finally {
    await f.temp.close();
  }
});

Deno.test("authentication errors do not load full history or retry linked Gemini", async () => {
  const f = await fixture();
  try {
    f.archiveMedia();
    f.setMode("auth");
    await assertRejects(() => f.create().handle(f.followup));
    assertEquals(f.calls.length, 2);
    assertEquals(f.counts(), { downloads: 0, opens: 0 });
  } finally {
    await f.temp.close();
  }
});

Deno.test("window mismatch and provider/model switching require complete media rather than a stale remote ID", async () => {
  for (const change of ["window", "model", "provider"] as const) {
    const f = await fixture();
    try {
      f.archiveMedia();
      if (change === "window") {
        f.temp.db.run(
          "UPDATE ai_session_provider_state SET state_json=json_set(state_json,'$.windowStartMessageId',999)",
        );
      } else {f.repository.setSessionModel(
          1,
          change === "provider" ? "cliproxyapi" : "gemini",
          "gemini-3.8-flash-high",
          2,
        );}
      await assertRejects(() => f.create().handle(f.followup), Error, "历史媒体已离线归档");
      assertEquals(f.calls.length, 1);
    } finally {
      await f.temp.close();
    }
  }
});
