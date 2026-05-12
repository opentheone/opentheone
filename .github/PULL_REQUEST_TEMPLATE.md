<!--
感谢你贡献 OpenTheOne！花一分钟填一下这份模板，能帮 reviewer 至少节省十分钟。
Thanks for contributing! Filling this out will save your reviewer hours.
-->

## 这个 PR 解决了什么 / What does this PR do?

<!-- 一句话说清楚动机，如果对应 issue 请用 `Fixes #123` 让 GitHub 自动关联。 -->

Fixes #

## 变更类型 / Type of change

<!-- 勾上对应的，如果是多类型勾多个。 -->

- [ ] Bug fix（无破坏性变更，回归修复）
- [ ] New feature（无破坏性变更，新增功能）
- [ ] Breaking change（**会改变现有接口、配置或数据库结构** — 必须在 CHANGELOG 里突出）
- [ ] Docs only
- [ ] CI / build / chore
- [ ] Refactor（行为不变，仅代码改动）

## 影响面 / Surface area

<!-- 勾上动到的模块，便于自动 reviewer 分配。 -->

- [ ] `backend/internal/ilink/`（**对照官方协议改**，不要"猜测"字段）
- [ ] `backend/internal/engine/` 对话核心
- [ ] `backend/internal/runner/` 长轮询
- [ ] `backend/internal/memory/` 长期记忆
- [ ] `backend/internal/auth/` / `crypto/` / `middleware/`（**安全相关**）
- [ ] `backend/internal/handler/` HTTP 层
- [ ] `backend/internal/model/` GORM 模型（**只允许加字段**）
- [ ] `frontend/`
- [ ] CI / Dockerfile / Makefile / 文档

## 测试 / Testing

<!-- 怎么验证的？至少描述一种验证手段。 -->

- [ ] `make test`（`go test -race ./...`）通过
- [ ] `make vet` + `make fmt` 通过
- [ ] `pnpm build`（前端类型检查 + 生产构建）通过
- [ ] 端到端手动验证（请简述步骤）：

<!-- 例如：
1. 启动后端 `make dev`
2. 打开 http://localhost:5173
3. 登录 → 新建角色 → 扫码 → 触发 X 流程 → 看到 Y 结果
-->

## 兼容性 / Compatibility

- [ ] **未引入** 破坏性数据库变更（无 rename / drop column）
- [ ] **未引入** 破坏性 HTTP 接口变更（旧前端版本仍可用）
- [ ] 若引入了，已在 `CHANGELOG.md` 与 `doc.md` 里写明迁移路径

## 截图 / Screenshots（UI 变更时必填）

<!-- 把改动前后的截图贴在这里。 -->

## 自查 / Self-checklist

- [ ] 我已阅读 [CONTRIBUTING.md](../CONTRIBUTING.md)
- [ ] commit message 用动词开头（`fix(runner): ...`、`feat(memory): ...`）
- [ ] 没有把秘钥、`.env`、`data/` 这种东西误提交
- [ ] 我同意以 MIT 协议授权我的贡献
