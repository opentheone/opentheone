# OpenTheOne

[![CI](https://github.com/wzyjerry/opentheone/actions/workflows/ci.yml/badge.svg)](https://github.com/wzyjerry/opentheone/actions/workflows/ci.yml)
[![CodeQL](https://github.com/wzyjerry/opentheone/actions/workflows/codeql.yml/badge.svg)](https://github.com/wzyjerry/opentheone/actions/workflows/codeql.yml)
[![Release](https://img.shields.io/github/v/release/wzyjerry/opentheone?include_prereleases&sort=semver)](https://github.com/wzyjerry/opentheone/releases)
[![License](https://img.shields.io/badge/license-MIT-blue.svg)](./LICENSE)
[![Go Version](https://img.shields.io/badge/go-%E2%89%A51.25-00ADD8?logo=go)](https://go.dev/)
[![Vue 3](https://img.shields.io/badge/vue-3.x-42b883?logo=vue.js)](https://vuejs.org/)
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-brightgreen.svg)](./CONTRIBUTING.md)
[![Code of Conduct](https://img.shields.io/badge/Contributor%20Covenant-2.1-4baaaa.svg)](./CODE_OF_CONDUCT.md)

> 你的唯一 AI，与最重要的 TA。  
> 把 AI 角色合法合规接入微信（基于腾讯官方 ClawBot / iLink 协议）的开源后台，自带 Web 控制台。

OpenTheOne 是商业产品 [TheOne 陪伴](https://one.dxcat.cn/) 的开源替代实现，提供：

- **唯一角色**：你可以创建多个 AI 角色，但同一时刻只能激活一个，TA 就是你的唯一。
- **直连微信**：基于腾讯官方 ClawBot 协议（iLink）扫码绑定，AI 像普通联系人一样收发消息。
- **多模型支持**：兼容所有 OpenAI Chat / Embedding 协议的模型（DeepSeek、OpenAI、Qwen、Kimi…），内置 5 个常用 provider 预置一键填表。
- **长期记忆**：自动抽取偏好/事实/事件并以向量检索的方式注入回复（无 embedding API 时降级为时间序）。
- **主动消息**：用 cron 表达式让 TA 定时主动找你聊聊。
- **完整历史**：所有对话本地存储 SQLite，可导出 Markdown / JSON。
- **单二进制**：前端被 `go:embed` 进二进制，部署只需要一份 `oto-server` 可执行文件，或者一个 Docker 镜像。

详细设计请看 [`doc.md`](./doc.md)。

---

## 快速开始

### 方式一：Docker（推荐）

```bash
git clone https://github.com/wzyjerry/opentheone.git
cd opentheone
docker compose up -d
```

打开浏览器访问 <http://localhost:8080>，**第一个注册的账号自动成为 admin**。  
所有持久化数据落到 `oto-data` 这个 docker volume，包含 SQLite 文件、附件、自动生成的 JWT secret。

### 方式二：本地构建二进制

依赖：Go ≥ 1.25，pnpm ≥ 9。

```bash
git clone https://github.com/wzyjerry/opentheone.git
cd opentheone
make build           # 等价于 pnpm build + go build，会注入 version/commit/build_time
./backend/oto-server --version
./backend/oto-server --config backend/config.example.yaml
```

启动后浏览器访问 <http://localhost:8080>，按下面 3 步上线 AI：

1. **模型** → 点 "DeepSeek 预置" → 填 API Key → 测试通过。
2. **角色** → 新建你的「唯一」→ 写人设和说话风格。
3. **角色详情** → "设为唯一" → 扫码绑定可控的微信号 → 完工。

完成后回到 Dashboard，顶部健康检查应显示「一切就绪，TA 正在线」。

### 方式三：开发模式

```bash
# 终端 A：后端
cd backend && go run ./cmd/server

# 终端 B：前端（Vite 已经把 /api 代理到 :8080）
cd frontend && pnpm dev
```

---

## 接口规范

- 全部 `POST`，JSON body，统一前缀 `/api/`。
- 响应固定包装 `{ "code": 0, "msg": "ok", "data": ... }`，字段使用 `lower_snake_case`。
- 登录后颁发 JWT，置于 `Authorization: Bearer <token>` 中。
- `/api/health`（GET 也支持）返回 `version` / `uptime` / `db_ok`，可直接喂给监控系统。

完整 API 列表见 [`doc.md`](./doc.md#5-http-api)。

---

## 项目结构

```
opentheone/
├── doc.md                  # 详细设计文档
├── Dockerfile              # 多阶段构建（pnpm build → go build → alpine）
├── docker-compose.yml      # 一行 docker compose up 跑起来
├── Makefile                # build / test / vet / docker
├── backend/                # Go 服务端
│   ├── cmd/server/         # main 入口（含 --version）
│   ├── internal/
│   │   ├── auth/           # JWT + bcrypt + secret 自动生成
│   │   ├── config/         # 配置加载
│   │   ├── crypto/         # AES-256-GCM
│   │   ├── engine/         # 对话引擎（核心）
│   │   ├── handler/        # HTTP 路由处理
│   │   ├── ilink/          # 微信 ClawBot iLink 协议
│   │   ├── llm/            # OpenAI 兼容客户端 + 预置 provider
│   │   ├── memory/         # 长期记忆
│   │   ├── middleware/     # JWT / Admin / RateLimit
│   │   ├── model/          # GORM 数据模型
│   │   ├── proactive/      # 主动消息调度
│   │   ├── runner/         # 长轮询 goroutine
│   │   ├── server/         # 路由组装
│   │   ├── settings/       # 运行时可调全局设置
│   │   ├── store/          # DB + AutoMigrate
│   │   └── web/            # go:embed dist
│   └── config.example.yaml
└── frontend/               # Vue 3 + Vite + Tailwind 控制台
    └── src/
```

---

## 安全须知

OpenTheOne 默认假设是**自部署、私有用途**。如果要暴露在公网，请务必：

1. **设置强 JWT secret**：把 `auth.jwt_secret` 留空（服务首次启动会自动在 `data/secret.key` 生成并写入），或自己填一个 ≥32 字节的随机字符串。**不要使用文档/示例里的占位符**。
2. **关闭开放注册**：登录后到「管理员」面板关闭 `allow_register`，或者在 `config.yaml` 里设置 `allow_register: false`。
3. **前置反向代理**：用 nginx / Caddy / Cloudflare Tunnel 提供 TLS 和 IP 白名单。
4. **备份 `data/`**：里面有 SQLite 数据库、加密后的 LLM API Key、JWT 签名根、附件文件。

完整威胁模型见 [`SECURITY.md`](./SECURITY.md)。

---

## 合规与免责声明

OpenTheOne 只调用腾讯官方公开的 [微信 ClawBot iLink Bot API](https://developers.weixin.qq.com/doc/aispeech/knowledge/openapi/Clawbotrelated.html)：

- 完全使用正规扫码登录，不做协议逆向、不绕过反作弊。
- 适合个人自部署、与好朋友/特定人群的陪伴场景；**不适合任何规模化垃圾消息或灰产用途**。
- 模型生成的内容由你的 LLM Provider 提供，请自行确保符合所在地法律法规。

---

## 贡献

欢迎贡献！请先阅读：

- [CONTRIBUTING.md](./CONTRIBUTING.md) — 开发环境、提交规范、PR 流程。
- [CODE_OF_CONDUCT.md](./CODE_OF_CONDUCT.md) — 行为准则。
- [CHANGELOG.md](./CHANGELOG.md) — 版本变更记录。

报告安全漏洞请走 [SECURITY.md](./SECURITY.md) 里的私有渠道，不要在公开 issue 里发。

## 路线图

短期内的方向（不承诺时间表）：

- 更细的权限模型（团队 / 共享 persona 模式）。
- 更多 LLM provider 预置（豆包、智谱、Gemini OpenAI-compat 端点）。
- 多媒体生成（让 TA 主动发图、发语音）。
- 历史检索（全文检索对话历史）。

想推进上面任何一条？欢迎开 issue 讨论实现方案。

## License

[MIT](./LICENSE) © 2026 OpenTheOne contributors
