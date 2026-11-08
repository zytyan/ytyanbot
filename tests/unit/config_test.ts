import { assertEquals } from "@std/assert";
import { parseConfig } from "../../src/config.ts";

Deno.test("Go-style YAML fields and defaults remain compatible", () => {
  const config = parseConfig(`
database-path: /srv/ytyan/main.db
bot-token: token
god: 42
ai-chats: [-1001]
content-moderator:
  account-id: cloudflare-account
  api-token: secret
ocr:
  endpoint: https://azure.example/
  api-key: ocr-secret
`);
  assertEquals(config.databasePath, "/srv/ytyan/main.db");
  assertEquals(config.aiMediaPath, "/srv/ytyan/ai-media");
  assertEquals(config.god, 42);
  assertEquals(config.aiChats, [-1001]);
  assertEquals(config.contentModerator.accountId, "cloudflare-account");
  assertEquals(config.telegramApiUrl, "https://api.telegram.org");
  assertEquals(config.deepSeekBaseUrl, "https://api.deepseek.com");
  assertEquals(config.cliproxyapiKey, "");
  assertEquals(config.cliproxyapiBaseUrl, "http://127.0.0.1:8317");
});

Deno.test("CLIProxyAPI native base URL and credential are independently configured", () => {
  const config = parseConfig(`database-path: /tmp/test.db
gemini-key: official-key
cliproxyapi-key: proxy-key
cliproxyapi-base-url: https://proxy.example/
`);
  assertEquals(config.geminiKey, "official-key");
  assertEquals(config.cliproxyapiKey, "proxy-key");
  assertEquals(config.cliproxyapiBaseUrl, "https://proxy.example");
});
