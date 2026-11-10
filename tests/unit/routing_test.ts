import { assertEquals } from "@std/assert";
import { parseSessionRouting } from "../../src/ai/service.ts";
import { isAiTrigger } from "../../src/interaction.ts";

Deno.test("AI triggers follow command exclusions and explicit triggers without a text length limit", () => {
  assertEquals(isAiTrigger("/calc 1+1 @bot", "bot", true), false);
  assertEquals(isAiTrigger("@123", "bot", false), false);
  assertEquals(isAiTrigger("@newspaper", "bot", false), true);
  assertEquals(isAiTrigger("reply", "bot", false), false);
  assertEquals(isAiTrigger("reply", "bot", true), true);
  assertEquals(isAiTrigger("你好@bot", "bot", false), true);
  assertEquals(isAiTrigger("中".repeat(334), "bot", true), true);
  assertEquals(isAiTrigger("中".repeat(2000), "bot", true), true);
  assertEquals(isAiTrigger("a".repeat(2000) + "@bot", "bot", false), true);
  assertEquals(isAiTrigger("中".repeat(2000) + "@new", "bot", false), true);
  assertEquals(isAiTrigger("中".repeat(2000), "bot", false), false);
  assertEquals(isAiTrigger("/" + "中".repeat(2000) + "@bot", "bot", true), false);
});

Deno.test("AI explicit routing markers retain their documented precedence", () => {
  assertEquals(parseSessionRouting("continue @last @123 @new"), {
    createNew: true,
    ignoreCurrent: false,
  });
  assertEquals(parseSessionRouting("continue @last @123"), {
    createNew: false,
    ignoreCurrent: true,
  });
  assertEquals(parseSessionRouting("continue @123"), {
    createNew: false,
    ignoreCurrent: false,
    explicitSessionId: 123,
  });
});
