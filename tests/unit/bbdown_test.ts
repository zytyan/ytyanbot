import { assertEquals, assertThrows } from "@std/assert";
import { bbdownArgs, bbdownOutput } from "../../src/download/service.ts";

Deno.test("bbdown-go selects audio output and rejects missing or escaping paths", () => {
  assertEquals(
    bbdownArgs("/account.json", "/tmp/download", true, "https://bilibili.com/video/BV1"),
    [
      "--cookie-file",
      "/account.json",
      "-work-dir",
      "/tmp/download",
      "-encoding-priority",
      "HEVC",
      "--audio-only",
      "https://bilibili.com/video/BV1",
    ],
  );
  const bytes = (body: unknown) => new TextEncoder().encode(JSON.stringify(body));
  assertEquals(
    bbdownOutput(bytes({ video_path: "v.mp4", audio_path: "a.m4a" }), "/tmp/download", true),
    "/tmp/download/a.m4a",
  );
  assertThrows(() => bbdownOutput(bytes({ video_path: "v.mp4" }), "/tmp/download", true));
  assertThrows(() => bbdownOutput(bytes({ video_path: "../outside.mp4" }), "/tmp/download", false));
  assertThrows(() => bbdownOutput(bytes({ video_path: "/etc/passwd" }), "/tmp/download", false));
});

Deno.test("cookie discovery skips denied candidates and uses the canonical production path first", async () => {
  const { findBilibiliCookies } = await import("../../src/download/service.ts");
  const checked: string[] = [];
  const stat: typeof Deno.stat = (path) => {
    checked.push(String(path));
    if (String(path) === "blocked") return Promise.reject(new Deno.errors.NotCapable("blocked"));
    return Promise.resolve({ isFile: true } as Deno.FileInfo);
  };
  assertEquals(
    await findBilibiliCookies(undefined, stat),
    "/opt/tgbots/ytyanbot/build/bili_cookies.json",
  );
  assertEquals(checked, ["/opt/tgbots/ytyanbot/build/bili_cookies.json"]);
  assertEquals(
    await findBilibiliCookies(["blocked", "/allowed/account.json"], stat),
    "/allowed/account.json",
  );
});

Deno.test("Bilibili download parsing handles the short-link alias and canonicalizes tracking before cache lookup", async () => {
  const { parseDownloadKey } = await import("../../src/download/service.ts");
  const key = await parseDownloadKey(
    "https://bili2233.cn/test 480p",
    false,
    () =>
      Promise.resolve(
        new Response(null, {
          status: 302,
          headers: {
            location: "https://www.bilibili.com/video/BV1pmJy6mEre?p=1&share_session_id=tracking",
          },
        }),
      ),
  );
  assertEquals(key, {
    url: "https://www.bilibili.com/video/BV1pmJy6mEre?p=1",
    audioOnly: false,
    resolution: 480,
  });
  const { assertRejects } = await import("@std/assert");
  await assertRejects(() => parseDownloadKey("https://www.bilibili.com/read/cv1", false));
});

Deno.test("Bilibili helper failures expose diagnostic categories without credentials or signed URLs", async () => {
  const { BilibiliDownloadError } = await import("../../src/download/service.ts");
  const error = new BilibiliDownloadError(
    1,
    new TextEncoder().encode(
      'app playurl failed: {"code":-101,"access_token":"secret"} https://cdn.example/video?sign=private SESSDATA=secret',
    ),
  );
  assertEquals(error.message.includes("app playurl failed"), true);
  assertEquals(error.message.includes("-101"), true);
  assertEquals(error.message.includes("secret"), false);
  assertEquals(error.message.includes("private"), false);
  assertEquals(error.message.includes("https://"), false);
});
