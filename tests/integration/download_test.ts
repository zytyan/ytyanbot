import { assertEquals, assertRejects } from "@std/assert";
import { DownloadService } from "../../src/download/service.ts";
import { dirname } from "node:path";
import { FakeTelegram, FakeYoutubeRunner, temporaryDatabase } from "./helpers.ts";

Deno.test("same download runs and uploads once while every requester receives media", async () => {
  const temporary = await temporaryDatabase();
  const telegram = new FakeTelegram();
  const runner = new FakeYoutubeRunner();
  try {
    const service = new DownloadService(temporary.db, telegram, runner);
    const key = { url: "https://youtube.com/watch?v=test", audioOnly: false, resolution: 720 };
    await Promise.all([
      service.deliver({ key, chatId: -100, messageId: 1, userId: 1, userName: "A" }),
      service.deliver({ key, chatId: -100, messageId: 2, userId: 2, userName: "B" }),
    ]);
    assertEquals(runner.downloads, 1);
    const videos = telegram.calls.filter((call) => call.method === "video");
    assertEquals(videos.length, 2);
    assertEquals(typeof videos[0]?.media, "object");
    assertEquals(videos[1]?.media, "uploaded-video");
    assertEquals(runner.arguments.includes("res:720"), false);
    assertEquals(runner.arguments.some((value) => value.includes("res:720")), true);
    assertEquals(
      temporary.db.get<{ upload_count: number }>("SELECT upload_count FROM yt_dl_results")
        ?.upload_count,
      1,
    );
  } finally {
    await temporary.close();
  }
});

Deno.test("Bilibili audio uses account JSON and survives optional metadata failure", async () => {
  const temporary = await temporaryDatabase();
  const telegram = new FakeTelegram();
  const temporaryRoot = `${dirname(temporary.path)}/downloads`;
  let workDir = "";
  const runner = {
    async run(command: string, args: readonly string[], options: { cwd?: string }) {
      assertEquals(command, "/usr/bin/bbdown-go");
      assertEquals(args.includes("--audio-only"), true);
      assertEquals(args[1], "/account.json");
      workDir = options.cwd!;
      await Deno.writeFile(`${workDir}/audio.m4a`, new Uint8Array([1]));
      return {
        code: 0,
        stdout: new TextEncoder().encode(JSON.stringify({ audio_path: "audio.m4a" })),
        stderr: new Uint8Array(),
      };
    },
  };
  const fetcher: typeof fetch = () => Promise.reject(new Error("metadata unavailable"));
  try {
    const service = new DownloadService(
      temporary.db,
      telegram,
      runner,
      fetcher,
      () => Promise.resolve("/account.json"),
      temporaryRoot,
    );
    await service.deliver({
      key: { url: "https://bilibili.com/video/BV1", audioOnly: true, resolution: 0 },
      chatId: -100,
      messageId: 1,
      userId: 1,
      userName: "A",
    });
    assertEquals(telegram.calls.filter((call) => call.method === "audio").length, 1);
    assertEquals(dirname(workDir), temporaryRoot);
    await assertRejects(() => Deno.stat(workDir), Deno.errors.NotFound);
  } finally {
    await temporary.close();
  }
});

Deno.test("Bilibili downloads reach local API as file URIs and cache only after successful delivery", async () => {
  const temporary = await temporaryDatabase();
  const paths: string[] = [];
  let downloads = 0;
  let uploads = 0;
  const { Bot } = await import("grammy");
  const { GrammyTelegramGateway } = await import("../../src/telegram/gateway.ts");
  const apiRoot = "http://127.0.0.1:8081";
  const bytes = new Uint8Array([1, 2, 3]);
  const bot = new Bot("fake", {
    client: {
      apiRoot,
      fetch: (async (input: RequestInfo | URL, options?: RequestInit) => {
        const request = new Request(input, options);
        assertEquals(request.headers.get("content-type")?.startsWith("application/json"), true);
        const payload = await request.json();
        const kind = request.url.endsWith("sendAudio") ? "audio" : "video";
        const source = payload[kind];
        if (source.startsWith("file://")) {
          assertEquals(await Deno.readFile(new URL(source)), bytes);
          uploads++;
        } else assertEquals(source, `cached-${kind}`);
        return Response.json({
          ok: true,
          result: {
            message_id: 11,
            date: 1,
            chat: { id: -100, type: "supergroup", title: "test" },
            [kind]: { file_id: `cached-${kind}` },
          },
        });
      }) as never,
    },
  });
  const gateway = new GrammyTelegramGateway(bot, "fake", apiRoot);
  const runner = {
    async run(command: string, args: readonly string[], options: { cwd?: string }) {
      if (command.endsWith("ffprobe")) {
        return { code: 0, stdout: new TextEncoder().encode("{}"), stderr: new Uint8Array() };
      }
      assertEquals(command, "/usr/bin/bbdown-go");
      downloads++;
      const audio = args.includes("--audio-only");
      const filename = audio ? "audio #中文.m4a" : "video #中文.mp4";
      const path = `${options.cwd}/${filename}`;
      paths.push(path);
      await Deno.writeFile(path, bytes);
      return {
        code: 0,
        stdout: new TextEncoder().encode(
          JSON.stringify({ [audio ? "audio_path" : "video_path"]: filename }),
        ),
        stderr: new Uint8Array(),
      };
    },
  };
  try {
    const service = new DownloadService(
      temporary.db,
      gateway,
      runner,
      () => Promise.reject(new Error("optional metadata unavailable")),
      () => Promise.resolve("/fake-account.json"),
    );
    for (const audioOnly of [false, true]) {
      const input = {
        key: { url: "https://bilibili.com/video/BV1", audioOnly, resolution: audioOnly ? 0 : 720 },
        chatId: -100,
        messageId: 1,
        userId: 1,
        userName: "A",
      };
      await service.deliver(input);
      await assertRejects(() => Deno.stat(paths.at(-1)!), Deno.errors.NotFound);
      assertEquals(
        temporary.db.get<{ file_id: string }>(
          "SELECT file_id FROM yt_dl_results WHERE audio_only=?",
          audioOnly ? 1 : 0,
        )?.file_id,
        `cached-${audioOnly ? "audio" : "video"}`,
      );
      // A fresh service must reuse the persisted ID even after local media is gone.
      const restarted = new DownloadService(temporary.db, gateway, {
        run() {
          throw new Error("cached delivery must not invoke download or probe helpers");
        },
      });
      await restarted.deliver(input);
    }
    assertEquals(downloads, 2);
    assertEquals(uploads, 2);
    assertEquals(
      temporary.db.all("SELECT file_id,upload_count FROM yt_dl_results ORDER BY audio_only"),
      [{ file_id: "cached-video", upload_count: 1 }, { file_id: "cached-audio", upload_count: 1 }],
    );
  } finally {
    await temporary.close();
  }
});

Deno.test("Bilibili helper failure leaves no cache or upload and cleans temporary files", async () => {
  const { BilibiliDownloadError } = await import("../../src/download/service.ts");
  const temporary = await temporaryDatabase();
  const telegram = new FakeTelegram();
  const temporaryRoot = `${dirname(temporary.path)}/downloads`;
  let workDir = "";
  const runner = {
    run(_command: string, _args: readonly string[], options: { cwd?: string }) {
      workDir = options.cwd!;
      return Promise.resolve({
        code: 1,
        stdout: new Uint8Array(),
        stderr: new TextEncoder().encode("all candidates failed: no space left on device"),
      });
    },
  };
  try {
    const service = new DownloadService(
      temporary.db,
      telegram,
      runner,
      () => Promise.reject(new Error("metadata unavailable")),
      () => Promise.resolve("/account.json"),
      temporaryRoot,
    );
    await assertRejects(
      () =>
        service.deliver({
          key: { url: "https://bilibili.com/video/BV1", audioOnly: false, resolution: 720 },
          chatId: -100,
          messageId: 1,
          userId: 1,
          userName: "A",
        }),
      BilibiliDownloadError,
      "服务器下载目录空间不足",
    );
    assertEquals(dirname(workDir), temporaryRoot);
    assertEquals(temporary.db.all("SELECT * FROM yt_dl_results"), []);
    assertEquals(telegram.calls.filter((call) => call.method === "video").length, 0);
    await assertRejects(() => Deno.stat(workDir), Deno.errors.NotFound);
  } finally {
    await temporary.close();
  }
});
