# AGENTS.md — xiaowen-core 全局研发规范

本仓库是 xiaowenz.com 的自研动态服务（评论 / 订阅与验证 / Newsletter 群发 / 媒体上传 / 下载与访问统计）。
任何 Agent 或人类贡献者都必须遵守本规范。**违反本规范的代码不得合入。**

## 1. TDD 铁律（最高优先级）

1. **严格 Red → Green → Refactor**：先写失败的测试，再写最小实现让测试通过，最后重构。禁止先实现后补测试。
2. **从功能角度出发**：每个测试必须描述一个用户可感知的行为或契约（"订阅后收到验证邮件""重复下载当天只计一次"），而不是为实现细节凑覆盖率。
3. 测试金字塔：数据层（store）→ HTTP 处理层（httptest）→ 少量端到端（真实 SQLite 临时库 + httptest.Server）。
4. 外部依赖一律接口化 + Fake：邮件发送、Turnstile 验证、S3/R2、时钟。测试绝不触网、绝不依赖真实凭据。
5. 每个 PR/提交必须使 `go test ./... -race` 全绿；新增行为必须有新增测试；修 bug 必须先写复现测试。

## 2. 持续集成质量门

CI（.github/workflows/ci.yml）必须包含且全绿才可合入：

- `go test ./... -race -count=1`
- `go vet ./...`
- `gofmt -l .`（输出必须为空）
- `go build ./...`
- `docker build`（Dockerfile 必须可构建）

## 3. 数据库迁移纪律

1. Schema 变更**只能**通过新增迁移文件完成：`internal/db/migrations/NNNN_description.sql`，序号单调递增，禁止修改已合入的迁移。
2. 每个迁移用 goose 格式标注 `-- +goose Up` / `-- +goose Down`，Down 必须真实可回滚。
3. 迁移必须是**加法式**的（新表、新列、新索引），破坏性变更（删列、改类型）拆成两次发布：先兼容上线，再清理。
4. 启动时自动执行 `goose up`；迁移失败必须让进程启动失败（fail fast），禁止忽略。
5. SQLite 文件位于 `/data/core.db`（容器内），WAL 模式；开发环境可用 `CORE_DB_PATH` 覆盖。

## 4. 安全基线

1. 所有机密走环境变量（见 README 配置表），**仓库中禁止出现任何真实密钥**；示例用 `.env.example`。
2. 管理员接口：会话 cookie 必须 HttpOnly + Secure + SameSite=Lax；口令比较用 `subtle.ConstantTimeCompare`；admin POST 请求校验 Origin。
3. 公开写接口（订阅/评论）必须有三件套：Turnstile 校验（可配置跳过仅限 dev）、蜜罐字段、按 IP 限流。页面打点（/api/pv）与下载计数（/a/*.mp3）只做限流 + DNT 尊重——逐页请求无法做挑战，属刻意豁免。
4. 所有 SQL 用参数化查询；所有用户输入输出到 HTML 必须经模板转义；邮件 header 防注入（CRLF 过滤）。
5. IP 不落盘明文：统一 `sha256(ip + 当日日期)` 后存储。

## 5. 代码风格

1. Go 标准库优先：路由用 `http.ServeMux`（Go 1.22+ 模式路由），不引 Web 框架。依赖保持最小集，新增依赖需在 PR 说明理由。
2. 包结构：`cmd/core`（装配）+ `internal/*`（config / db / store / httpapi / admin / newsletter / mailer / s3store / turnstile / ratelimit / backup）。internal 包禁止被 cmd 以外的包导入业务逻辑。
3. 错误处理：向上返回 `fmt.Errorf("...: %w", err)`；HTTP 层统一错误响应格式 `{"error": "..."}`。
4. 日志：`log/slog`，JSON 格式，关键操作（订阅激活、群发完成、备份完成）必须有结构化日志。
5. 注释只写「为什么」，不写「是什么」。公开导出标识符必须有 doc 注释。

## 6. 完成定义（Definition of Done）

一个功能完成 = 实现 + 测试 + 文档三者齐全：

- [ ] 行为测试存在且通过（先红后绿）
- [ ] `go test ./... -race` / `go vet` / `gofmt` 全绿
- [ ] README/SPEC 中相关章节同步更新
- [ ] 配置项变化同步到 `.env.example` 与 README 配置表
- [ ] Schema 变化有新迁移文件且 Down 可执行
