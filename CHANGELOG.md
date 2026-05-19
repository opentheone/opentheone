# Changelog

本项目所有显著变更都会记录在这里。

格式遵循 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，
版本号遵循 [语义化版本](https://semver.org/lang/zh-CN/)。

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Changed
- _TBD_

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
