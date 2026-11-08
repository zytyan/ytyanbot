import { parse as parseYaml } from "@std/yaml";
import { z } from "zod";
import { dirname, resolve } from "node:path";

const rawConfigSchema = z.object({
  "bot-token": z.string().default(""),
  god: z.number().int().default(0),
  "my-chats": z.array(z.number().int()).default([]),
  "ai-chats": z.array(z.number().int()).default([]),
  "content-moderator": z.object({
    "account-id": z.string().default(""),
    "api-token": z.string().default(""),
  }).default({ "account-id": "", "api-token": "" }),
  ocr: z.object({
    endpoint: z.string().default(""),
    "api-key": z.string().default(""),
    "api-ver": z.string().default("2023-10-01"),
    language: z.string().default(""),
    features: z.string().default("Read"),
  }).default({
    endpoint: "",
    "api-key": "",
    "api-ver": "2023-10-01",
    language: "",
    features: "Read",
  }),
  "tg-api-url": z.string().url().default("https://api.telegram.org"),
  "drop-pending-updates": z.boolean().default(false),
  "log-level": z.number().int().min(-8).max(12).default(0),
  "database-path": z.string().min(1),
  "ai-media-path": z.string().optional(),
  "gemini-key": z.string().default(""),
  "gemini-explicit-cache": z.boolean().default(true),
  "cliproxyapi-key": z.string().default(""),
  "cliproxyapi-base-url": z.string().url().default("http://127.0.0.1:8317"),
  "deepseek-key": z.string().default(""),
  "deepseek-base-url": z.string().url().default("https://api.deepseek.com"),
}).passthrough();

export interface AppConfig {
  readonly botToken: string;
  readonly god: number;
  readonly myChats: readonly number[];
  readonly aiChats: readonly number[];
  readonly contentModerator: { readonly accountId: string; readonly apiToken: string };
  readonly ocr: {
    readonly endpoint: string;
    readonly apiKey: string;
    readonly apiVersion: string;
    readonly language: string;
    readonly features: string;
  };
  readonly telegramApiUrl: string;
  readonly dropPendingUpdates: boolean;
  readonly logLevel: number;
  readonly databasePath: string;
  readonly aiMediaPath: string;
  readonly geminiKey: string;
  readonly geminiExplicitCache: boolean;
  readonly cliproxyapiKey: string;
  readonly cliproxyapiBaseUrl: string;
  readonly deepSeekKey: string;
  readonly deepSeekBaseUrl: string;
}

function env(name: string): string | undefined {
  try {
    const value = Deno.env.get(name);
    return value && value.length > 0 ? value : undefined;
  } catch {
    return undefined;
  }
}

export function configPath(): string {
  return env("YTYAN_CONFIG_FILE") ?? "config.yaml";
}

export function parseConfig(text: string): AppConfig {
  const raw = rawConfigSchema.parse(parseYaml(text) ?? {});
  const databasePath = env("YTYAN_DATABASE_PATH") ?? raw["database-path"];
  const slash = Math.max(databasePath.lastIndexOf("/"), databasePath.lastIndexOf("\\"));
  const databaseDirectory = slash >= 0 ? databasePath.slice(0, slash) : ".";
  return Object.freeze({
    botToken: env("YTYAN_BOT_TOKEN") ?? raw["bot-token"],
    god: raw.god,
    myChats: Object.freeze([...raw["my-chats"]]),
    aiChats: Object.freeze([...raw["ai-chats"]]),
    contentModerator: Object.freeze({
      accountId: raw["content-moderator"]["account-id"],
      apiToken: raw["content-moderator"]["api-token"],
    }),
    ocr: Object.freeze({
      endpoint: raw.ocr.endpoint.replace(/\/$/, ""),
      apiKey: raw.ocr["api-key"],
      apiVersion: raw.ocr["api-ver"],
      language: raw.ocr.language,
      features: raw.ocr.features,
    }),
    telegramApiUrl: raw["tg-api-url"].replace(/\/$/, ""),
    dropPendingUpdates: raw["drop-pending-updates"],
    logLevel: raw["log-level"],
    databasePath,
    aiMediaPath: env("YTYAN_AI_MEDIA_PATH") ?? raw["ai-media-path"] ??
      `${databaseDirectory}/ai-media`,
    geminiKey: env("GEMINI_API_KEY") ?? raw["gemini-key"],
    geminiExplicitCache: raw["gemini-explicit-cache"],
    cliproxyapiKey: env("CLIPROXYAPI_API_KEY") ?? raw["cliproxyapi-key"],
    cliproxyapiBaseUrl: (env("CLIPROXYAPI_BASE_URL") ?? raw["cliproxyapi-base-url"]).replace(
      /\/$/,
      "",
    ),
    deepSeekKey: env("DEEPSEEK_API_KEY") ?? raw["deepseek-key"],
    deepSeekBaseUrl: raw["deepseek-base-url"].replace(/\/$/, ""),
  });
}

export async function loadConfig(path = configPath()): Promise<AppConfig> {
  const parsed = parseConfig(await Deno.readTextFile(path));
  return Object.freeze({
    ...parsed,
    databasePath: parsed.databasePath === ":memory:"
      ? parsed.databasePath
      : resolve(dirname(path), parsed.databasePath),
    aiMediaPath: resolve(dirname(path), parsed.aiMediaPath),
  });
}

export class ConfigStore {
  #value: AppConfig;

  constructor(initial: AppConfig, readonly path: string) {
    this.#value = initial;
  }

  get value(): AppConfig {
    return this.#value;
  }

  async watch(signal: AbortSignal, onError: (error: unknown) => void): Promise<void> {
    const watcher = Deno.watchFs(this.path);
    const stop = () => watcher.close();
    signal.addEventListener("abort", stop, { once: true });
    try {
      for await (const event of watcher) {
        if (signal.aborted) break;
        if (
          !event.paths.includes(this.path) && !event.paths.some((path) => path.endsWith(this.path))
        ) {
          continue;
        }
        try {
          const next = await loadConfig(this.path);
          const current = this.#value;
          if (
            current.databasePath !== next.databasePath ||
            current.aiMediaPath !== next.aiMediaPath ||
            current.botToken !== next.botToken ||
            current.telegramApiUrl !== next.telegramApiUrl
          ) {
            throw new Error("startup-only configuration changed; reload rejected");
          }
          this.#value = next;
        } catch (error) {
          onError(error);
        }
      }
    } catch (error) {
      if (!signal.aborted) throw error;
    } finally {
      signal.removeEventListener("abort", stop);
      try {
        watcher.close();
      } catch {
        // Closing an already aborted watcher is harmless.
      }
    }
  }
}
