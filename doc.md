# OpenTheOne 设计文档

> 这份文档是 OpenTheOne 的技术总览：从一行命令把服务跑起来，到读这份文档之
> 后能改任何一个模块、提任何一类 issue 不至于走错路径。
>
> 它不重复 [README.md](./README.md) 里的"产品介绍 / 快速上手"，只回答
> 三个问题：**这个系统是什么形状？数据在哪？接口长什么样？**

---

## 0. 目录

- [1. 概览](#1-概览)
- [2. 架构](#2-架构)
- [3. 数据模型](#3-数据模型)
- [4. 对话引擎内部](#4-对话引擎内部)
- [5. HTTP API](#5-http-api)
- [6. 配置](#6-配置)
- [7. 微信 iLink 协议笔记](#7-微信-ilink-协议笔记)
- [8. 部署与运维](#8-部署与运维)
- [9. 不做什么 (Non-goals)](#9-不做什么-non-goals)

---

## 1. 概览

OpenTheOne 是一个**单二进制**的 Go 服务，把一个 LLM 角色"装"进微信。具体来说：

- 用户在 Web 控制台里造一个 AI 角色（人设、说话风格、问候语、主动消息 cron）。
- 通过腾讯官方 ClawBot / iLink Bot 协议**扫码**绑定一个可控的微信号。
- 此后，发送到这个微信号的消息会被长轮询拉过来，喂给 LLM，**像真人聊天一样**回复。
- 系统自动抽取偏好/事实/事件成为**长期记忆**；超出 LLM 上下文窗口的部分自动**滚动摘要**。

商业产品参考：[TheOne 陪伴](https://one.dxcat.cn/)。OpenTheOne 的目标是
让这一套自部署、可审计、可改。

### 1.1 设计原则

| 原则 | 含义 |
|---|---|
| **单进程优先** | 不引入消息队列 / Redis / 多服务，全部在一个 Go 二进制里跑 |
| **SQLite 默认** | 个人 / 朋友圈规模够用，零运维；切换 Postgres 留接口但不是默认 |
| **GORM AutoMigrate** | 只增字段，永不删 / rename，老库平滑升级 |
| **不重复造轮子** | LLM 调用统一走 OpenAI 兼容协议；记忆系统借鉴 Mem0；摘要借鉴 LangChain |
| **失败要响** | fire-and-forget 的 goroutine 必须 `defer recover()`，HTTP 层用统一的 `code/msg/data` 响应壳 |

---

## 2. 架构

```
   ┌─────────────────────────────────────────────────────────────────────────┐
   │                        OpenTheOne 单二进制                              │
   │                                                                         │
   │   HTTP /api/*                       internal/server                     │
   │      │                                  │                               │
   │      ▼                                  ▼                               │
   │   handler  ────────────────►  engine  ◄────────  proactive (cron)       │
   │      │                          │   ▲                                   │
   │      │                          │   │                                   │
   │      ▼                          ▼   │                                   │
   │   middleware (jwt / admin / ratelimit) │                                │
   │      │                              memory  (长期记忆: 抽取 / 检索)     │
   │      ▼                                  │                               │
   │   model + GORM ──► SQLite (data/oto.db) │                               │
   │                                        ▼                                │
   │                                  runner.Manager                         │
   │                                        │                                │
   │                                        │ long-poll get_updates           │
   │                                        ▼                                │
   │                                  internal/ilink ──► ilinkai.weixin.qq.com│
   │                                                                         │
   │   /                                                                     │
   │      └── internal/web (go:embed dist) ── Vue 3 SPA                      │
   └─────────────────────────────────────────────────────────────────────────┘
```

### 2.1 模块职责

| 包 | 职责 | 改这里要注意 |
|---|---|---|
| `cmd/server` | main：加载配置 / 初始化日志 / 装配依赖 / 信号处理 | 启动顺序很重要，先 DB 再 secret 再 runner |
| `internal/config` | viper 加载 yaml + env (`OTO_*`) + defaults | 新增字段同步更新 `config.example.yaml` |
| `internal/store` | GORM 连接 + AutoMigrate；SQLite 加 WAL/busy_timeout/FK | SQLite DSN 拼接处理过 `?` / `&` |
| `internal/auth` | JWT + bcrypt + 自动生成 secret | secret 落盘 0600；弱 secret 不打到日志 |
| `internal/crypto` | AES-256-GCM 加解密 LLM API Key | 密钥从 `auth.jwt_secret` SHA-256 派生 |
| `internal/ilink` | 微信 ClawBot / iLink HTTP + CDN AES-128-ECB | 字段必须对照官方文档，不要"猜" |
| `internal/runner` | 每个 binding 一个长轮询 goroutine + QR 扫码协调 | panic-safe；ctx 由 Manager 持有 |
| `internal/engine` | 对话核心：组装 prompt / 调 LLM / 切分 / 发送 / 入库 | 业务规则集中地；handler 不绕过它 |
| `internal/memory` | Mem0 风格的抽取 / 去重 / 两阶段检索 | 没 embedding 时降级时间序 |
| `internal/proactive` | robfig/cron 调度，按 persona 的 cron 触发主动消息 | 一个 persona 一个 entry |
| `internal/handler` | gin 路由 handler；做参数校验和响应包装 | 业务逻辑放 engine / memory |
| `internal/middleware` | JWT、AdminOnly、登录限流 (sliding window) | 限流器有 Cleanup janitor |
| `internal/settings` | system_settings 表（运行时可调全局开关） | seed 第一次启动，admin 可改 |
| `internal/server` | gin 路由组装 + graceful shutdown | 路由表见 §5 |
| `internal/web` | `go:embed dist/`，Vue 3 SPA 前端 | 前端构建后才有内容 |

### 2.2 数据流：一次入站消息

```
微信群友发"今天好热啊" 
   │
   ▼
ilink GetUpdates 长轮询拉到 1 条 WeixinMessage
   │
   ▼
runner.Runner.handleMsg 把消息交给 engine.HandleInbound
   │
   ▼
engine.HandleInbound:
   1. 查 / 建 conversation 行 (binding_id + ilink_user_id)
   2. 落 inbound message 行（消息体 + context_token）
   3. 异步 goroutine 下载图片 / 语音 / 文件附件
   4. 取 persona 的 LLM 配置 → 解密 APIKey
   5. 检索长期记忆 (memory.Retrieve top-K)
   6. 组装 prompt:
        system  = persona.system_prompt
                + rolling_summary（若有）
                + 记忆 bullets
                + 风格 / cron 提示
        history = 最近 N 条对话 (不含已被摘要的)
        user    = 当前 inbound text
   7. llm.Chat → 拿到回复
   8. engine.splitForChunks → 拆成 ≤ maxChunk 字的段
   9. 逐段 ilink.SendMessage（用 context_token），中间 SendTyping
  10. 每段都落 outbound message 行
  11. 异步 goroutine: memory.IngestSnippet（抽取新事实）
  12. 异步 goroutine: engine.MaybeSummarize（必要时滚动摘要）
```

---

## 3. 数据模型

所有表共享 `BaseModel`（`id` UUID + `created_at` + `updated_at`），由 GORM
AutoMigrate 自动建表。完整定义在
[`backend/internal/model/model.go`](./backend/internal/model/model.go)，下表
摘核心字段。

### 3.1 `users`

| 字段 | 类型 | 备注 |
|---|---|---|
| `id` | UUID | |
| `username` | varchar(64) UNIQUE | 登录名 |
| `password_hash` | text | bcrypt cost = 10 |
| `display_name` | varchar(64) | |
| `role` | varchar(16) | `admin` / `user`；第一个注册的自动 admin |

### 3.2 `llm_configs`

| 字段 | 类型 | 备注 |
|---|---|---|
| `user_id` | UUID | 隔离 |
| `name` | varchar(64) | 友好名，如 "DeepSeek" |
| `base_url` | varchar(255) | OpenAI 兼容 endpoint |
| `api_key_enc` | text | **AES-256-GCM 加密** |
| `chat_model` | varchar(128) | 例 `deepseek-v4-pro` |
| `embedding_model` | varchar(128) | 留空则记忆走时间序降级 |
| `temperature` | float | 默认 0.8 |
| `max_tokens` | int | 默认 1024 |
| `is_default` | bool | 同 user 内只有一个 true |

### 3.3 `personas`

| 字段 | 类型 | 备注 |
|---|---|---|
| `user_id` | UUID | 拥有者 |
| `name` | varchar(64) | |
| `avatar` | varchar(255) | |
| `description` | text | |
| `system_prompt` | text | 喂给 LLM 的核心人设 |
| `greeting` | text | 主动开场白 |
| `speaking_style` | text | 风格描述 |
| `proactive_cron` | varchar(64) | 5 段标准 cron，例 `0 9 * * *` |
| `proactive_prompt` | text | 主动消息的引导词 |
| `is_active` | bool | **同 user 内只能有一个 true** |
| `llm_config_id` | UUID | 关联到一组 LLM 配置 |

### 3.4 `we_chat_bindings`

| 字段 | 类型 | 备注 |
|---|---|---|
| `user_id` | UUID | |
| `persona_id` | UUID | 一对一 |
| `state` | varchar(32) | `pending_scan` / `active` / `expired` / `revoked` / `paused` |
| `bot_token` | text | iLink 鉴权令牌（敏感） |
| `ilink_bot_id` | varchar(128) | 当前 bot 标识 |
| `ilink_user_id` | varchar(128) | 当前已登录的微信用户 ID |
| `last_context_token` | text | 上一条已收消息的 context_token，用于回复 |
| `typing_ticket` | text | 正在输入态票据 |
| `qrcode_token` / `qrcode_image_url` | | 扫码阶段临时存储 |
| `scan_phase` | varchar(16) | `wait` / `scanned` / `confirmed` / `expired` |
| `last_proactive_at` | time | 主动消息节流用 |

### 3.5 `conversations`

| 字段 | 类型 | 备注 |
|---|---|---|
| `binding_id` | UUID | |
| `ilink_user_id` | varchar(128) | 对端微信用户 |
| `session_id` | varchar(255) | iLink session 标识 |
| `nickname` | varchar(128) | |
| `last_message_at` | time | 排序用 |
| `last_context_token` | text | 当前会话最新可回复 token |
| **`summary`** | text | 滚动摘要正文 |
| **`summary_until_message_id`** | UUID | 已纳入摘要的最后一条消息 |
| **`summary_updated_at`** | time | 防并发摘要的水位线 |

### 3.6 `messages`

| 字段 | 类型 | 备注 |
|---|---|---|
| `conversation_id` | UUID | |
| `direction` | varchar(16) | `inbound` / `outbound` |
| `ilink_message_id` | int64 | iLink 平台分配的全局 ID |
| `client_id` | varchar(128) | 出站消息的 idempotency 键 |
| `context_token` | text | 对应 iLink 的 contextToken |
| `type` | varchar(16) | `text` / `image` / `voice` / `file` / `video` |
| `text` | text | 文本内容 |
| `media_url` | varchar(255) | 原始 CDN URL |
| `extra` | text | 序列化的额外字段（JSON） |
| `status` | varchar(16) | `received` / `sent` / `failed` |

### 3.7 `memories`

| 字段 | 类型 | 备注 |
|---|---|---|
| `persona_id` | UUID | 隔离 |
| `conversation_id` | UUID | 来源会话（用于局部性加权） |
| `kind` | varchar(32) | `fact` / `preference` / `event` / `summary` |
| `content` | text | 第三人称的短句 |
| `embedding` | blob | float32[] little-endian，长度 = embedding model 维度 |
| `importance` | int | 1-10，由 LLM 估计 |
| `source_message_id` | UUID | 抽取出该记忆的入站消息 |

### 3.8 `attachments`

| 字段 | 类型 | 备注 |
|---|---|---|
| `message_id` | UUID | |
| `kind` | varchar(16) | `image` / `voice` / `file` / `video` |
| `local_path` | varchar(255) | 解密后落盘的路径 |
| `size` | int64 | 字节 |
| `mime` | varchar(64) | |

### 3.9 `system_settings`

| 字段 | 类型 | 备注 |
|---|---|---|
| `key` | varchar(64) UNIQUE | 当前唯一一项 `allow_register` |
| `value` | text | 文本，业务侧自己解释成 bool / int |

---

## 4. 对话引擎内部

`internal/engine` 是整个项目最值得读的包。下面挑几处不显然的点。

### 4.1 历史窗口 + 滚动摘要

Engine 默认参数（可通过 `engine.Options` 调）：

| 参数 | 默认 | 含义 |
|---|---|---|
| `MaxChunk` | 1800 字符 | 单次 `sendmessage` 上限，超出自动切段 |
| `HistoryN` | 16 | 喂给 LLM 的"最近原文"消息条数 |
| `RetrieveK` | 5 | 注入 prompt 的长期记忆条数 |
| `SummaryEvery` | 8 | 超过 `HistoryN + SummaryEvery` 条未摘要时，触发摘要 |
| `SummaryTarget` | 600 字 | 摘要目标长度 |

策略来自 LangChain 的 `ConversationSummaryBufferMemory`：

- 凡是被 `summary_until_message_id` 覆盖的消息，**不再原文喂给 LLM**，只
  以 system message 形式注入摘要文本。
- 摘要本身按"上一次摘要 + 新增 N 条"的滚动方式做：每次扩出去的代价是
  O(批大小)，而非 O(全部历史)。
- 触发是**异步 fire-and-forget**，回复用户不阻塞。

并发控制：`engine.summarize.go` 里有一个 `sync.Map` 维护"每个 conversation
一把 mutex"。`MaybeSummarize` 用 `TryLock` 抢，抢不到就让位；
`RebuildSummary`（用户手动触发）用阻塞 `Lock` 抢。两者都会**重新从 DB 拉
最新水位线**，防止覆盖竞争窗口里另一个协程已经写好的摘要。

### 4.2 长期记忆：两阶段检索

`memory.Retrieve(personaID, conversationID, query, k)`：

1. **SQL 预筛**：按 persona 取 top-300，排序 `importance DESC, created_at DESC`。
2. **内存重排**：把 query 通过 embedding model 拿到一个 float32 向量，逐条
   计算余弦相似度，并叠加：
   - 会话局部性加权：来自同一 conversation 的 memory，分数 ×1.2。
   - 时间衰减：`exp(-Δdays / 30)`，越新越加分。
3. 返回 top-K。

若 LLM 配置里 `embedding_model` 为空 → 走纯时间序降级，返回最近 K 条。
**这是有意的"无 embedding 也能用"路径**，不应被移除。

### 4.3 消息切分

`splitForChunks(text, max)` 优先按段落（`\n\n`）→ 单行（`\n`）→ 句号/问
号/感叹号 → 强制按字符切。每段单独发送，段间穿插 `sendtyping` 让对端
看到"对方正在输入"。

### 4.4 附件下载

入站消息若包含媒体类型（`image` / `file` / `voice`），engine 异步从
iLink CDN 拉加密 blob → AES-128-ECB 解密 → 落盘到
`storage.attachments_dir`。**有 50MB 硬上限**，防止恶意/异常响应 OOM。

---

## 5. HTTP API

### 5.1 通用约定

- **方法**：除 `/api/health` 同时支持 GET 之外，所有接口都是 **POST**。
- **请求体**：`Content-Type: application/json`，参数放 body。
- **响应**：固定包装。

  ```json
  {
    "code": 0,            // 0 = ok，非 0 = 业务错误（参考各接口）
    "msg":  "ok",
    "data": { ... }       // 或 null
  }
  ```

  HTTP 状态码与 `code` **不一定一致**：例如登录失败用 HTTP 401 +
  `code: 401`；参数错误用 HTTP 400 + `code: 400`。
- **鉴权**：登录后拿 token，放 `Authorization: Bearer <token>`。
- **字段命名**：一律 `lower_snake_case`。

### 5.2 端点一览

#### 公共
| Method | Path | 说明 |
|---|---|---|
| POST | `/api/auth/register` | 注册（受 5/10min 限流；`allow_register=false` 时返回 403） |
| POST | `/api/auth/login` | 登录（受 10/min 限流） |
| GET / POST | `/api/health` | 健康检查（`db_ok` / `version` / `uptime`） |

#### 已登录
| Method | Path | 说明 |
|---|---|---|
| POST | `/api/auth/me` | 当前用户信息 |
| POST | `/api/auth/update_profile` | 更新昵称 |
| POST | `/api/auth/update_password` | 改密码 |
| POST | `/api/llm/create` | 新增 LLM 配置 |
| POST | `/api/llm/list` | 当前用户的 LLM 配置列表 |
| POST | `/api/llm/update` | 更新（含 `is_default` 切换） |
| POST | `/api/llm/delete` | 删除 |
| POST | `/api/llm/test` | 跑一次最短的 chat 探活 |
| POST | `/api/llm/providers` | 取内置预置列表（DeepSeek / OpenAI / Qwen / Kimi / Claude） |
| POST | `/api/persona/create` | 新建 persona（校验 `proactive_cron`） |
| POST | `/api/persona/list` | 列表 |
| POST | `/api/persona/get` | 详情 |
| POST | `/api/persona/update` | 改人设、风格、cron |
| POST | `/api/persona/delete` | 删除（级联清 binding） |
| POST | `/api/persona/activate` | 设为"唯一" (强制只允许一个 active) |
| POST | `/api/persona/deactivate` | 取消激活 |
| POST | `/api/persona/trigger_proactive` | 立即跑一次主动消息（调试用） |
| POST | `/api/binding/start` | 拿一张二维码（调 iLink `getqrcode`） |
| POST | `/api/binding/status` | 轮询扫码状态 |
| POST | `/api/binding/active` | 当前已激活 binding |
| POST | `/api/binding/for_persona` | 某 persona 对应的 binding |
| POST | `/api/binding/revoke` | 解绑（清 `last_context_token`） |
| POST | `/api/binding/restart` | 重启 binding 的 runner goroutine |
| POST | `/api/conversation/list` | 会话列表（分页） |
| POST | `/api/conversation/messages` | 某会话的消息（分页） |
| POST | `/api/conversation/send_manual` | 由人类用户口直接说一句（走 engine） |
| POST | `/api/conversation/export` | 导出 Markdown / JSON |
| POST | `/api/conversation/delete` | 删除会话（级联消息） |
| POST | `/api/conversation/rebuild_summary` | 用户触发的滚动摘要全量重算 |
| POST | `/api/attachment/get` | 取附件二进制（base64 包到 JSON 里） |
| POST | `/api/memory/list` | persona 的长期记忆 |
| POST | `/api/memory/delete` | 删一条记忆 |
| POST | `/api/memory/upsert_manual` | 手工写入一条记忆 |

#### 仅 admin
| Method | Path | 说明 |
|---|---|---|
| POST | `/api/admin/users` | 用户列表 |
| POST | `/api/admin/users/set_role` | 提为 admin / 降为 user |
| POST | `/api/admin/users/reset_password` | 强制改密码 |
| POST | `/api/admin/users/delete` | 删除用户（级联） |
| POST | `/api/admin/settings` | 取当前 `allow_register` 等 |
| POST | `/api/admin/settings/update` | 改之 |

### 5.3 响应壳示例

成功：

```json
{ "code": 0, "msg": "ok", "data": { "id": "..." } }
```

业务错误（参数校验）：

```json
{ "code": 400, "msg": "proactive_cron 无效（请使用 5 段标准 cron）", "data": null }
```

鉴权失败：

```json
{ "code": 401, "msg": "invalid token", "data": null }
```

限流：

```json
{ "code": 429, "msg": "too many requests", "data": null }
```

---

## 6. 配置

配置加载顺序：**默认值 ← `config.yaml` ← 环境变量 (`OTO_*`)**，后者覆盖前者。

完整示例见 [`backend/config.example.yaml`](./backend/config.example.yaml)，
速查表如下：

| 段 | 字段 | 默认 | 环境变量 | 说明 |
|---|---|---|---|---|
| `server` | `listen` | `:8080` | `OTO_SERVER_LISTEN` | 监听地址 |
| `server` | `base_url` | `http://localhost:8080` | `OTO_SERVER_BASE_URL` | 外部访问 URL |
| `database` | `driver` | `sqlite` | `OTO_DATABASE_DRIVER` | `sqlite` 或 `postgres` |
| `database` | `dsn` | `data/oto.db` | `OTO_DATABASE_DSN` | 连接串 |
| `auth` | `jwt_secret` | _empty_ | `OTO_AUTH_JWT_SECRET` | **留空时自动生成并落盘到 `data/secret.key`** |
| `auth` | `jwt_expire_hours` | 168 | `OTO_AUTH_JWT_EXPIRE_HOURS` | token 有效期 |
| `auth` | `allow_register` | `true` | `OTO_AUTH_ALLOW_REGISTER` | 全局开关，初始 seed 进 system_settings；运行时可被 admin 改 |
| `ilink` | `base_url` | `https://ilinkai.weixin.qq.com` | `OTO_ILINK_BASE_URL` | 微信 ClawBot API 根 |
| `ilink` | `cdn_base_url` | `https://novac2c.cdn.weixin.qq.com/c2c` | `OTO_ILINK_CDN_BASE_URL` | 媒体 CDN 根 |
| `ilink` | `channel_version` | `1.0.0` | | iLink 协议要求 |
| `ilink` | `long_poll_timeout_ms` | 35000 | | 长轮询超时 |
| `ilink` | `user_agent` | `opentheone/0.1` | | |
| `ilink` | `sk_route_tag` | _empty_ | | 部署方下发的路由标签 |
| `storage` | `data_dir` | `data` | `OTO_STORAGE_DATA_DIR` | SQLite / secret.key / attachments 都落这里 |
| `storage` | `attachments_dir` | `data/attachments` | `OTO_STORAGE_ATTACHMENTS_DIR` | 附件子目录 |
| `logging` | `level` | `info` | `OTO_LOGGING_LEVEL` | `debug` / `info` / `warn` / `error` |
| `logging` | `format` | `console` | `OTO_LOGGING_FORMAT` | `console` / `json`（生产推荐 json） |

### 6.1 JWT secret 解析逻辑

```
1. cfg.Auth.JWTSecret 非空且长度 ≥ 16 且不是已知占位符 → 直接用
2. 否则尝试读 data/secret.key
3. 读不到就生成 32 字节随机 → 写入 data/secret.key（mode 0600）
4. 日志里**只打长度**，不打实际值
```

**删 secret.key = 所有现存登录态失效。** 数据库里的 LLM API Key
（用 JWT secret 派生密钥加密）也读不出来 — 用户需要重新填一次。

---

## 7. 微信 iLink 协议笔记

### 7.1 用了哪些接口

| 接口 | 路径 | 用途 |
|---|---|---|
| `get_bot_qrcode` | `GET /ilink/bot/get_bot_qrcode?bot_type=3` | 申请扫码登录二维码 |
| `get_qrcode_status` | `GET /ilink/bot/get_qrcode_status?qrcode=…` | 长轮询扫码状态（`wait` → `scaned` → `confirmed`） |
| `notifystart` | `POST /ilink/bot/msg/notifystart` | 通告"这个 bot 现在上线了"，下一步进入长轮询前**必须**做（best-effort） |
| `getupdates` | `POST /ilink/bot/getupdates` | 长轮询接收消息 |
| `sendmessage` | `POST /ilink/bot/sendmessage` | 发送文本（带 `context_token`） |
| `getconfig` | `POST /ilink/bot/getconfig` | 获取 typing 的临时票据 |
| `sendtyping` | `POST /ilink/bot/sendtyping` | 触发"对方正在输入" |
| `notifystop` | `POST /ilink/bot/msg/notifystop` | 下线前通告服务器释放长轮询槽位（best-effort） |

### 7.2 通用请求头

所有请求（GET 含 QR 端点、POST 含业务端点）都带：

| Header | 值 | 备注 |
|---|---|---|
| `iLink-App-Id` | `bot` | 来自官方插件 `package.json#ilink_appid`；缺它部分服务端实例**不会下发消息** |
| `iLink-App-ClientVersion` | 整数字符串，例如 `65536` | `((major&0xff)<<16)\|((minor&0xff)<<8)\|(patch&0xff)`，从 `ilink.channel_version` 推导 |
| `SKRouteTag` | 可选 | 部署方自定义路由标签 |
| `User-Agent` | `opentheone/0.1` | 仅用于观测 |

业务 POST 额外带：

| Header | 值 | 备注 |
|---|---|---|
| `Content-Type` | `application/json` |  |
| `AuthorizationType` | `ilink_bot_token` | 固定值 |
| `Authorization` | `Bearer <bot_token>` | 扫码确认后返回 |
| `X-WECHAT-UIN` | `base64(<random uint32 as decimal string>)` | 每次请求重新随机 |

### 7.3 关键细节

- **`context_token`**：每条入站消息携带，**回复必须带上原 token**，否则
  消息不会被路由给对的会话。我们把它存到 `conversations.last_context_token`
  以及 `messages.context_token`。
- **`get_updates_buf` 不透明游标**：服务器返回什么我们就原样回传，绝不解析。
  首次请求传空字符串；扫码重登 / `errcode == -14` 后清空。
- **`message_state` 取舍**：协议允许该字段缺省。我们**只过滤** `GENERATING (1)`
  这种半成品流式中间态，不过滤 `NEW (0)`——后者实际上经常是「未设置」的零值，
  把它一并丢弃曾导致整段会话"收不到消息"。
- **`message_type` 过滤**：服务器有时会把我们自己发出的 BOT 消息（`type=2`）
  回放在 `msgs` 里，长轮询里跳过这种 echo，避免自己回复自己。
- **错误判定**：`ret != 0` **或** `errcode != 0` 都视为失败；只看 `ret` 会漏。
  `ret == -14` 或 `errcode == -14` 是 session timeout，立刻清凭证并提示重新扫码。
- **`longpolling_timeout_ms`**：服务器每次响应都附带"下次建议的长轮询窗口"，
  我们采纳作为下次 `getupdates` 的客户端 deadline。
- **`client_id` 幂等键**：出站消息生成 UUID 作 client_id；iLink 用它去
  重，避免网络抖动重发产生重复消息。
- **`from_user_id` 显式空串**：出站 `sendmessage` 在 `msg.from_user_id` 上
  **显式写空串**（不使用 Go `omitempty`），匹配官方插件——观察到某些部署在
  字段缺失时会拒收。
- **媒体加密**：CDN 返回的图片/语音/文件是 **AES-128-ECB**，密钥从消息的
  `aes_key` 字段 base64 解出（兼容 raw-16-bytes 和 hex-string 两种格式）。
  我们解密后直接落盘到 `attachments_dir`，原文 URL 也保留在 `media_url` 备查。
  下载限制 50MB 上限。
- **revoke**：解绑只是把 `state` 改成 `revoked` + 清 `last_context_token`，
  老 binding 行保留以便事后审计。同时尽力发一次 `notifystop`。

### 7.4 我们**不**做的事

- 不做协议逆向 / 不伪造客户端协议字段。
- 不绕过 iLink 平台的速率 / 反作弊。
- 不支持非腾讯 ClawBot 渠道（不接 itchat / wechaty 等灰协议）。

---

## 8. 部署与运维

### 8.1 推荐部署形态

| 场景 | 推荐 |
|---|---|
| 一个人自用 | `docker compose up -d` |
| 小团队 / 朋友圈 | 同上 + nginx 反代 + Let's Encrypt |
| 想完全控制 | `make build` + systemd unit + 反代 |

### 8.2 健康检查

- `GET /api/health` 不需要鉴权，返回：

  ```json
  {
    "status": "ok",
    "db_ok": true,
    "db_error": "",
    "version": "v0.1.0",
    "commit": "abc1234",
    "build_time": "2026-05-13T00:00:00Z",
    "uptime": "1m30s",
    "started_at": "2026-05-13T00:00:00Z"
  }
  ```

- Docker `HEALTHCHECK` 用 `wget -qO- http://127.0.0.1:8080/api/health`。
- 接进 Prometheus / Datadog 把 `db_ok=false` 设成关键告警；`uptime`
  字段适合监控**重启抖动**。

### 8.3 备份

需要备份的文件全部在 `data/` 下：

```
data/
├── oto.db          ← 主数据库（含加密的 API Key、会话、记忆）
├── oto.db-wal      ← WAL 日志，备份时 sqlite3 .backup 命令更安全
├── oto.db-shm
├── secret.key      ← JWT 签名根，丢了所有现存登录失效，加密的 API Key 也读不出
└── attachments/    ← 媒体附件
```

**正确备份**：

```bash
sqlite3 data/oto.db ".backup '/backup/oto-$(date +%F).db'"
cp data/secret.key /backup/secret.key.$(date +%F)
rsync -a data/attachments/ /backup/attachments/
```

### 8.4 升级

- 二进制升级：`go install` 或拉新版 `oto-server`，直接重启进程。GORM
  AutoMigrate 会自动加新字段。
- Docker：`docker compose pull && docker compose up -d`。
- 老库**永远兼容**（我们承诺只增字段）。

### 8.5 关掉服务

`SIGINT` / `SIGTERM` 触发优雅停机：

1. HTTP 不再接新连接
2. 所有 runner.Manager 里的长轮询 goroutine 收到 ctx.Done()
3. proactive 调度器 stop
4. 5 秒 grace 内还没完的强制结束

---

## 9. 不做什么 (Non-goals)

明确不在 OpenTheOne 范围内的事，避免 issue / PR 走偏：

- **不是企业级 SaaS**：没有计费、没有审计日志落库、没有完整 RBAC。
- **不是消息群发工具**：不做"加爆通讯录 / 群发广告 / 灰产引流"。
- **不做协议逆向**：所有微信交互都通过腾讯官方 ClawBot / iLink Bot API。
- **不做多租户隔离强保证**：部署者就是 root。如果你需要"管理员看不到用户
  API Key"的强隔离，本项目不适合，参考 [SECURITY.md](./SECURITY.md)。
- **不做 LLM 训练**：本项目只是 LLM 的应用层；本地推理 / 微调请另选项目
  （Ollama / vLLM / unsloth 等）配合使用。
- **不绑 Vue / Gin / SQLite**：上述选择是当前最简单实现，不是教义；
  若有更优解，欢迎在 issue 里讨论。但单纯换技术栈不会被接受。

---

文档持续更新中。发现不准确的地方欢迎直接发 PR 修。
