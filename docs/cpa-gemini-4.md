# CPA Gemini 4 入口（2026-11-13）

检查基线：`8d615d26fe86d93053db1e1d273d5362919153ea`，在 TypeScript master 开发。

菜单追加 `Gemini 4 Hydrogen（CPA）`（`gemini-4-hydrogen`）和
`Gemini 4 Argon（CPA）`（`gemini-4-argon`），`aimodel3` 索引分别为 12、13。 沿用 CPA 独立配置及
Vercel Google SDK 的 `/v1beta/models/...:generateContent` 通道； 不使用官方 Gemini 缓存或
Interactions。Argon 沿用 medium 思考配置，Hydrogen 不额外指定思考等级。

移除 Gemini 3.1 Flash-Lite 菜单项，原索引 2 留空并拒绝旧回调，其他模型索引保持不变。
既有会话与历史数据保留；官方 Argon 入口仍独立存在。

离线拦截回归覆盖菜单、精确模型路由、CPA 凭据及官方状态隔离、两轮签名重放和用量统计。
本次未调用真实模型，入口可选不代表 CPA 账号已开通对应模型；未部署或重启服务。

验证：`deno task check` 通过；`deno task test` 通过（117 个单元测试、65 个集成测试）。
