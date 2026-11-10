# CPA Opus 5.5（2026-11-10）

检查基线：`ad4eb31ff1d014e603fc7524c131fc531538c154`，在 TypeScript master 开发。

菜单末尾追加 `Claude Opus 5.5（CPA）`，精确模型 ID 为 `claude-opus-5-5-high`， `aimodel3` 索引为
11，既有索引和默认模型保持不变。沿用 CPA 独立配置与 Vercel Anthropic SDK 的 `/v1/messages`
通道；图片/PDF、签名续聊、原始必要 payload 和用量统计与既有 Sonnet 路径一致，不使用官方 Gemini
缓存或 Interactions。 不新增思考开关、强制工具调用或自动切换模型。

模型 ID 依据：[官方说明](https://platform.claude.com/docs/en/models/opus-5-5/whats-new-opus-5-5)。
部署前本机 CPA `/v1/models` 返回 `claude-opus-5-5-high`，因此使用该精确 ID；
未通过真实生成验证账号额度。拦截式回归覆盖图片、两轮签名重放、状态隔离及用量统计。
