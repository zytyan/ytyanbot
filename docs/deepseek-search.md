# 最新 DeepSeek 与原生联网搜索（2026-10-14）

实现前刷新 TS `e9cce69dcd08ace3378d2afebb2f818c4094b481`、封存 Go
`archive/go/20261013/codex/ai-db-v2` 的 `d395a28545701a5640d91b1ff7057bee236a3f1e`。 账号 /models
实测返回 DeepSeek V4.1 Flash (`deepseek-flash`) 和 V4 Pro (`deepseek-v4-pro`)。 新版 Flash 支持
text/image，Pro 仅 text。已过期旧 Flash/图片内测菜单索引仍可解析， 映射到新版
Flash；菜单显示去重，Argon 的索引 7 不变，Pro 追加为索引 8。

最新两种模型使用 DeepSeek 的 Anthropic Messages 兼容接口与原生 web_search_20250305
工具，每请求最多三次搜索。复用现有 DeepSeek key，地址从 deepseek-base-url 去掉尾部 /v1 后加
/anthropic/v1；不增加外部搜索账号或自动抓网页。 仅明确触发的 AI
请求提供工具，由模型决定搜索；不新增后台搜索、摘要或付费请求。 使用 API 默认思考设置，同步输出上限
32768 Token，原 15 分钟/五次有界重试策略保留。

仍由 createAiGenerator 中 generateText 执行，不添加 provider 框架。锁定 @ai-sdk/anthropic
4.0.71，与现有 ai SDK 共用 provider-utils 5.0.53 类型。 使用该包已导出的 /internal
AnthropicLanguageModel 和 anthropicTools：主工厂模块在 导入时创建默认实例并读取
ANTHROPIC_BASE_URL，违反单元测试无环境权限；直接实例避免
该行为，且不复制生成协议。该导出属于内部入口，后续升级须重跑兼容验收，不自动升版本。

新响应保存 vercel-ai-messages-v1、provider=deepseek、mode=deepseek-anthropic， 包括 SDK
response.messages、全部 native content、原始 HTTP 字节及服务器搜索结果。 原 Chat Completions 的
native/SDK payload 与旧重放路径保留；首次转协议有意以便携文字/图片重建。
用户可见回复附带去重的最多八个 HTTP(S) 来源链接；全部结果仍保留在 payload。

2026-11-26（检查 TS `7a5d86d9ada2d28dfdbc1ed2e5122b62ce095419`）起，Flash 和 Pro
续聊时发送历史回答正文
和带签名的思考，移除历史联网搜索的调用、结果（含错误）和结构化搜索引用。筛选发生在请求构建时，
对已保存的历史同样生效。本轮仍可发起新的搜索。

Messages input_tokens 不含 cache_read/cache_creation，SDK 返回的 inputTokens 已将 它们汇总；取 SDK
用量，不再叠加。outputTokens 不重复添加 reasoning。搜索请求次数 仍在原始 usage.server_tool_use
中保留。本次 Flash 实测两轮：首轮原生搜索请求两次， 后续成功重放并未再次搜索；第一轮 8884 非缓存 +
384 缓存 = 9268 输入，412 输出。

部署停机后先备份 TS 数据库，再通过既有 AiRepository.setChatModel/setSessionModel 更新旧 DS
默认选择和会话选择；清理旧当前 provider 状态并标记便携重建。历史 Run、消息、 原始 payload、Token
和媒体保持不变。只读比较更新前后 Run 数、Token 与 payload 字节总数，
执行完整性检查并保存私有运行报告；不迁移 schema，不写原 Go 数据库。

依据：[可用模型](https://api-docs.deepseek.com/api/list-models/)、
[Responses 工具限制](https://api-docs.deepseek.com/guides/responses_api/)、
[官方原生搜索实现](https://github.com/deepseek-ai/deepseek-harness/tree/master/packages/web/web-search-deepseek)。
