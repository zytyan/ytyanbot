# CPA Gemini 4 入口（2026-11-13）

初始检查基线：`8d615d26fe86d93053db1e1d273d5362919153ea`；CPA ID 修正基线：
`47ac7ae2678beaba13d4092a6f2566f918df8f9b`，在 TypeScript master 开发。

菜单追加 `Gemini 4 Hydrogen XHigh（CPA）`（`gemini-4-hydrogen-xhigh`）和
`Gemini 4 Argon High（CPA）`（`gemini-4-argon-high`），`aimodel3` 索引分别为 12、13。 沿用 CPA
独立配置及 Vercel Google SDK 的 `/v1beta/models/...:generateContent` 通道； 不使用官方 Gemini 缓存或
Interactions。CPA 模型不额外指定思考等级，使用代理发布的 high/xhigh 别名；官方 Argon 仍使用独立的
`gemini-4-argon` ID 和 medium 配置。

移除 Gemini 3.1 Flash-Lite 菜单项，原索引 2 留空并拒绝旧回调，其他模型索引保持不变。
既有会话与历史数据保留；官方 Argon 入口仍独立存在。

离线拦截回归覆盖菜单、精确模型路由、CPA 凭据及官方状态隔离、两轮签名重放和用量统计。 已只读查询生产
CPA `/v1/models`，确认上述两个精确 ID 存在，无后缀的 `gemini-4-hydrogen`、`gemini-4-argon` 不在 CPA
列表中，CPA 请求拒绝这两个误用 ID。
未调用真实模型生成，列表可见不代表真实生成验收通过。实际部署记录保存在生产 `run/deploy-gemini4-*`。

验证：`deno task check` 通过；`deno task test` 通过（117 个单元测试、65 个集成测试）。
