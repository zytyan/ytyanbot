# 简化架构与 AI SDK 评估

## 基线

本次读取与远程核对的最新代码：

- TS `codex/retain-features`：`b991db7396befea259ae139dd422546cfa557d81`。
- Go `codex/ai-db-v2`：`9b52b20188c592f736987eca97d9ece65aa43a41`（当前最新开发提交）。
- Go `master`：`7a73f0e79e7698f94439382e839618f085b81ef3`；开发分支另有极小数格式修复。

不再以旧的 `b6376...` 冻结提交制定目标。上面是本次核对记录，不是新的永久冻结基线；
每次实现切片应刷新相关分支 HEAD。原 Go 工作树未 checkout 或合并。

## 结论

统一多家模型的候选是 **Vercel AI SDK**，不是旧的 Google Vertex AI SDK。 用户已确认采用 Vercel AI
SDK；运行时切片已完成，详见下方实施记录。没有切换到 Vertex 云平台。

旧 `@google-cloud/vertexai` 生成式 AI 接口已进入官方退役迁移范围，官方方向是 `@google/genai`。
Google Gen AI SDK 可访问 Gemini Developer API/Vertex AI，但不是现有独立 DeepSeek、Sub2API
接口的跨厂商通用协议。 不为了“统一 SDK”把现有 API 供应商和计费渠道搬到 Vertex AI。
依据：[Google 迁移说明](https://docs.cloud.google.com/vertex-ai/generative-ai/docs/deprecations/genai-vertexai-sdk)、
[Google Gen AI SDK](https://github.com/googleapis/js-genai)。

Vercel AI SDK 的统一生成调用有 Google/DeepSeek provider，适合消除本项目的手写协议与响应转换；
业务状态仍由本项目保存。无需引入 Agent、UI hooks、Next.js、服务容器或多层 provider 注册框架。
依据：[providers](https://ai-sdk.dev/docs/foundations/providers-and-models)。

## 评估时的具体抽象问题

- `src/shared/types.ts` 把 Telegram、media、process、clock 的行为端口集中在一起，92 行；
  单实现端口可改为具体对象或少量可替换函数，数据结构类型不是删除目标。
- `GrammyTelegramGateway` 116 行，许多方法只是转发 grammY；普通命令直接用 ctx/API。 保留 RichMessage
  窄扩展、必要消息身份/显式事实关联，不复制整套 Telegram API。
- `AiProvider` 和 `DeepSeekProvider/GeminiProvider` 是自定义行为契约；`providers.ts` 478 行，
  手写编码/HTTP/响应解析多。若用 SDK，直接采用 SDK 模型实例和消息类型，不再套自建 provider 接口。
- `ProviderState` 把 Gemini Interaction/cache 字段放进通用状态；改成按 provider 区分的少量状态，
  不要求 DeepSeek/Sub2API 存一堆无效 Gemini 字段。
- `src/app.ts` 682 行，同时混合注册、权限、输出、路由和定时任务；拆成少数功能模块，
  而不是为了拆文件添加 controller/service/use-case/port/adapter 层层传递。
- repository 当前确实封装 AI 状态事务，有存在价值；新 schema 的业务查询直接集中， 不再增加
  IRepository、generic repository 或额外 UnitOfWork。
- SHA-256
  媒体校验、事务、取消/进程超时、singleflight、会话互斥都有真实消费者，不应作为“抽象过多”一起删除。
  测试可以传入 fetch/runProcess/now 等少量函数，不必为每个函数造接口和实现类。

## 建议层级

`Telegram 功能与顺序编排 → 直接数据库查询 / SDK / 外部工具`

跨功能仅保留：

- 一次请求的薄 Interaction 对象：reply()、aiContext() 和消息身份关联。
- 一个 AI 执行入口：选择回复链、冻结请求、调用 SDK、保存结果、交付。
- 可见的事务与安全工具函数：解决可靠性，不发展成框架。

传统功能注入上下文不是模型工具调用，不能因为 SDK 有 tools/Agent 就改成模型自主执行传统功能。

## 接入方案

| 渠道                | 候选                            | 必须保留的区别                                                              |
| ------------------- | ------------------------------- | --------------------------------------------------------------------------- |
| 官方 Gemini         | Vercel Google provider          | generateContent/Interactions 分开；官方缓存/远端状态隔离                    |
| Sub2API             | 同一 Google provider 的独立实例 | 自定义 baseURL、独立凭证、3.7 Flash → tiered 映射；只走原生 generateContent |
| DeepSeek            | Vercel DeepSeek provider        | 图片能力按所选模型判断；维持当前最新 Go 的 active user image 行为           |
| Gemini 特殊管理接口 | 必要时少量 @google/genai 调用   | 显式缓存生命周期等不是统一 generateText 自动解决；禁止未触发时后台付费调用  |

官方 Google provider 支持自定义 baseURL/fetch、cachedContent、Interactions 与 provider metadata。
显式缓存创建示例仍使用 Google Gen AI SDK；不能把“传缓存名”等同于“管理缓存生命周期”。
依据：[Google provider](https://ai-sdk.dev/providers/ai-sdk-providers/google)。

DeepSeek provider 有自己的 provider options；模型名称能序列化不代表实验模型目前仍对账号开放。
依据：[DeepSeek provider](https://ai-sdk.dev/providers/ai-sdk-providers/deepseek)。

## 最新正式版本的离线验证

以下是实施前的 /tmp 探针记录；随后已锁定前三项到 deno.json/deno.lock 并固化仓库测试。 npm registry
发布记录：

- ai 7.0.127
- @ai-sdk/google 4.0.87
- @ai-sdk/deepseek 3.0.58
- @google/genai 2.27.0（仅查询版本，未用它做 mock）

实际用 Deno 2.9.3 + 前三项 mock：

1. Sub2API 请求精确发往 `/antigravity/v1beta/models/gemini-3.7-flash-tiered:generateContent`。
2. Gemini 模拟响应的 thoughtSignature 在 response.messages 中保留，下一轮原生请求正确重放。
3. DeepSeek vision-exp 的图片序列化为 image_url/data URI；这是序列化测试，不是模型可用性验证。
4. 两者均正确返回 cached input 和 reasoning 用量：11 input、4 cached、3 text + 2 reasoning → 5
   output。 **不要再把 reasoning 加到 outputTokens，否则重复计算。**
5. 最新 generateText 默认不附带原始 response body；需 `include: { responseBody: true }`。
   持久化可重放 assistant messages（含 provider options）以及原始 provider payload，不能只存 text。
   如果需要 HTTP 原字节而非解析后的 JSON，可用窄 fetch hook 克隆响应保存，避免新建 transport 框架。
   依据：[generateText](https://ai-sdk.dev/docs/reference/ai-sdk-core/generate-text)。
6. Deno 可运行这些 mock，但 ai 根入口的间接 @vercel/oidc 导入读取 hostname： 需
   `--allow-sys=hostname`；无网络、无 allow-env 复跑也通过。现有 compile/test
   权限必须单独调整和验收， 不能直接认为 SDK 在旧权限集下即插即用。
7. 使用当前 file/image MIME 形式输入，避免沿用 deprecated image part。

没有真实 Gemini/DeepSeek/Sub2API、cache 创建、Google Interactions 远端链或生产迁移验收。
Interactions 的本地旧 steps payload 到 SDK
新消息格式仍须做导入/重放测试；不能只依据文档宣布完全兼容。

## 实施记录

本次实施前重新核对：TS HEAD/远程 `28afaa3a21488fc1daa3cd2dba5ce065b4e17060`， Go
HEAD/远程开发分支仍为 `9b52b20188c592f736987eca97d9ece65aa43a41`。 这只是检查记录，下一切片继续刷新
HEAD。

- 已移除 AiProvider 接口、GeminiProvider/DeepSeekProvider 类与注册 Map。 一个 createAiGenerator
  配置闭包直接创建 SDK 模型并调用 generateText，不加代理框架。
- 三通道及 provider/model 联合选择已接通；回调 aimodel2 使用稳定选项索引避免 Telegram 64-byte
  限制，旧模型菜单明确提示重新打开。Sub2API 映射 tiered，只走原生路径。
- 新 assistant 保存 vercel-ai-messages-v1：provider、mode、SDK response.messages 与 native payload；
  TS V6 不再额外持久化 raw_payload 原始 HTTP 副本；保留必要的回放 payload。旧原生
  content/steps/DeepSeek message 在相同协议下精确重放；
  明确的供应商/模型切换依旧使用可移植历史重建。无需新建 transport/legacy provider 类。
- 新官方 Gemini 会话开启缓存时使用原生生成；已存在 Interactions 历史继续原协议。 Interactions opaque
  steps 不被压成显示文字创建缓存。生成与缓存只在 AI 执行入口调用，
  不增加后台摘要/缓存请求；整个传统事实触发编排仍待后续切片。
- 缓存保留 4096-token、稳定五分钟、900 秒 TTL 策略；新增 model/prompt/tools/content
  指纹，修正整个未缓存尾部发送与失效有限回退。生命周期端点保留两个小型 HTTP 调用，未额外引入 Google
  Gen AI SDK；generateText 无对应的缓存管理接口。
- DeepSeek 按模型选择图片能力，对齐最新 Go 的连续图片/文字合并与最近图片放入 active user 行为。 SDK
  输入验收不等于相册、完整回复链与上下文引用加载都已验收，这些仍在新持久化/编排切片中。
- 最初验收为 maxRetries=0、180 秒；2026-10-13 用户改为 15 分钟总期限，瞬时错误退避重试。
  生成调用与协议恢复共享最多五次尝试，SDK maxRetries=0 防止叠加；401/403 不重试。
  只有缓存失效、远端链失效或接口兼容错误有显式有限回退。reasoning 不重复计入 outputTokens。
- 保留原 Run 状态/计量/交付事务，没有修改业务 SQL 或 schema。SDK 到临时数据库的集成测试覆盖 pending
  先落库、生成保存、交付失败重发不二次生成/计量、RichMessage 与消息身份。
- 格式/lint/类型检查、62 项测试通过；hostname 最小权限的独立 Linux 二进制 CLI 和临时 V9
  db-init/db-check smoke 通过。没有真实供应商调用、生产数据库操作或服务重启。

## 后续切片

SDK 切片之后：

- 真实测试账号验证 Gemini/Interactions/Sub2API/DeepSeek/显式缓存；实验模型不依据名称假定可用。
- 扩展真实旧数据副本的导入/重放验收；旧原始 payload 保留，不自动覆盖或删除。
- 再按新 schema/导入计划重组持久化与 aiContext；不要在一次提交里同时改 SDK、数据库和全 bot 架构。

SDK 净收益主要是手写协议减少和模型消息统一；不预先承诺整个仓库缩减比例。

## 2026-10-14 DeepSeek 原生搜索补充

最新账号模型列表提供 deepseek-flash（V4.1 Flash，图片）与 deepseek-v4-pro。 两者通过现有
createAiGenerator 中的 Vercel generateText 调用 DeepSeek Anthropic 兼容 Messages 和原生
web_search，具体协议重建、SDK 最小权限与真实验收见
[DeepSeek 搜索接入](deepseek-search.md)。继续保留旧 DeepSeek Chat/native 重放路径和历史数据，
不引入搜索框架、Agent 或新服务账号。

## 2026-11-07：移除 Sub2API，增加 CPA

检查基线：`3ef4c3801eaf3d05e608c1f587ebee5b504504fa`（TypeScript master）。 本节取代上文 Sub2API
的运行时接口约定；历史记录和导入 payload 不改写或删除。

- 原有官方 Gemini、DeepSeek 菜单保留，仅删除 Sub2API 入口；新菜单版本为 `aimodel3`，
  旧版本要求重新打开，防止索引变化导致选择错误模型。
- `cliproxyapi` 为独立 provider，配置服务根地址和独立 key。仅开放 `claude-sonnet-4-6` /
  `gemini-3.8-flash-high`，显示名均带 `CPA`。
- 直接使用现有 Anthropic/Google SDK：分别走 `/v1/messages`、
  `/v1beta/models/gemini-3.8-flash-high:generateContent`，不新增 provider 层。
- Claude 使用 `anthropic` 协议标签，保留 SDK 消息、providerOptions 和必要 native content； Gemini
  使用 `generateContent` 标签，保留签名和工具 payload。CPA 不使用官方 cache/Interactions。
- 保持总计 15 分钟、最多五次生成、SDK maxRetries=0 及原有持久化/投递事务。
- CLIProxyAPI
  路由依据：[上游路由源码](https://github.com/router-for-me/CLIProxyAPI/blob/main/internal/api/server.go)。
  离线回归不等于已验证真实代理中的模型可用性。

2026-11-07 内部 CPA 联调：`/v1/models` 实际发布 `claude-sonnet-4-6` 与 `gemini-3.8-flash-high`，因此
Gemini 使用后者精确 ID，显示名仍为 `Gemini 3.8 Flash（CPA）`。 两个模型各通过两轮真实 SDK
生成，验证独立凭证、协议、SDK payload 续聊及用量统计，未发送 Telegram 测试消息。Sonnet
两轮输入/输出分别为 49/4、87/4；Gemini 为 48/112、409/113， Gemini 输出已包含
reasoning，不额外累加。完整离线回归为 112 项 unit、61 项 integration。
