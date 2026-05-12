# Contributing to OpenTheOne

感谢你愿意参与 OpenTheOne！请先花两分钟读一下这份指南。

## 行为准则

请遵守 [Contributor Covenant](https://www.contributor-covenant.org/version/2/1/code_of_conduct/) 的精神：友善、尊重、有耐心。

## 我能贡献什么？

- **Bug 报告**：在 issue 里附上复现步骤、期望行为、`oto-server --version` 和日志片段。
- **新功能 / 改进**：先开一个 issue 讨论清楚再发 PR，避免做了白工。
- **文档**：`README.md` / `doc.md` 任何不准确、过时、表达不清的地方欢迎修。
- **测试**：核心路径（`engine`、`runner`、`crypto`、`memory`）的单元测试覆盖率永远嫌少。

## 开发环境

```bash
# 后端
cd backend
go mod download

# 前端
cd ../frontend
pnpm install
```

需要 Go 1.25+ 与 pnpm 9+。

## 常用命令

仓库根目录 `Makefile` 已经把常用命令包装好了：

```bash
make build           # 完整 e2e 构建（前端 + 后端，含版本号）
make test            # go test -race ./...
make vet             # go vet ./...
make lint            # golangci-lint run ./... （需先安装 golangci-lint）
make fmt             # gofmt -s -w
make docker          # 构建 docker 镜像
```

如果你还没装 `golangci-lint`：

```bash
go install github.com/golangci/golangci-lint/cmd/golangci-lint@v1.61
# 或者 brew install golangci-lint
```

如果你不想用 Make：

```bash
cd frontend && pnpm build
cd ../backend && go test ./... && go vet ./... && go build ./...
```

## 提 PR 前请确认

1. **所有测试通过**：`make test` 和 `make vet` 都没有报错。
2. **代码格式**：跑过 `make fmt`；CI 会用 `gofmt -d` 检查 diff 必须为空。
3. **提交信息**：尽量小步快走，一次只做一件事；commit message 用动词开头，如 `fix(runner): recover from panic in inbound message handler`。
4. **改了协议/接口/数据库**：同步更新 `doc.md`。
5. **改了用户可见行为**：如果有必要，同步更新 `README.md` 的截图/示例。

## 项目分层（哪些文件该改哪些不该改）

```
backend/internal/
  ilink/      微信 iLink 协议层。改这里需要对照官方文档；不要"猜测"协议字段。
  llm/        OpenAI 兼容客户端。保持薄；专用逻辑放 engine。
  engine/     对话核心。新规则（system prompt 等）从这里走。
  runner/     长轮询。修改时务必保持 panic-safe，不要把 ctx 泄漏。
  memory/     长期记忆。请避免在这里做"业务侧"决策。
  handler/    HTTP 层。只做参数校验、调用业务包、返回 JSON。
  middleware/ 中间件。
  model/      GORM 模型。**只增字段，不要破坏老数据**。
```

## 数据库变更

OpenTheOne 用 GORM AutoMigrate。规则：

- ✅ **新增字段** 任意时候都可以。
- ✅ **新增表** 加到 `AllModels()`。
- ❌ **重命名字段** 没有迁移脚本前不允许。
- ❌ **删除字段** 同上。

如果你的需求只有破坏性变更可以满足，请在 PR 里详细说明、提供清晰的迁移方案，并打 `breaking-change` 标签。

## 安全相关贡献

请阅读 [`SECURITY.md`](./SECURITY.md)，**不要在公开 issue 里报告安全漏洞**。

## License

提交 PR 即视为同意以 MIT 协议授权你的代码。
