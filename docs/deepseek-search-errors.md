# DeepSeek 搜索错误响应兼容（2026-10-17）

实现前刷新检查 TS `e4ff18a37e24361bed0a31040747512f7e4697de`，Go 开发封存分支
`archive/go/20261013/codex/ai-db-v2` 为 `d395a28545701a5640d91b1ff7057bee236a3f1e`。

会话 10698 的请求 151872/151873/151875（Run 48018/48019/48020）连续失败，错误均为 AI_APICallError:
Invalid JSON response。日志实际是 HTTP 200、合法 JSON，SDK 的下层原因是
TypeValidationError/ZodError。完整脱敏样本显示第四次搜索触发 max_uses_exceeded；错误被 DeepSeek 放入
web_search_tool_result.content 的单元素数组，而 Anthropic 协议及当前 SDK
要求错误为单对象，数组仅用于正常搜索结果。响应中已经有最终回答。
协议：https://platform.claude.com/docs/en/agents-and-tools/tool-use/web-search-tool#errors

修复前，新增三种搜索错误的 SDK 回归均复现失败。修复只在成功的 DeepSeek Anthropic 响应入口，
把恰好一个 web_search_tool_result_error（且 error_code 为字符串）的数组展开为该对象。
正常结果数组、原本合法的错误对象、空数组、多项/混合结果、其他协议和非 2xx 响应不做转换。
继续使用现有 Vercel AI SDK，不修改依赖或增加 provider 框架；搜索次数上限仍为三次。

原始响应字节先捕获，始终按原样作为 raw 保存；native assistant payload 同样来自原响应。 仅传给 SDK
的响应副本做格式兼容，并去掉已失效的 content-length/content-encoding/transfer-encoding。 SDK
response messages 保留规范化工具错误、思考签名、成功搜索结果和来源。当前续聊策略见
[DeepSeek 搜索接入](deepseek-search.md)。
保持工具错误语义，不伪造成功搜索，不隐藏其他不合法字段，不因搜索达到上限重新生成。 现有
provider.response.compatibility 探针记录兼容处理耗时，大小指标仍代表原始响应。

回归覆盖 max_uses_exceeded/too_many_requests/unavailable、来源/思考/opaque 数据续传、
原始响应逐字节保留、usage 不变、成功空文本保留计量，以及混合结果保持严格拒绝。 集成验证模型成功后
Telegram 交付失败，重发原结果只调用一次模型、只累计一次用量。 历史失败 Run 不自动重试或改写。

验收：deno task check 与 148 项测试通过；原故障脱敏结构样本直接通过新入口，无需样本预处理。 独立编译
smoke 在生产用户及原受限权限下运行，两轮 DeepSeek 搜索错误响应均正常交付，第二轮
检查规范工具错误和思考签名回传，数据库 raw 与模拟上游原始响应字节完全相同。仅注入的一次 503
触发瞬时重试，搜索达到上限不触发重试；计量与预期一致。原有媒体、临时交互、CPU/heap/备份验收
同时通过。未向真实 Telegram 发送验收消息，未发起真实付费模型调用或改写历史失败请求。
