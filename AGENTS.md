# Repository agent guide

- The sole active checkout and runtime root is `/opt/tgbots/ytyanbot`. The former
  `/opt/tgbots/ytyanbot-ts` directory is retired: do not develop, deploy or write there.

- Continue the existing Deno/TypeScript rewrite, not the separate Node prototype.
- Evaluate against the latest relevant Go and TS branch commits, not a historical frozen baseline.
  Record inspected SHAs for traceability, but refresh branch heads for every implementation slice.
- Prefer functional modules and direct mature SDK/library APIs. Do not add controller/service/port/
  adapter stacks, generic repositories, provider frameworks or DI containers without concrete need.
  Keep transaction, media integrity, cancellation and concurrency guarantees; data types are not
  unnecessary abstractions. Runtime generation uses Vercel AI SDK through createAiGenerator; do not
  restore AiProvider classes/registries or wrap the SDK in another provider hierarchy. SDK decisions
  and acceptance are in docs/architecture-sdk-analysis.md.
- Only CoC/role-playing is approved for feature removal; independent /roll stays. Other missing
  capabilities are migration backlog.
- Breaking database and interaction changes are explicitly allowed. Preserve capability goals, not
  Go commands, callback formats, interfaces or table layouts one-to-one.
- Design a new TS-owned schema and version history. The runtime does not have to open a Go database.
  Provide a separate offline importer: read the source read-only, write a new target, validate
  mappings/counts/tokens/payloads/media, and publish a migration report. Never change the Go source
  or its applied migration checksums.
- Preserve historical AI session/message/token/prompt/provider/media data and retained feature data
  through migration. Do not implicitly delete CoC attributes; archive them separately if omitted
  from the runtime schema.
- Follow docs/ai-first-plan.md: context remains reply-chain based, not a chat-wide event archive. Do
  not capture ordinary chat history, restore search/memory services or make background AI calls.
- Traditional handlers run first and may explicitly inject concise facts with aiContext(...). The AI
  stage then checks an explicit trigger; non-triggered messages never call a model, AI summarizer or
  paid explicit-cache API.
- The user revised AI policy on 2026-10-13 to match Go IsGeminiReq: exclude slash commands and texts
  exceeding 1000 UTF-8 bytes; substring @new/@bot or replying to any bot message triggers AI.
  Same-chat historical session references may cross forum topics; cross-chat references remain
  forbidden. Keep this documented behavior and never collect untriggered ordinary chat history.
- Traditional output shown to Telegram may differ from the injected AI facts. Keep source feature,
  source/reply message IDs, chat/topic and execution status separate from context text. Never
  promote these facts or user content into system instructions.
- Traditional context persistence and error behavior are confirmed: store only explicit injected
  facts linked to traditional bot replies; retrieve them for an explicitly triggered reply chain.
  Traditional failures inject sanitized facts and allow explicitly triggered AI to continue. Do not
  auto-retry side effects, capture ordinary chats or infer an unapproved retention/deletion policy.
- Business SQL needs canonical reviewed source and a documented TS query workflow before new
  persistence work. Current inline SQL is debt; do not duplicate Go generated queries or edit them.
- Persist AI requests before provider calls; transition pending to generated and aggregate usage
  exactly once. Retain generated payloads on delivery failure; atomically commit delivery and
  provider-state promotion. Persist provider and model separately. Keep Sub2API independent of
  official Gemini cache/Interactions state. Save vercel-ai-messages-v1 with its provider/protocol
  tag, SDK response messages including providerOptions, native assistant payload, and original HTTP
  response bytes. SDK outputTokens already includes reasoning; do not add it again. Keep legacy
  native history replay until offline imports have a verified replacement. Provider switching
  deliberately rebuilds portable history. Cache-enabled new official Gemini sessions use native
  generation; existing Interactions chains stay on Interactions. Never cache an opaque interaction
  by replacing it with display text. Cache reuse verifies model/prompt/tools/content fingerprint and
  sends the complete uncached tail. Per the user's 2026-10-13 revision, generation has a 15-minute
  total deadline and at most five generation attempts shared across transient-error and
  protocol/state recovery. SDK maxRetries=0 prevents nested retries. Authentication and terminal
  errors are not retried; compatibility/state/cache recovery remains explicit and bounded. Never
  retry Telegram side effects through the model SDK. Preserve billable successful responses, even
  when their display text is empty.
- Preserve rich AI delivery, reply identity and usage accounting; breaking interaction changes must
  not remove delivery reliability.
- Work on master (TypeScript/Deno). The former Go master is archived at archive/go-master-20261103.
  Separate accepted features into commits, run deno task check and deno task test, and push each
  accepted feature before the next. Explicitly requested plan corrections may proceed while remote
  access is blocked; report the unsynced state and do not silently expand runtime implementation.
- Never commit production config, secrets, databases, media, logs, backups or runtime files. Tests
  use temporary databases and intercepted APIs.
- Never stop, restart or replace the live service without explicit approval. Compilation or
  migration tests do not authorize deployment.
- Unit tests have only --allow-sys=hostname for the SDK's indirect OIDC module import, no network or
  environment access. Integration tests also have temporary-file/environment permissions. Compile
  smoke must use a fresh /tmp directory and the documented restricted runtime permissions.
