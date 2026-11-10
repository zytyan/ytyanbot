# Gemini 4 Carbon 入口（2026-11-10）

检查基线：`13ffc6cb40978002c3b52ac80b79a4d63a3b6d46`，在 TypeScript master 开发。

模型菜单追加官方 Gemini `gemini-4-carbon`，显示为 Gemini 4 Carbon，使用 `aimodel3` 索引
10。保留已有索引及聊天、会话默认模型。沿用 createAiGenerator/Vercel AI SDK 的 Gemini
生成、回复链、协议恢复和用量统计；不猜测 Carbon 专用思考参数或缓存限制。

当前生产账号只读模型查询返回 404，模型列表也未包含 Carbon，故入口可选不代表账号已开通。
官方模型文档暂无法读取。未将拦截式回归当作真实生成验收，未改用其他模型冒充 Carbon。

回归验证菜单路由、精确模型 ID、两轮原生签名重放及输出 Token 统计。
