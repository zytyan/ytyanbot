import { assertEquals, assertStringIncludes } from "@std/assert";
import { createApplication } from "../../src/app.ts";
import { ConfigStore, parseConfig } from "../../src/config.ts";
import { temporaryDatabase } from "./helpers.ts";
import type { Message, Update } from "grammy/types";
import { latencySnapshot } from "../../src/shared/latency.ts";

Deno.test("Go-compatible bot replies trigger AI after traditional commands; facts remain user context", async () => {
  const temporary = await temporaryDatabase();
  const originalFetch = globalThis.fetch;
  const chat = { id: -100, type: "supergroup", title: "test" } as const;
  const botUser = { id: 99, is_bot: true, first_name: "bot", username: "test_bot" } as const;
  const user = { id: 7, is_bot: false, first_name: "user" } as const;
  let nextId = 100;
  let modelCalls = 0;
  let fileDownloads = 0;
  let typingCalls = 0;
  const sent: Message[] = [];
  let lastBody: Record<string, unknown> = {};
  globalThis.fetch = (_input, options) => {
    const url = String(_input);
    if (url.includes("/file/botfake/")) {
      fileDownloads++;
      return Promise.resolve(new Response(new Uint8Array([1, 2, 3])));
    }
    if (url.endsWith("/sendRichMessage")) {
      const payload = JSON.parse(String(options?.body));
      const message = {
        message_id: nextId++,
        date: 1,
        chat,
        from: botUser,
        text: payload.rich_message.markdown,
      } as Message;
      sent.push(message);
      return Promise.resolve(Response.json({ ok: true, result: message }));
    }
    modelCalls++;
    lastBody = JSON.parse(String(options?.body));
    assertEquals(
      temporary.db.get<{ count: number }>("SELECT COUNT(*) AS count FROM traditional_facts")!
        .count > 0,
      true,
    );
    assertEquals(
      temporary.db.get("SELECT name FROM sqlite_master WHERE name='ai_run_context'"),
      undefined,
    );
    return Promise.resolve(
      Response.json({
        candidates: [{
          content: { role: "model", parts: [{ text: "answer" }] },
          finishReason: "STOP",
        }],
        usageMetadata: { promptTokenCount: 3, candidatesTokenCount: 2 },
      }),
    );
  };
  try {
    const cfg = parseConfig(
      `database-path: ${temporary.path}\nbot-token: fake\nai-chats: [-100]\ncliproxyapi-key: fake\n`,
    );
    temporary.db.run(
      "INSERT INTO ai_chat_settings VALUES(-100,'cliproxyapi','gemini-3.8-flash-high',0,1)",
    );
    const app = await createApplication(
      temporary.db,
      new ConfigStore(cfg, "unused"),
      (bot) =>
        bot.api.config.use((_previous, method, payload) => {
          let result: unknown = true;
          if (method === "getMe") {
            result = {
              ...botUser,
              can_join_groups: true,
              can_read_all_group_messages: true,
              supports_inline_queries: true,
            };
          } else if (method === "getFile") {
            const id = (payload as { file_id: string }).file_id;
            result = {
              file_id: id,
              file_unique_id: id.replace("photo-", "uid-"),
              file_path: `photos/${id}`,
            };
          } else if (method === "sendMessage") {
            const message = {
              message_id: nextId++,
              date: 1,
              chat,
              from: botUser,
              text: (payload as { text: string }).text,
            } as Message;
            sent.push(message);
            result = message;
          } else if (method === "sendChatAction") {
            typingCalls++;
            assertEquals((payload as { action: string }).action, "typing");
          } else if (!["setMessageReaction"].includes(method)) {
            throw new Error(`unexpected API ${method}`);
          }
          return Promise.resolve({ ok: true, result } as Awaited<ReturnType<typeof _previous>>);
        }),
    );
    const update = (id: number, text: string, reply?: Message, extra: object = {}): Update => ({
      update_id: id,
      message: {
        message_id: nextId++,
        date: 1,
        chat,
        from: user,
        text,
        reply_to_message: reply as never,
        // Ordinary supergroup replies can carry a thread ID without being forum topics.
        message_thread_id: reply?.message_id,
        entities: text.startsWith("/")
          ? [{ type: "bot_command", offset: 0, length: text.split(" ")[0]!.length }]
          : undefined,
        ...extra,
      },
    });
    await app.bot.handleUpdate(update(1, "ordinary chat"));
    assertEquals(latencySnapshot().recent.some((t) => t.update_id === 1), false);
    assertEquals(temporary.db.all("SELECT * FROM ai_messages"), []);
    await app.bot.handleUpdate(update(2, "/roll 2 2"));
    assertEquals(modelCalls, 0);
    assertEquals(typingCalls, 0);
    const traditional = sent.at(-1)!;
    await app.bot.handleUpdate(update(3, "hello", traditional));
    assertEquals(modelCalls, 1);
    assertEquals(typingCalls, 1);
    await app.bot.handleUpdate(update(4, "@test_bot explain", traditional));
    assertEquals(modelCalls, 2);
    assertStringIncludes(JSON.stringify(lastBody.contents), "掷出了2");
    assertEquals(JSON.stringify(lastBody.systemInstruction).includes("掷出了2"), false);
    await app.bot.handleUpdate(update(5, "/roll 3 3 @test_bot"));
    assertEquals(modelCalls, 2);
    await app.bot.handleUpdate(update(6, "continue", sent.at(-1)!));
    assertEquals(modelCalls, 3);
    assertStringIncludes(JSON.stringify(lastBody.contents), "掷出了3");
    await app.bot.handleUpdate(update(7, "/calc 1/3+1/6 @test_bot"));
    assertEquals(modelCalls, 3);
    await app.bot.handleUpdate(update(8, "explain this calculation", sent.at(-1)!));
    assertEquals(modelCalls, 4);
    assertStringIncludes(JSON.stringify(lastBody.contents), "1/3+1/6 = 0.5");
    const calculationAnswer = sent.at(-1)!;
    assertStringIncludes(JSON.stringify(lastBody.contents), "/calc 1/3+1/6 @test_bot");
    const albumMessage = (id: number, caption: string) =>
      ({
        message_id: id,
        date: Math.floor(Date.now() / 1000),
        chat,
        from: user,
        caption,
        media_group_id: "test-album",
        photo: [{ file_id: `photo-${id}`, file_unique_id: `uid-${id}`, width: 400, height: 400 }],
      }) as Message;
    const ordinary = albumMessage(901, "ordinary private album caption");
    await Promise.all([
      app.bot.handleUpdate({ update_id: 900, message: albumMessage(900, "@test_bot album") }),
      app.bot.handleUpdate({ update_id: 901, message: ordinary }),
      app.bot.handleUpdate({ update_id: 902, message: albumMessage(902, "") }),
    ]);
    assertEquals(modelCalls, 5);
    assertEquals(fileDownloads, 3);
    assertEquals(JSON.stringify(lastBody).includes("ordinary private album caption"), false);
    assertEquals(temporary.db.all("SELECT * FROM ai_message_media").length, 3);
    nextId = 1000;
    // Telegram may omit media_group_id in the embedded replied-to photo.
    await app.bot.handleUpdate(
      update(903, "@test_bot inspect all", { ...ordinary, media_group_id: undefined }),
    );
    assertEquals(modelCalls, 6);
    assertEquals(fileDownloads, 3); // Reuses the Telegram identity cache.
    const ordinaryAnswer = sent.at(-1)!;
    const latestSession = () =>
      temporary.db.get<{ session_id: number }>(
        "SELECT session_id FROM ai_runs ORDER BY id DESC LIMIT 1",
      )!.session_id;
    const ordinarySession = latestSession();
    const forum = { is_topic_message: true, message_thread_id: 55 };
    await app.bot.handleUpdate(update(904, "cross topic", ordinaryAnswer, forum));
    assertEquals(modelCalls, 7);
    assertEquals(latestSession(), ordinarySession);
    await app.bot.handleUpdate(update(905, "@test_bot forum conversation", undefined, forum));
    assertEquals(modelCalls, 8);
    const forumAnswer = { ...sent.at(-1)!, ...forum };
    const forumSession = latestSession();
    await app.bot.handleUpdate(update(906, "continue forum", forumAnswer, forum));
    assertEquals(modelCalls, 9);
    assertStringIncludes(JSON.stringify(lastBody.contents), "forum conversation");
    await app.bot.handleUpdate(
      update(907, "wrong topic", forumAnswer, { ...forum, message_thread_id: 56 }),
    );
    await app.bot.handleUpdate(update(908, "outside topic", forumAnswer));
    assertEquals(modelCalls, 11);
    assertEquals(latestSession(), forumSession);
    await app.bot.handleUpdate(update(909, "@test_bot see documentation", undefined, {
      entities: [{ type: "text_link", offset: 14, length: 13, url: "https://example.com/manual" }],
    }));
    assertEquals(modelCalls, 12);
    assertStringIncludes(JSON.stringify(lastBody.contents), "https://example.com/manual");
    await app.bot.handleUpdate(update(910, "continue link discussion", sent.at(-1)!));
    assertEquals(modelCalls, 13);
    assertStringIncludes(JSON.stringify(lastBody.contents), "https://example.com/manual");
    await app.bot.handleUpdate(
      update(911, `@test_bot @${forumSession} resume across topic`, undefined, {
        ...forum,
        message_thread_id: 56,
      }),
    );
    assertEquals(modelCalls, 14);
    assertEquals(latestSession(), forumSession);
    await app.bot.handleUpdate(update(912, "中".repeat(2000), sent.at(-1)!));
    assertEquals(modelCalls, 15);
    assertStringIncludes(JSON.stringify(lastBody.contents), "中".repeat(2000));
    await app.bot.handleUpdate(update(913, "remember the calculation", calculationAnswer));
    assertEquals(modelCalls, 16);
    const probe = latencySnapshot().recent.find((r) => r.update_id === 913)!;
    assertEquals(probe.measurements.history_messages! >= 4, true);
    assertEquals(probe.measurements.context_snapshot_chars, undefined);
    assertEquals(probe.measurements.request_body_chars! > 0, true);
    assertEquals(probe.measurements.response_body_bytes! > 0, true);
    assertEquals(probe.measurements.assistant_payload_bytes! > 0, true);
    for (
      const stage of [
        "ai.history.sql",
        "ai.history.media",
        "provider.request.parse",
        "provider.request.stringify",
        "provider.history.replay",
        "provider.history.build",
        "ai.persist.pending",
        "ai.persist.generated",
        "ai.persist.delivered",
        "provider.response.parse",
        "provider.payload.encode",
      ]
    ) {
      assertEquals(probe.stages[stage]!.calls, 1);
    }
    assertStringIncludes(JSON.stringify(lastBody.contents), "/calc 1/3+1/6 @test_bot");
    assertStringIncludes(JSON.stringify(lastBody.contents), "1/3+1/6 = 0.5");
    assertEquals(JSON.stringify(lastBody.systemInstruction).includes("1/3+1/6"), false);
    app.flush();
  } finally {
    globalThis.fetch = originalFetch;
    await temporary.close();
  }
});
