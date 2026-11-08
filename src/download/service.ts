import type { MainDatabase } from "../db/database.ts";
import { Semaphore } from "../shared/concurrency.ts";
import type { ProcessRunner, SentMessage, TelegramPort } from "../shared/types.ts";
import { convertBilibiliLinks, hasBilibiliVideo } from "./bilibili.ts";
import { type DownloadNotice, withDownloadNotice } from "./notice.ts";
import { isAbsolute, resolve } from "node:path";
import { latencyStage } from "../shared/latency.ts";

const DOWNLOAD_URL =
  /(?:(?:bilibili\.com|www\.bilibili\.com|b23\.tv|bili2233\.cn|youtu\.be|youtube\.com)\/[a-zA-Z\d_%.~:/?#[\]@!$&'()*+,;=\-]+)/i;
const RESOLUTION = /\b(144|360|480|720|1080)p?\b/i;
const decoder = new TextDecoder();

// Helper output may contain account data and signed media URLs. Retain only
// recognized diagnostic categories and numeric upstream codes in logs/notices.
export class BilibiliDownloadError extends Error {
  constructor(code: number, stderr: Uint8Array) {
    const output = decoder.decode(stderr);
    const stages = [
      "invalid account fields",
      "invalid account object",
      "invalid cookie JSON object",
      "app playurl failed",
      "view api failed",
      "ffmpeg",
      "permission denied",
      "no such file or directory",
      "no space left on device",
      "unexpected eof",
      "timeout",
      "connection refused",
    ].filter((stage) => output.toLowerCase().includes(stage));
    const upstream = output.match(/(?:code["']?\s*[:=]\s*|HTTP\s+)(-?\d{1,6})\b/i)?.[1];
    super(
      output.toLowerCase().includes("no space left on device")
        ? "下载失败：服务器下载目录空间不足，请联系管理员释放空间。"
        : `B站下载工具失败（退出码 ${code}${stages.length ? `；${stages.join(" / ")}` : ""}${
          upstream ? `；上游状态 ${upstream}` : ""
        }），请稍后重试或联系管理员检查下载工具及登录状态。`,
    );
    this.name = "BilibiliDownloadError";
  }
}

export interface DownloadKey {
  readonly url: string;
  readonly audioOnly: boolean;
  readonly resolution: number;
}

interface DownloadRecord {
  url: string;
  audio_only: number;
  resolution: number;
  file_id: string;
  title: string;
  description: string;
  uploader: string;
  upload_count: number;
}

interface Downloaded {
  readonly key: DownloadKey;
  readonly filePath: string;
  readonly title: string;
  readonly description: string;
  readonly uploader: string;
  readonly cleanup: () => Promise<void>;
}

interface VideoProbe {
  readonly duration?: number;
  readonly width?: number;
  readonly height?: number;
}

function escapeHtml(value: string): string {
  return value.replaceAll("&", "&amp;").replaceAll("<", "&lt;").replaceAll(">", "&gt;").replaceAll(
    '"',
    "&quot;",
  );
}

function uploadedFileId(message: SentMessage, kind: "audio" | "video"): string | undefined {
  const raw = message.raw as Record<string, unknown>;
  const media = raw[kind] as Record<string, unknown> | undefined;
  return typeof media?.file_id === "string" ? media.file_id : undefined;
}

async function firstMediaFile(root: string, audioOnly: boolean): Promise<string> {
  const allowed = audioOnly
    ? [".mp3", ".m4a", ".ogg", ".wav"]
    : [".mp4", ".mkv", ".flv", ".ts", ".avi", ".webm"];
  for await (const entry of Deno.readDir(root)) {
    const path = `${root}/${entry.name}`;
    if (entry.isDirectory) {
      try {
        return await firstMediaFile(path, audioOnly);
      } catch {
        continue;
      }
    }
    if (allowed.some((extension) => entry.name.toLowerCase().endsWith(extension))) return path;
  }
  throw new Error(`${root} 目录中没有文件`);
}

export async function findBilibiliCookies(
  paths: readonly string[] = [
    "/opt/tgbots/ytyanbot/build/bili_cookies.json",
    "bili_cookies.json",
    "build/bili_cookies.json",
    "../ytyanbot/build/bili_cookies.json",
  ],
  stat: typeof Deno.stat = Deno.stat,
): Promise<string> {
  for (const path of paths) {
    try {
      if ((await stat(path)).isFile) return resolve(path);
    } catch (error) {
      if (
        !(error instanceof Deno.errors.NotFound) && !(error instanceof Deno.errors.NotCapable) &&
        !(error instanceof Deno.errors.PermissionDenied)
      ) throw error;
    }
  }
  throw new Error("找不到 Bilibili 账户文件 bili_cookies.json");
}

export function bbdownArgs(
  cookieFile: string,
  workDir: string,
  audioOnly: boolean,
  url: string,
): string[] {
  return [
    "--cookie-file",
    cookieFile,
    "-work-dir",
    workDir,
    "-encoding-priority",
    "HEVC",
    ...(audioOnly ? ["--audio-only"] : []),
    url,
  ];
}

export function bbdownOutput(output: Uint8Array, workDir: string, audioOnly: boolean): string {
  const body = JSON.parse(decoder.decode(output)) as Record<string, unknown>;
  const key = audioOnly ? "audio_path" : "video_path";
  if (typeof body[key] !== "string" || !body[key]) throw new Error(`bbdown-go 未返回 ${key}`);
  const path = isAbsolute(body[key]) ? body[key] : resolve(workDir, body[key]);
  const root = resolve(workDir);
  if (!path.startsWith(`${root}/`)) throw new Error("bbdown-go 返回的文件不在下载目录内");
  return path;
}

export async function parseDownloadKey(
  text: string,
  audioOnly: boolean,
  fetcher: typeof fetch = fetch,
): Promise<DownloadKey> {
  const found = text.match(DOWNLOAD_URL)?.[0];
  if (!found) throw new Error("未找到支持的视频链接");
  let url = `https://${found}`;
  if (
    ["b23.tv", "bili2233.cn", "bilibili.com", "www.bilibili.com", "m.bilibili.com"].includes(
      new URL(url).hostname,
    )
  ) {
    const converted = await convertBilibiliLinks(url, fetcher);
    if (!converted.hasBv || !hasBilibiliVideo(converted.bvText)) {
      throw new Error("链接不是可下载的B站视频");
    }
    url = converted.bvText;
  }
  return { url, audioOnly, resolution: audioOnly ? 0 : Number(text.match(RESOLUTION)?.[1] ?? 720) };
}

export interface DeliveryRequest {
  readonly key: DownloadKey;
  readonly chatId: number;
  readonly messageId: number;
  readonly userId: number;
  readonly userName: string;
  readonly signal?: AbortSignal;
  readonly notice?: DownloadNotice;
}

export class DownloadService {
  readonly #semaphore = new Semaphore(5);
  readonly #inflight = new Map<
    string,
    Promise<{ record: DownloadRecord; response: SentMessage }>
  >();

  constructor(
    readonly db: MainDatabase,
    readonly telegram: TelegramPort,
    readonly process: ProcessRunner,
    readonly fetcher: typeof fetch = fetch,
    readonly cookieFile: () => Promise<string> = findBilibiliCookies,
    readonly temporaryRoot?: string,
  ) {}

  async deliver(input: DeliveryRequest): Promise<SentMessage> {
    return await withDownloadNotice(
      this.telegram,
      input.chatId,
      input.messageId,
      () => this.#deliver(input),
      input.signal,
      1000,
      input.notice,
    );
  }

  async #deliver(input: DeliveryRequest): Promise<SentMessage> {
    const cached = this.#find(input.key);
    if (cached?.file_id) {
      const sent = await this.#sendCached(cached, input);
      this.db.run(
        "UPDATE yt_dl_results SET upload_count=upload_count+1 WHERE file_id=?",
        cached.file_id,
      );
      return sent;
    }
    const flightKey = this.#key(input.key);
    const running = this.#inflight.get(flightKey);
    if (running) {
      const { record } = await latencyStage("queue.download_singleflight", () => running);
      const sent = await this.#sendCached(record, input);
      this.db.run(
        "UPDATE yt_dl_results SET upload_count=upload_count+1 WHERE file_id=?",
        record.file_id,
      );
      return sent;
    }
    const leader = this.#downloadAndUpload(input);
    this.#inflight.set(flightKey, leader);
    try {
      return (await leader).response;
    } finally {
      if (this.#inflight.get(flightKey) === leader) this.#inflight.delete(flightKey);
    }
  }

  async #downloadAndUpload(
    input: DeliveryRequest,
  ): Promise<{ record: DownloadRecord; response: SentMessage }> {
    const downloaded = await this.#semaphore.use(
      () => latencyStage("download.fetch", () => this.#download(input.key, input.signal)),
      "queue.download",
    );
    try {
      const probe = input.key.audioOnly
        ? {}
        : await this.#probeVideo(downloaded.filePath, input.signal);
      const sent = await this.#sendFile(downloaded, input, probe);
      const id = uploadedFileId(sent, input.key.audioOnly ? "audio" : "video");
      if (!id) throw new Error("Telegram 上传媒体后未返回文件 ID");
      this.db.run(
        `INSERT INTO yt_dl_results(url,audio_only,resolution,file_id,title,description,uploader,upload_count)
VALUES(?,?,?,?,?,?,?,0) ON CONFLICT(url,audio_only,resolution) DO UPDATE SET
file_id=excluded.file_id,title=excluded.title,description=excluded.description,uploader=excluded.uploader`,
        input.key.url,
        input.key.audioOnly ? 1 : 0,
        input.key.resolution,
        id,
        downloaded.title,
        downloaded.description,
        downloaded.uploader,
      );
      return {
        response: sent,
        record: {
          url: input.key.url,
          audio_only: input.key.audioOnly ? 1 : 0,
          resolution: input.key.resolution,
          file_id: id,
          title: downloaded.title,
          description: downloaded.description,
          uploader: downloaded.uploader,
          upload_count: 0,
        },
      };
    } finally {
      await downloaded.cleanup();
    }
  }

  async checkExecutables(): Promise<void> {
    const results = await Promise.all([
      this.process.run("/usr/local/bin/yt-dlp", ["--version"], { timeoutMs: 10_000 }),
      this.process.run("/usr/bin/bbdown-go", ["--help"], { timeoutMs: 10_000 }),
      this.process.run("/usr/bin/ffprobe", ["-version"], { timeoutMs: 10_000 }),
    ]);
    if (results.some((result) => result.code !== 0)) {
      throw new Error("download helper executable check failed");
    }
  }

  cleanupInline(now: number): void {
    this.db.run("DELETE FROM bili_inline_results WHERE created_at<?", now - 30 * 24 * 3600);
  }

  createInlineContext(now: number): bigint {
    this.db.run(
      "INSERT INTO bili_inline_results(text,chat_id,msg_id,created_at) VALUES('',0,0,?)",
      now,
    );
    return BigInt(
      this.db.get<{ id: string }>("SELECT CAST(last_insert_rowid() AS TEXT) AS id")!.id,
    );
  }

  updateInlineContext(uid: bigint, text: string, chatId: number, messageId: number): void {
    this.db.run(
      "UPDATE bili_inline_results SET text=?,chat_id=?,msg_id=? WHERE uid=?",
      text,
      chatId,
      messageId,
      uid,
    );
  }

  getInlineContext(
    uid: bigint,
    now: number,
  ): { text: string; chatId: number; messageId: number } | undefined {
    const row = this.db.get<{ text: string; chat_id: number; msg_id: number }>(
      "SELECT text,chat_id,msg_id FROM bili_inline_results WHERE uid=? AND created_at>=?",
      uid,
      now - 30 * 24 * 3600,
    );
    return row ? { text: row.text, chatId: row.chat_id, messageId: row.msg_id } : undefined;
  }

  #key(key: DownloadKey): string {
    return `${key.audioOnly}:${key.resolution}:${key.url}`;
  }

  #find(key: DownloadKey): DownloadRecord | undefined {
    return this.db.get<DownloadRecord>(
      "SELECT * FROM yt_dl_results WHERE url=? AND audio_only=? AND resolution=?",
      key.url,
      key.audioOnly ? 1 : 0,
      key.resolution,
    );
  }

  async #download(key: DownloadKey, signal?: AbortSignal): Promise<Downloaded> {
    if (this.temporaryRoot) await Deno.mkdir(this.temporaryRoot, { recursive: true, mode: 0o700 });
    const temporary = await Deno.makeTempDir({ dir: this.temporaryRoot, prefix: "ytdlp-" });
    try {
      if (key.url.includes("bilibili.com") || key.url.includes("b23.tv")) {
        const cookies = await this.cookieFile();
        const [result, metadata] = await Promise.allSettled([
          this.process.run(
            "/usr/bin/bbdown-go",
            bbdownArgs(cookies, temporary, key.audioOnly, key.url),
            { cwd: temporary, timeoutMs: 30 * 60_000, signal },
          ),
          this.#bilibiliInfo(key.url, signal),
        ]);
        if (result.status === "rejected") throw result.reason;
        if (result.value.code !== 0) {
          throw new BilibiliDownloadError(result.value.code, result.value.stderr);
        }
        const info = metadata.status === "fulfilled"
          ? metadata.value
          : { title: "未知标题", uploader: "未知上传者", description: "" };
        return {
          key,
          filePath: bbdownOutput(result.value.stdout, temporary, key.audioOnly),
          ...info,
          cleanup: () => Deno.remove(temporary, { recursive: true }),
        };
      }
      const output = `${temporary}/%(title).150B.%(ext)s`;
      const args = [
        key.url,
        "-o",
        output,
        "--quiet",
        "--print",
        "after_move:%(filepath)j",
        "--windows-filenames",
        "--write-info-json",
        "--format",
        "bestvideo+bestaudio/best",
        "--format-sort",
        `+vcodec:h264:h265:av01,+acodec:flac:alac:wav:aiff:aac:mp4a:mp3,res:${
          key.resolution || 1080
        }`,
        "--embed-metadata",
        "--embed-info-json",
        "--add-metadata",
      ];
      if (key.audioOnly) {
        args.push("--extract-audio", "--audio-format", "mp3", "--audio-quality", "0");
      }
      const result = await this.process.run("/usr/local/bin/yt-dlp", args, {
        cwd: temporary,
        timeoutMs: 30 * 60_000,
        signal,
      });
      if (result.code !== 0) throw new Error(`yt-dlp failed: ${decoder.decode(result.stderr)}`);
      let filePath: string;
      try {
        filePath = JSON.parse(decoder.decode(result.stdout).trim());
      } catch {
        filePath = await firstMediaFile(temporary, key.audioOnly);
      }
      const dot = filePath.lastIndexOf(".");
      let info: Record<string, unknown> = {};
      try {
        info = JSON.parse(await Deno.readTextFile(`${filePath.slice(0, dot)}.info.json`));
      } catch {
        // Metadata is optional.
      }
      return {
        key,
        filePath,
        title: String(info.title ?? "未知标题"),
        uploader: String(info.uploader ?? "未知上传者"),
        description: String(info.description ?? ""),
        cleanup: () => Deno.remove(temporary, { recursive: true }),
      };
    } catch (error) {
      await Deno.remove(temporary, { recursive: true });
      throw error;
    }
  }

  async #bilibiliInfo(
    url: string,
    signal?: AbortSignal,
  ): Promise<{ title: string; uploader: string; description: string }> {
    const match = url.match(/\/(BV[\da-zA-Z]+|av\d+)/i);
    if (!match) return { title: "未知标题", uploader: "未知上传者", description: "-" };
    const token = match[1]!;
    const endpoint = /^av/i.test(token)
      ? `https://api.bilibili.com/x/web-interface/view?aid=${token.slice(2)}`
      : `https://api.bilibili.com/x/web-interface/view?bvid=${token}`;
    const deadline = AbortSignal.timeout(10_000);
    const response = await this.fetcher(endpoint, {
      signal: signal ? AbortSignal.any([signal, deadline]) : deadline,
    });
    if (!response.ok) throw new Error(`Bilibili metadata HTTP ${response.status}`);
    const body = await response.json() as { code: number; data?: Record<string, unknown> };
    if (body.code !== 0 || !body.data) throw new Error(`Bilibili metadata code ${body.code}`);
    const owner = body.data.owner as Record<string, unknown> | undefined;
    return {
      title: String(body.data.title ?? "未知标题"),
      uploader: String(owner?.name ?? "未知上传者"),
      description: String(body.data.desc ?? ""),
    };
  }

  async #probeVideo(filePath: string, signal?: AbortSignal): Promise<VideoProbe> {
    const result = await this.process.run("/usr/bin/ffprobe", [
      "-v",
      "error",
      "-show_entries",
      "format=duration:stream=width,height",
      "-of",
      "json",
      filePath,
    ], { timeoutMs: 30_000, signal });
    if (result.code !== 0) return {};
    try {
      const body = JSON.parse(decoder.decode(result.stdout)) as {
        format?: { duration?: string };
        streams?: Array<{ width?: number; height?: number }>;
      };
      const stream = body.streams?.find((item) => item.width && item.height);
      const duration = Math.round(Number(body.format?.duration));
      return {
        duration: Number.isFinite(duration) && duration > 0 ? duration : undefined,
        width: stream?.width,
        height: stream?.height,
      };
    } catch {
      return {};
    }
  }

  #caption(
    record: Pick<DownloadRecord, "url" | "title" | "uploader" | "description">,
    input: DeliveryRequest,
  ): string {
    const header = `<b><a href="${escapeHtml(record.url)}">${escapeHtml(record.title)}</a></b>\n` +
      `上传者: ${escapeHtml(record.uploader)}\n<a href="tg://user?id=${input.userId}">${
        escapeHtml(input.userName)
      }</a>\n`;
    const remaining = Math.max(0, 1000 - header.length - 36);
    return record.description
      ? `${header}<blockquote expandable>${
        escapeHtml(record.description).slice(0, remaining)
      }</blockquote>`
      : header;
  }

  #sendCached(record: DownloadRecord, input: DeliveryRequest): Promise<SentMessage> {
    const options = {
      caption: this.#caption(record, input),
      parse_mode: "HTML",
      reply_parameters: { message_id: input.messageId },
    };
    return input.key.audioOnly
      ? this.telegram.sendAudio(input.chatId, record.file_id, options)
      : this.telegram.sendVideo(input.chatId, record.file_id, options);
  }

  #sendFile(
    downloaded: Downloaded,
    input: DeliveryRequest,
    probe: VideoProbe,
  ): Promise<SentMessage> {
    const options = {
      caption: this.#caption({ url: downloaded.key.url, ...downloaded }, input),
      parse_mode: "HTML",
      reply_parameters: { message_id: input.messageId },
      ...probe,
    };
    const source = { path: downloaded.filePath };
    return input.key.audioOnly
      ? this.telegram.sendAudio(input.chatId, source, options)
      : this.telegram.sendVideo(input.chatId, source, options);
  }
}
