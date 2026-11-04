# 性能探针（2026-10-16）

本轮刷新 TS 分支基线 `94e613262b24a67e449b19ca30098cc4e9546e9e`，Go 开发封存分支
`archive/go/20261013/codex/ai-db-v2` 仍为 `d395a28545701a5640d91b1ff7057bee236a3f1e`。
本次只补充观测，不引入 Worker、后台模型调用、数据库版本或聊天采集。

## 入口与记录边界

沿用受 X-Backup-Token 保护的 GET /debug/latency。正常请求记录延迟阶段及数值大小；不记录
消息正文、URL、SQL 文本/参数、配置和密钥。阶段包含 first_start_ms / last_end_ms，可以区分 reaction
发出前的同步处理与 Telegram 接口耗时，再结合 runner_queue_ms 和 transport_lag_ms。 Telegram
消息时间只有秒精度，transport_lag_ms 不能当作精确网络 RTT。

保留最多 100 个完成请求、64 个耗时至少 50 ms 的同步操作、8 次事件循环停顿。后台汇总最多 256
个阶段，内联 SQL 指纹缓存最多 128 项。旧字段保留，新增字段如下：

- measurements：历史消息/媒体数量、媒体和 assistant payload 字节数、 出站请求字符数、原始响应和
  assistant payload 字节数。字符数不冒充 UTF-8 字节数。 provider
  的请求/响应大小是该请求中最近一次模型尝试的值；阶段耗时仍累计各次尝试。
- slow_synchronous_spans：同步操作的阶段、时间、耗时、update/run ID 及成功/失败。 start_ms/end_ms
  为进程内单调时钟；started_at 为便于日志关联的墙上时间近似值。
- runtime_sample：每秒采样的进程 user/system CPU 时间、CPU 占用、RSS、V8 heap 与 external 内存。CPU
  包括运行时线程，可能超过 100%，不等于主线程独占用量。
- event_loop_stalls：延迟至少 500 ms 时，在下一次定时器恢复执行后立即记录并发出 [runtime]
  event_loop_stall 日志，不再只等每 30 次采样的汇总。
  同时关联上次采样以来重叠的慢同步操作及仍在执行的请求，即使阻塞请求已结束也能追溯。

## 具体阶段

- db.get/all/run.<规范查询名>；内联 SQL 为 inline_<SQL SHA-256 前 12 位>。指纹只基于 SQL
  模板，不含参数，便于将原先统一的 inline 耗时区分开；排查时在仓库内对原 SQL 模板计算指纹。
- db.begin/commit/rollback.<指纹>、db.transaction：包含等待写锁和事务提交，事务边界保持不变。 同步
  SQL 超过 100 ms 仍输出安全告警，后台汇总可观察非 AI 请求期间的峰值。
- ai.history.sql / ai.history.media：区分同步历史查询及媒体读取。
- TS V6 已移除 ai.snapshot.json 与全量请求上下文快照持久化。
- ai.persist.pending/generated/delivered：区分请求预存、模型结果落库、交付事务。
- provider.history.build/replay：区分 SDK 消息构建和原生历史回放。
- provider.request.parse/stringify、provider.response.parse、provider.payload.encode： 区分请求/响应
  JSON、payload 编码与 provider.headers / provider.body 网络阶段。

阶段存在父子嵌套，例如 db.transaction 包含 SQL，ai.persist.generated 又包含事务。
不能将所有阶段耗时直接相加，或将 async 阶段耗时等同于 CPU 时间。

## 归因方式与限制

1. reaction.first_start_ms 大：看此前的同步 SQL、历史、JSON 阶段及 runner 排队。
2. reaction 开始及时但 total_ms 大：看 Telegram API/超时/限流记录。
3. event_loop_stall 有重叠同步操作：用 stage/update/run 关联该操作的完整请求。
4. 无已知重叠同步操作：结合 CPU、heap 与当时 provider/其他请求阶段继续排查。 CPU 高是计算/GC
   等活动的线索，CPU 低是 I/O/锁等待/调度等的线索，都不是单独的根因证明。 这不是 GC 事件探针或 CPU
   调用栈采样；未默认开启持续 profiler/heap snapshot。

单元测试在原权限下人工制造约 70 ms 主线程停顿，验证完成请求的同步操作仍能与停顿关联，并采到
CPU/内存。集成测试验证 SQL 指纹不随参数变化、不泄漏 SQL/参数，以及真实编排请求具有各阶段和
大小指标。独立编译 smoke 在生产用户、原受限权限及模拟 API 下验证采样与请求链路。 CPU API 语义参见
https://docs.deno.com/api/node/process/ （cpuUsage）。

验收结果：deno task check 与 142 项测试通过；独立编译 smoke 在生产服务用户下、原权限不变时 确认
CPU/内存采样、历史/JSON/持久化阶段及大小指标均可查询，原有模拟 AI 连续对话、重试、
临时菜单、媒体、本地 API file://、CPU/heap/备份验收继续通过。没有向真实 Telegram 发送测试消息。
