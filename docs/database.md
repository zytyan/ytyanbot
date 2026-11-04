# TS 数据库与 SQL 工作流

运行时使用 TS V6。开发 checkout 为 `/root/mycode/ytyanbot`，直接在 `master` 实现；
`/opt/tgbots/ytyanbot` 仅为线上运行目录，修改代码或通过测试不授权部署。 本次检查 TS 基线
`3e94dcfbea4779da4e1d34dc79ed4869c4909ee6`，归档 Go `ac67c5f7651d3e2a4a2225ae4bd3f3a9f455e979`。

## 最小持久化

- 每条选中的回复链消息保存一次正文、身份和回复关系，会话关系保存顺序、引用片段及 context-only 标记。
  不收集未触发的普通聊天。传统功能只持久化明确注入的事实。
- run 保存请求/回复身份、provider/model、生成与交付状态、每次生成的 tokens 和必要回放 payload。 SDK
  格式保留 provider/protocol、response messages（含 providerOptions）及所需 native assistant 数据。
  旧 native payload 继续支持原协议回放。
- 不保存每轮全量上下文快照或原始 HTTP 响应副本；HTTP 响应仅在生成过程中解析。 不单列重复的 thought
  signature、cache expiry 或会话 token 累计；分别从回放 payload、provider state 和 run 汇总获取。SDK
  outputTokens 已包括 reasoning。
- 请求先落库，生成完成只更新一次 tokens。交付失败保留生成结果，不重新调用模型；交付与 provider state
  晋升仍在同一事务提交。成功交付后 run 的临时 response_text 清空，正文从关联的回复消息读取。
  空文本的计费成功响应同样保留 payload 与 tokens。

## Telegram 媒体缓存

`media_objects` 以 `file_unique_id` 为主键，只保存当前 bot 的 `file_id`、大小和 MIME；
消息媒体关系保存唯一标识、种类、顺序。相同内容的不同 Telegram 唯一标识不合并。 文件路径为
`telegram/<FileUniqueId UTF-8 字节的十六进制编码>`，不保存可推导的路径，不计算媒体 hash。

读取优先使用磁盘缓存；仅 NotFound 使用保存的 FileId 调用 getFile，取得新路径、核对 FileUniqueId、
下载并更新 FileId。缓存写入通过临时文件、完整写入和原子 rename 发布。保留大小、超时和取消检查；
同一唯一标识的并发下载合并，单个请求取消不影响仍在使用下载的其他请求。
权限/大小错误直接报告，Telegram 下载失败也不静默丢弃媒体或调用模型。

备份和离线检查按标识与大小检查文件，不重新计算内容哈希。备份中缺失的可恢复缓存文件单独报告，
不伪称完整媒体备份；运行时首次使用时补下载。`media-gc` 仍须离线运行，默认 dry-run，保留已登记资源。

## SQL 工作流

规范源码为 `sql/migrations/*.sql`、`sql/queries.sql`。修改 SQL 后执行 `deno task db:generate`，
生成可嵌入的 schema 与命名 SQL 常量。生成器在内存库建 schema 并 prepare 每条命名查询；
`deno task db:verify` 检查生成物漂移。业务代码直接调用
`db.get/run/all(SQL.Name, ...)`，不增加查询框架。 既有 inline SQL 是债务，新增运行时查询先进入规范
SQL。已发布 migration 与 Go checksum 不改写。

## 离线转换

```sh
deno run --allow-read --allow-write --allow-sys=hostname src/main.ts db-import \
  --source /path/to/source.db --media /path/to/source-media --output /tmp/new-dataset
```

支持 Go/旧 TS canonical V9–V11、独立 TS V1–V5。源库只读，输出目录必须不存在。 V6
含删列和媒体主键变更，有历史 AI 数据的旧库必须使用 db-import；db-upgrade 拒绝原地精简这类旧库。
空旧库可以原地升级。源库和线上服务均不改动。

转换先写 staging，保留 chat/message/session/run 标识，验证行摘要、回放 BLOB、run token 汇总、
评分聚合、外键和 integrity 后发布 `dataset/`：

- `main.db`：新建的精简运行库，避免删列后的空闲页继续携带旧 payload。
- `ai-media/`：新 Telegram 缓存目录。
- `archive/source.db`：完整只读源库快照，保留旧 HTTP 原始响应、上下文快照、旧会话累计等历史数据。
- `archive/media/`：旧 hash 文件按原目录离线归档，以大小和复制前后逐字节比较验证，不重新哈希。
- `migration-report.json`：源版本/checksum、表映射/摘要、旧累计与 run
  tokens、媒体及不可恢复引用数量。

旧媒体只有 hash，不能据此猜测 Telegram 标识；此版本全部离线归档，运行时保留 unavailable 引用标记，
源目录中已缺失的旧媒体列入报告的 missingSourceMedia，保留源库元数据，不伪称文件已归档。 有效的官方
Gemini Interactions 会话优先通过 previous_interaction_id 发送新增消息，
不读取、下载或重传此前的媒体。只有远端拒绝续接、窗口改变或 provider/model 切换等必须重建历史时，
才加载完整媒体；该流程仍共享 15 分钟总期限和最多五次生成尝试。
需要重建时读取相关回复链会明确提示“历史媒体已离线归档，无法恢复 Telegram
文件标识”，不会偷偷丢掉媒体继续生成。 旧 Go 远端 provider state 与相册 caption
仅保存在离线源库归档；TS provider state 保留。 运行库不再包含重复的 import_archives 表。 CoC
属性仍独立保留于 legacy_coc_attrs。转换失败删除本次新建的 staging，不发布目标、不改源库。

回归使用临时数据库和拦截 API，执行 `deno task check`、`deno task test`、`deno task db:verify`。
实际转换线上数据库与替换服务需要另行明确授权。
