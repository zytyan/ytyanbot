import { assertEquals, assertStringIncludes } from "@std/assert";
import type { Message } from "grammy/types";
import { messageMarkdown } from "../../src/telegram/markdown.ts";

const message = (fields: object) =>
  ({ message_id: 1, date: 1, chat: { id: 1, type: "private" }, ...fields }) as Message;

Deno.test("AI Markdown preserves UTF-16 entities, nested emphasis and hidden link destinations", () => {
  assertEquals(
    messageMarkdown(message({
      text: "🌍 see docs",
      entities: [
        { type: "bold", offset: 3, length: 8 },
        { type: "italic", offset: 3, length: 3 },
        { type: "text_link", offset: 7, length: 4, url: "https://example.com/a(b)" },
      ],
    })),
    "🌍 **_see_ [docs](https://example.com/a\\(b\\))**",
  );
  assertEquals(
    messageMarkdown(
      message({ text: "plain * text", entities: [{ type: "mention", offset: 0, length: 5 }] }),
    ),
    "plain * text",
  );
});

Deno.test("AI Markdown retains captions, quoted lines and literal fenced code", () => {
  assertEquals(
    messageMarkdown(message({
      caption: "one\ntwo",
      caption_entities: [{ type: "blockquote", offset: 0, length: 7 }],
    })),
    "\n> one\n> two\n",
  );
  const code = "print(```)";
  assertStringIncludes(
    messageMarkdown(message({
      text: code,
      entities: [{ type: "pre", offset: 0, length: code.length, language: "python" }],
    })),
    "````python\nprint(```)\n````",
  );
  assertEquals(
    messageMarkdown(message({
      text: "`x`",
      entities: [{ type: "code", offset: 0, length: 3 }],
    })),
    "`` `x` ``",
  );
});

Deno.test("Telegram dice context describes the game and the server-provided result", () => {
  for (
    const [emoji, value, description] of [
      ["🎲", 3, "🎲 掷骰子，结果：3 点"],
      ["⚽", 4, "⚽ 足球射门，结果：进球（Telegram 数值 4）"],
      ["⚽", 3, "⚽ 足球射门，结果：未进球（Telegram 数值 3）"],
      ["🏀", 5, "🏀 篮球投篮，结果：投进（Telegram 数值 5）"],
      ["🏀", 2, "🏀 篮球投篮，结果：未投进（Telegram 数值 2）"],
      ["🎯", 6, "🎯 投飞镖，结果：命中靶心（Telegram 数值 6）"],
      ["🎳", 6, "🎳 保龄球，结果：全中（Telegram 数值 6）"],
      ["🎰", 58, "🎰 转动老虎机，结果：葡萄 · 柠檬 · 7"],
      ["🎰", 64, "🎰 转动老虎机，结果：7 · 7 · 7（头奖）"],
      ["🆕", 42, "🆕 Telegram 随机动画，结果数值：42"],
    ] as const
  ) {
    assertEquals(messageMarkdown(message({ dice: { emoji, value } })), description);
  }
  assertEquals(messageMarkdown(message({ text: "⚽" })), "⚽");
});

Deno.test("received rich blocks preserve nested text, links, tables, captions and code", () => {
  const rich = message({
    rich_message: {
      blocks: [
        { type: "heading", size: 2, text: "Report" },
        {
          type: "paragraph",
          text: ["🌍 ", {
            type: "bold",
            text: ["see ", { type: "url", text: "docs", url: "https://example.com/a(b)" }],
          }],
        },
        {
          type: "details",
          summary: "Details",
          blocks: [
            {
              type: "list",
              items: [{ label: "1.", blocks: [{ type: "paragraph", text: "nested content" }] }],
            },
            {
              type: "table",
              cells: [[{ text: "key", align: "left", valign: "top" }, {
                text: "value",
                align: "left",
                valign: "top",
              }]],
              caption: "table caption",
            },
            { type: "pre", language: "python", text: "print(```)" },
            { type: "photo", photo: [], caption: { text: "photo caption", credit: "Author" } },
          ],
        },
      ],
    },
  });
  const rendered = messageMarkdown(rich);
  for (
    const fragment of [
      "## Report",
      "**see [docs](https://example.com/a\\(b\\))**",
      "1. nested content",
      "key | value",
      "table caption",
      "````python\nprint(```)\n````",
      "photo caption\nAuthor",
    ]
  ) {
    assertStringIncludes(rendered, fragment);
  }
});
