import type { Context } from "grammy";
import type { MainDatabase } from "./db/database.ts";
import { SQL } from "./db/queries.ts";
import { messageMarkdown } from "./telegram/markdown.ts";
import { topicId } from "./telegram/topic.ts";
import { replyPrivately } from "./telegram/ephemeral.ts";

export interface TraditionalFact {
  feature: string;
  request_msg_id: number;
  reply_msg_id: number | null;
  status: "success" | "failure";
  context_text: string;
  created_at: number;
}

export function interaction(db: MainDatabase, ctx: Context) {
  const message = ctx.message!;
  const topic = topicId(message);
  const facts: TraditionalFact[] = [];
  let replyId: number | null = null;
  return {
    facts,
    async reply(text: string, options: Parameters<Context["reply"]>[1] = {}) {
      const reply = await ctx.reply(text, {
        reply_parameters: { message_id: message.message_id },
        ...options,
      });
      replyId = reply.message_id;
      return reply;
    },
    async notice(text: string) {
      const reply = await replyPrivately(ctx, text);
      replyId = reply?.message_id || null;
      return reply;
    },
    aiContext(
      feature: string,
      text: string,
      status: "success" | "failure" = "success",
      responseId: number | null = replyId,
    ) {
      const fact: TraditionalFact = {
        feature,
        request_msg_id: message.message_id,
        reply_msg_id: responseId,
        status,
        context_text: feature === "traditional" ? text : `请求文本：${messageMarkdown(message)}
处理结果：${text}`,
        created_at: Math.floor(Date.now() / 1000),
      };
      if (!message.ephemeral_message_id) {
        db.run(
          SQL.SaveFact,
          feature,
          message.chat.id,
          topic,
          fact.request_msg_id,
          responseId,
          status,
          fact.context_text,
          fact.created_at,
          responseId === null ? "failed" : "delivered",
        );
      }
      facts.push(fact);
    },
  };
}
export type Interaction = ReturnType<typeof interaction>;

export function isAiTrigger(text: string, username: string, repliedToBot: boolean): boolean {
  // Exclude commands; explicit mentions and bot replies trigger regardless of text length.
  if (text.startsWith("/")) return false;
  return text.includes("@new") || text.includes(`@${username}`) || repliedToBot;
}

export function factText(facts: readonly TraditionalFact[]): string {
  return facts.length ? `\n[传统功能事实（上下文数据）]\n${JSON.stringify(facts)}` : "";
}
