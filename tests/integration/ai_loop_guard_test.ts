import { assertEquals } from "@std/assert";
import { AiRepository } from "../../src/ai/repository.ts";
import { AiService, type IncomingAiMessage } from "../../src/ai/service.ts";
import type { AiRequest, AiResult } from "../../src/ai/types.ts";
import { parseConfig } from "../../src/config.ts";
import { ConfigStore } from "../../src/config.ts";
import { createApplication } from "../../src/app.ts";
import { FakeTelegram, temporaryDatabase } from "./helpers.ts";

const result: AiResult = {
  displayText: "answer",
  raw: new Uint8Array([1]),
  assistantPayload: new Uint8Array([2]),
  assistantPayloadFormat: "test",
  usage: { inputTokens: 3, outputTokens: 2, cachedInputTokens: 0, thinkingTokens: 0 },
  state: {},
  inputMessageCount: 1,
};

Deno.test("third consecutive bot submission is stored only; restart, deduplication and human resume preserve history", async () => {
  const temp = await temporaryDatabase();
  try {
    const telegram = new FakeTelegram();
    const requests: AiRequest[] = [];
    const create = () =>
      new AiService(
        new AiRepository(temp.db),
        (request) => {
          requests.push(request);
          return Promise.resolve(result);
        },
        telegram,
        {
          put() {
            throw new Error("unexpected media");
          },
          verify() {
            throw new Error("unexpected media");
          },
          open() {
            throw new Error("unexpected media");
          },
        },
        () => parseConfig("database-path: unused\nai-chats: [-100]"),
        { id: 99, firstName: "bot", username: "bot" },
      );
    let service = create();
    const message = (messageId: number, isBot = true, sessionId?: number): IncomingAiMessage => ({
      chatId: -100,
      chatName: "test",
      chatType: "supergroup",
      messageId,
      sentAt: messageId,
      userId: isBot ? 8 : 7,
      username: isBot ? "other bot" : "human",
      isBot,
      text: `@bot${sessionId ? ` @${sessionId}` : ""}`,
      media: [],
    });
    await service.handle(message(1, false));
    const id = service.currentSessionId(-100)!;
    await service.handle(message(2, true, id));
    // Duplicate delivery does not increment the streak.
    await service.handle(message(2, true, id));
    await service.handle(message(3, true, id));
    assertEquals(requests.length, 3);
    service = create();
    await service.handle(message(4, true, id));
    await service.handle(message(4, true, id));
    await service.handle(message(5, true, id));
    assertEquals(requests.length, 3);
    assertEquals(telegram.calls.filter((c) => c.method === "rich").length, 3);
    assertEquals(service.repository.runForRequest(-100, 4), undefined);
    assertEquals(
      service.repository.loadMessages(id).filter((m) => m.role === "user").map((m) => m.messageId),
      [1, 2, 3, 4, 5],
    );
    await service.handle(message(6, false, id));
    assertEquals(requests.length, 4);
    assertEquals(requests.at(-1)!.newMessages.map((m) => m.messageId), [4, 5, 6]);
    // Previously suppressed updates stay suppressed after the human reset.
    await service.handle(message(4, true, id));
    await service.handle(message(7, true, id));
    await service.handle(message(8, true, id));
    await service.handle(message(9, true, id));
    assertEquals(requests.length, 6);
    assertEquals(temp.db.all("SELECT * FROM ai_runs").length, 6);
    assertEquals(
      temp.db.get<{ total_output_tokens: number }>(
        "SELECT COALESCE(SUM(output_tokens),0) AS total_output_tokens FROM ai_runs WHERE session_id=?",
        id,
      )!.total_output_tokens,
      12,
    );
    // Another session has its own counter, including when no human opened it.
    await service.handle(message(10));
    const other = service.currentSessionId(-100)!;
    await Promise.all([11, 12, 13].map((n) => service.handle(message(n, true, other))));
    assertEquals(requests.length, 8);
    assertEquals(temp.db.validate().foreignKeyIssues, 0);
  } finally {
    await temp.close();
  }
});

Deno.test("Telegram bot identity reaches the loop guard and only human AI triggers reset it", async () => {
  const temp = await temporaryDatabase();
  const original = globalThis.fetch;
  let models = 0;
  const chat = { id: -100, type: "supergroup", title: "test" } as const;
  const identity = { id: 99, is_bot: true, first_name: "bot", username: "test_bot" } as const;
  globalThis.fetch = (input) => {
    if (String(input).endsWith("sendRichMessage")) {
      return Promise.resolve(Response.json({
        ok: true,
        result: {
          chat,
          message_id: 100 + models,
          date: 1,
          from: identity,
        },
      }));
    }
    models++;
    return Promise.resolve(Response.json({
      candidates: [{ content: { role: "model", parts: [{ text: "done" }] }, finishReason: "STOP" }],
      usageMetadata: { promptTokenCount: 3, candidatesTokenCount: 2 },
    }));
  };
  try {
    temp.db.run("INSERT INTO ai_chat_settings VALUES(-100,'sub2api','gemini-3.7-flash',0,1)");
    const cfg = parseConfig(
      `database-path: ${temp.path}\nbot-token: fake\nai-chats: [-100]\nsub2api-key: fake\n`,
    );
    const app = await createApplication(temp.db, new ConfigStore(cfg, "unused"), (bot) => {
      bot.api.config.use((_previous, method) =>
        Promise.resolve({
          ok: true,
          result: method === "getMe" ? identity : true,
        } as Awaited<ReturnType<typeof _previous>>)
      );
    });
    const send = (id: number, isBot: boolean, text: string) =>
      app.bot.handleUpdate({
        update_id: 990000 + id,
        message: {
          chat,
          message_id: id,
          date: Math.floor(Date.now() / 1000),
          from: { id: isBot ? 8 : 7, is_bot: isBot, first_name: isBot ? "other bot" : "human" },
          text,
        },
      });
    await send(1, false, "@test_bot");
    const session = temp.db.get<{ id: number }>("SELECT id FROM ai_sessions")!.id;
    for (const id of [2, 3, 4]) await send(id, true, `@test_bot @${session}`);
    assertEquals(models, 3);
    await send(5, false, "ordinary human chat");
    await send(6, true, `@test_bot @${session}`);
    assertEquals(models, 3);
    assertEquals(temp.db.get("SELECT * FROM ai_messages WHERE msg_id=5"), undefined);
    await send(7, false, `@test_bot @${session}`);
    assertEquals(models, 4);
    app.flush();
  } finally {
    globalThis.fetch = original;
    await temp.close();
  }
});
