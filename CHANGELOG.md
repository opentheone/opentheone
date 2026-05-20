# Changelog

本项目所有显著变更都会记录在这里。

格式遵循 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，
版本号遵循 [语义化版本](https://semver.org/lang/zh-CN/)。

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Changed
- **长期记忆全面重构**：从「embedding 余弦检索」改为「四层金字塔 + BM25 + LLM 主导」：
  L0 消息 → L1 原子记忆（`persona`/`episodic`/`instruction`）→ L2 主题场景（上限 15）→ L3 用户画像（≤2000 字）。
  全程零 embedding，新增 SQLite FTS5（bigram 中文分词）做关键词召回，LLM 负责抽取 / 冲突仲裁 / 场景归类 / 画像生成。
- **新增内置 agent 工具**：`oto_memory_search` / `oto_scene_read` / `oto_conversation_search`，与 MCP 工具并列暴露给 LLM。
- **system prompt 改为缓存友好布局**：persona 设定 + L3 画像 + L2 场景索引 + 工具说明放头部稳定段，
  rolling summary + L1 召回 + 最近对话放动态段，提升 OpenAI 兼容协议的 prompt-prefix cache 命中率。
- **memory 抽取异步化**：新增 `memory.Pipeline` 调度器（warmup 1→2→4→8→16 / threshold / idle / cold-start），
  消息处理 goroutine 立刻返回，不再阻塞回复延迟。

### Removed (Breaking, 仅影响 0.1.0 之前的 dev 库)
- `llm_configs.embedding_model` 列。
- `memories.embedding` 列。
- `/api/llm/*` 接口中的 `embedding_model` 字段；前端 LLM 配置页移除对应输入框。

### Added
- 新表：`memory_scenes`、`user_profiles`、`memory_pipeline_states`、`memory_extract_checkpoints`。
- 新接口：`/api/scene/{list,get,delete}`、`/api/profile/{get,regenerate}`。
- 前端 PersonaDetailView 新增「用户画像」「主题场景」「原子记忆 + kind 筛选」三个区块。
- **构建要求**：必须以 `-tags sqlite_fts5` 编译（Makefile/Dockerfile 已加，CI/release workflow 待补）。
- `store.Open` 启动时自动 backfill FTS 索引（最多 5000 条 memory + 20000 条 message），保证从旧版本升级上来不会出现空索引。
- proactive 消息现在也注入 L3 用户画像 + L2 场景索引，主动问候不再是泛泛而谈；outbound 文本同步写入 `messages_fts` 供后续 `oto_conversation_search` 检索。

### Fixed
- **多会话 persona 的 pipeline checkpoint 互相干扰**：`last_extracted_message_id` 原本是 per-persona，导致一个高频好友会把另一个安静好友的「未处理消息计数」算错。改为按 (persona, conversation) 拆表（`memory_extract_checkpoints`）。
- **persona 删除 / admin 删除用户 / conversation 删除**：补全对 `memory_scenes` / `user_profiles` / `memory_pipeline_states` / `memory_extract_checkpoints` / `memories_fts` / `messages_fts` 的级联清理，杜绝孤儿数据。
- **场景上限失守**：当 L2 场景已达 15 个上限、LLM 仍输出 `create` 时，事务内 `COUNT(*)` 校验会直接拒绝该决策，避免索引被稀释；该 atom 保留 orphan 由下一轮 pipeline 重试归类。
- **未归类 atom 永远是孤儿**：`runOneCycle` 现每次额外扫至多 16 个 `scene_id=''` 的 atom 重做 scene-fit。
- **dangling watermark 卡死计数**：`countPendingMessages` 在 `last_extracted_message_id` 行被删时通过 `COALESCE(..., '1970-01-01')` 退化为「无 watermark」，不再把 pending 永远算成 0。

---

## [0.1.0] - 2026-05-13

首个公开预览版。功能完整、可自部署，但 API / 数据库结构在 1.0 之前仍可能调整。

The first public preview. Feature-complete and self-hostable; APIs and the
database schema may still change before 1.0.

### Added

- **微信 ClawBot / iLink 接入**：扫码登录、长轮询收信、上下文 token 回复、`sendtyping` 正在输入态、CDN 媒体（图片/语音/文件）AES-128-ECB 下载解密。
- **AI 角色（Persona）**：自定义人设、说话风格、首条问候语；同一用户同一时刻只能激活一个角色（强制"唯一陪伴"）。
- **多 LLM 供应商**：内置 5 个 OpenAI 兼容预置（DeepSeek / OpenAI / Qwen / Kimi / Claude），默认 `deepseek-v4-pro`。
- **长期记忆（Mem0 思路）**：自动从对话抽取 fact / preference / event；启用 embedding 时走两阶段检索（SQL 预筛 → 内存余弦重排，附会话局部性 + 时间衰减加权）。
- **对话压缩（Rolling Summary）**：基于 LangChain `ConversationSummaryBufferMemory` 模式，异步窗口外摘要写入 system prompt，可一键重生成。
- **主动消息**：cron 表达式驱动，AI 按计划主动找你聊聊。
- **完整 Web 控制台**：Vue 3 SPA，控制台一站式管理账号 / 模型 / 角色 / 会话 / 记忆 / 系统设置。
- **管理员面板**：注册开关、用户列表，第一个注册账号自动 admin。
- **单二进制部署**：前端 `go:embed` 进二进制；提供多阶段 Dockerfile 与 `docker compose up -d`。
- **可观测性**：`/api/health` 含 version / commit / uptime / db_ok，`oto-server --version` CLI。

### Security

- LLM API Key 在数据库里以 **AES-256-GCM** 加密存储（密钥由 `auth.jwt_secret` SHA-256 派生）。
- JWT 用 **HS256**，启动时若 secret 缺失或过弱（< 16 字节 / 占位符），自动生成 32 字节随机 secret 并写入 `data/secret.key`（0600 权限）。日志仅打印长度，不打印 secret 本身。
- 登录 / 注册接入滑动窗口限流（10/min, 5/10min），并配套 janitor goroutine 防止 IP bucket 内存泄漏。
- CDN 媒体下载有 50MB 硬上限，防止恶意 CDN 响应导致 OOM。
- 所有 fire-and-forget goroutine（附件下载、记忆抽取、QR 长轮询、摘要生成）均带 `defer recover()`，单点 panic 不会拖垮整个进程。

### Engineering

- **测试**：`crypto` / `auth/secret` / `engine` / `handler` / `middleware` / `memory` 单元测试，CI 跑 `go test -race`。
- **CI**：GitHub Actions 跑 `gofmt` / `go vet` / `go test -race` / `pnpm build` / Docker 镜像 smoke。
- **文档**：README + 设计文档（doc.md）+ CONTRIBUTING + SECURITY + 本 CHANGELOG。

[Unreleased]: https://github.com/opentheone/opentheone/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/opentheone/opentheone/releases/tag/v0.1.0
