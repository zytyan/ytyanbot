import { assertEquals } from "@std/assert";
import { createApplication } from "../../src/app.ts";
import { ConfigStore, parseConfig } from "../../src/config.ts";
import { StatsService } from "../../src/stats/service.ts";
import { temporaryDatabase } from "./helpers.ts";

Deno.test("AI activity precedes slow traditional work and a hanging reaction cannot block generation", async () => {
  const temp = await temporaryDatabase();
  const original = globalThis.fetch;
  const redirect = Promise.withResolvers<Response>();
  const entered = Promise.withResolvers<void>();
  let typings = 0, reactions = 0, models = 0;
  let reactionSignal: AbortSignal | undefined;
  const chat = { id: -100, type: "supergroup", title: "test" } as const;
  const identity = { id: 99, is_bot: true, first_name: "bot", username: "test_bot" } as const;
  globalThis.fetch = (input) => {
    const url = String(input);
    if (url.includes("b23.tv")) {
      entered.resolve();
      return redirect.promise;
    }
    if (url.endsWith("sendRichMessage")) {
      return Promise.resolve(
        Response.json({ ok: true, result: { chat, message_id: 8, date: 1, from: identity } }),
      );
    }
    models++;
    return Promise.resolve(
      Response.json({
        candidates: [{
          content: { role: "model", parts: [{ text: "done" }] },
          finishReason: "STOP",
        }],
        usageMetadata: { promptTokenCount: 3, candidatesTokenCount: 2 },
      }),
    );
  };
  try {
    temp.db.run(
      "INSERT INTO ai_chat_settings VALUES(-100,'cliproxyapi','gemini-3.8-flash-high',0,1)",
    );
    new StatsService(temp.db).setConfigField(-100, "auto_cvt_bili", true);
    const cfg = parseConfig(
      `database-path: ${temp.path}\nbot-token: fake\nai-chats: [-100]\ncliproxyapi-key: fake\n`,
    );
    const app = await createApplication(temp.db, new ConfigStore(cfg, "unused"), (bot) => {
      bot.api.config.use((_previous, method, _payload, signal) => {
        if (method === "setMessageReaction") {
          reactions++;
          reactionSignal = signal as unknown as AbortSignal;
          return new Promise((_resolve, reject) => {
            reactionSignal!.addEventListener("abort", () => reject(reactionSignal!.reason), {
              once: true,
            });
          });
        }
        if (method === "sendChatAction") typings++;
        const result = method === "getMe"
          ? identity
          : method === "sendMessage"
          ? { chat, message_id: 7, date: 1, from: identity }
          : true;
        return Promise.resolve({ ok: true, result } as Awaited<ReturnType<typeof _previous>>);
      });
    });
    const work = app.bot.handleUpdate({
      update_id: 980001,
      message: {
        chat,
        message_id: 1,
        date: Math.floor(Date.now() / 1000),
        from: { id: 7, is_bot: false, first_name: "user" },
        text: "@test_bot https://b23.tv/slow",
      },
    });
    await entered.promise;
    assertEquals(typings, 1);
    assertEquals(reactions, 1);
    assertEquals(models, 0);
    redirect.resolve(
      new Response(null, {
        status: 302,
        headers: { location: "https://bilibili.com/video/BV1test?tracking=1" },
      }),
    );
    // The unresolved reaction used to block generation; its bounded abort must not gate this request.
    await work;
    assertEquals(models, 1);
    assertEquals(reactions, 1);
    assertEquals(reactionSignal!.aborted, true);
    assertEquals(
      temp.db.get<{ status: string }>("SELECT status FROM ai_runs")!.status,
      "delivered",
    );
    app.flush();
  } finally {
    redirect.resolve(
      new Response(null, {
        status: 302,
        headers: { location: "https://bilibili.com/video/BV1test" },
      }),
    );
    globalThis.fetch = original;
    await temp.close();
  }
});
