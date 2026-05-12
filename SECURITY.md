# Security Policy

## Supported Versions

OpenTheOne 目前还处于早期阶段（pre-1.0）。我们只对 **`main` 分支的最新一次提交** 和最近一次打的 release tag 提供安全修复。

## Reporting a Vulnerability

如果你发现了潜在的安全问题，**请不要直接在 GitHub Issues 里公开**。请通过以下任一渠道私下联系：

1. 在 GitHub 仓库 → Security → **"Report a vulnerability"** 提交 Private Vulnerability Report。
2. 或邮件发到 `security@opentheone.dev`（如有此邮箱），主题前缀 `[SECURITY]`。

请尽量提供：

- 复现步骤
- 你认为的影响范围（例如：能否拿到他人的 LLM API Key、能否伪造 JWT、能否越权访问他人会话）
- 你使用的 OpenTheOne 版本（`oto-server --version`）和部署形态（docker / 二进制 / 源码运行）

我们承诺：

- **24 小时内** 给出一个"收到，正在看"的回复。
- **7 天内** 给出初步的影响判断和修复时间表。
- 修复发布之后，会在 release notes 里 credit 你（除非你要求匿名）。

## 已知的安全约束

OpenTheOne 是一个自部署的私有服务，默认假设：

- **部署者就是 root**。一个能登录 `admin` 的人可以读所有用户的 LLM 配置（加密存储但密钥就在同一台机器上）、删除任意用户。如果你需要"多租户但管理员不能看 API Key"的强隔离场景，OpenTheOne **不适合你**。
- **不暴露公网**。如果一定要暴露，请：
  - 设置一个长度 ≥32 的随机 `auth.jwt_secret`（或留空让服务自动生成）。
  - 在前面套 nginx / Caddy / Cloudflare Tunnel 做 TLS 终结 + IP 白名单。
  - 关闭 `auth.allow_register`（默认开放，配合"第一位注册者即 admin"是为了首次部署便利）。
- **凭据保护**：`data/secret.key` 是 JWT 签名根；`data/oto.db` 包含所有用户对话和加密后的 LLM API Key。**这两个文件被泄露 = 你的私聊和 API Key 都泄露了**。定期备份并保证存储介质加密。

## 当前已知尚未完善的项

我们对这些点完全坦诚：

- 没有完整的 RBAC 模型，只有 `admin` / `user` 两级。
- 没有 audit log（admin 改了谁、删了谁，目前只在 zap 日志里有，没单独落库）。
- 没有针对 LLM provider 的输出过滤（提示词注入、PII 外泄等需要部署方在 LLM 侧做。

## Cryptography Notes

- LLM API Key 用 **AES-256-GCM** 加密，密钥从 `auth.jwt_secret` 通过 SHA-256 派生。
- JWT 签名算法是 **HS256**，secret 长度 ≥16；推荐 ≥32 byte 随机。
- 密码哈希用 `bcrypt`，cost = library default (10)。

如果你对加密实现有质疑（例如希望支持 Argon2id / KMS 托管 secret），欢迎提 issue 讨论。
