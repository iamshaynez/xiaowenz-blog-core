# SPEC.md — xiaowen-core 功能规格

本文件是 core 服务的**功能契约**。实现必须逐条满足；评审按本文件验收。
技术栈约束：Go 1.26 / 标准库 `net/http`（Go 1.22+ ServeMux 模式路由，不引框架）/ SQLite（`modernc.org/sqlite`，纯 Go 无 CGO）/ `pressly/goose/v3` 迁移（embed.FS 内嵌）/ `yuin/goldmark` 渲染 newsletter markdown / `aws-sdk-go-v2` 访问 R2（S3 兼容）。

## 0. 术语

- **core**：本服务，公网地址 `https://core.xiaowenz.com`（示例）。
- **site**：Hugo 静态站 `https://xiaowenz.com`，是 API 的主要调用方。
- 所有时间存 UTC epoch 秒或 RFC3339；对外 JSON 用 RFC3339。

## 1. 配置（全部环境变量）

| 变量 | 默认 | 说明 |
|---|---|---|
| `CORE_ADDR` | `:8080` | 监听地址 |
| `CORE_DB_PATH` | `/data/core.db` | SQLite 路径（容器挂卷点） |
| `CORE_BASE_URL` | `http://localhost:8080` | 对外根地址，用于邮件内链接 |
| `CORE_SITE_URL` | `http://localhost:1313` | 静态站地址，CORS 允许源 + 跳转回站 |
| `CORE_ADMIN_TOKEN` | 空（空则 admin 拒绝登录） | 管理员口令 |
| `CORE_SMTP_HOST/PORT/USER/PASS` | 空 | 发信；465=TLS，587=STARTTLS |
| `CORE_SMTP_FROM` | 空 | 发件人，如 `三杯过后 <letter@xiaowenz.com>` |
| `CORE_R2_ENDPOINT/KEY_ID/SECRET/BUCKET` | 空 | R2 S3 API；空则媒体上传与备份功能禁用并日志提示 |
| `CORE_R2_PUBLIC_BASE` | 空 | 如 `https://media.xiaowenz.com` |
| `CORE_TURNSTILE_SECRET` | 空 | 空 = dev 模式跳过校验并打 WARN |
| `CORE_ENV` | `dev` | `prod` 时开启更严格行为（cookie Secure 等） |

启动时校验：`prod` 模式下 ADMIN_TOKEN、SMTP、BASE_URL/SITE_URL 为必填，缺失则退出非零。

## 2. 数据模型（迁移 0001 一次建好，后续加法式演进）

- `subscribers`: id, email UNIQUE（小写归一化）, status(`pending|active|unsubscribed`), confirm_token, confirm_expires_at, unsub_token, created_at, confirmed_at, updated_at
- `comments`: id, post_slug, author_name, author_email（不公开）, content, status(`pending|approved|rejected`), ip_hash, ua, created_at, moderated_at
- `newsletters`: id, subject, markdown, html, status(`draft|sending|sent|failed`), sent_count, failed_count, created_at, sent_at
- `downloads`: id, slug, ip_hash, ua, referer, created_at ；统计口径=按 (slug, ip_hash, 日) 去重
- `pageviews`: id, path, referrer, ip_hash, ua, created_at
- `media`: id, key UNIQUE, url, bytes, content_type, created_at
- `send_log`: id, newsletter_id, subscriber_id, status, error, created_at

所有 token：`crypto/rand` 32 字节 hex。索引：email、post_slug+status、slug+created_at、created_at。

## 3. 公开 API

统一 JSON；错误格式 `{"error":"..."}`；写接口三件套（Turnstile/蜜罐 `website` 字段/IP 限流）。CORS 仅允许 `CORE_SITE_URL`。

客户端 IP 解析：仅当对端地址属于 `CORE_TRUSTED_PROXIES` 时才读 `X-Forwarded-For`，且**自右向左**跳过受信代理、取第一个非受信跳（左侧条目可被客户端伪造前插，不得采信）；全部受信或无 XFF 时用对端地址。

### 3.1 订阅与验证（double opt-in）
- `POST /api/subscribe` `{email, turnstile_token, website}` → 202 `{ok:true}`。
  - 幂等：已 active 直接 202 不发信；pending 重发验证邮件并刷新 token 有效期；unsubscribed 重新走验证流程。
  - 发验证邮件：链接 `{BASE_URL}/api/verify?token=...`，24h 有效。限流：5 次/小时/IP。
- `GET /api/verify?token=` → 成功：置 active、发欢迎邮件、302 到 `{SITE_URL}/subscribed/`；失败/过期 → 302 到 `{SITE_URL}/subscribe-expired/`。
- `GET /api/unsub?token=` → 200 确认页（HTML）；`POST /api/unsub` → 执行退订（防 prefetch 误触；body 上限 4 KiB）。群发邮件必须带 `List-Unsubscribe` + `List-Unsubscribe-Post: List-Unsubscribe=One-Click` 头（RFC 8058 一键退订）。

### 3.2 评论
- `POST /api/comments` `{post, name, email, content, turnstile_token, website}` → 202，状态 pending，限流 10 次/小时/IP；post 长度 1–200，内容长度 1–2000，name 1–50；给管理员发通知邮件（含 approve/reject 一键链接，带签名 token）。
- `GET /api/comments?post={slug}` → 200 `{comments:[{author_name, content, created_at}]}` 仅 approved，按时间正序。
- `GET /api/comments/moderate?token=` → admin 邮件里的一键 approve/reject（HMAC 签名 comment_id+action+expiry，链接 7 天有效）。`CORE_ADMIN_TOKEN` 为空时该端点整体禁用（签名密钥会退化为公开常量，必须 fail-closed）。

### 3.3 统计
- `POST /api/pv` `{path, referrer}` → 204；尊重 `DNT: 1`（直接丢弃）；限流 60 次/分/IP。
- `GET /a/{slug}.mp3` → 查 media 表 key=`audio/{slug}.mp3`：命中记 download（含 Range 请求也记，但同日同 IP 去重只算一次）→ 302 到 R2 URL；未命中 404。

### 3.4 健康检查
- `GET /healthz` → `{"ok":true,"db":true}`（db ping 失败则 503）。

## 4. 管理台（/admin，服务端渲染 html/template，无前端框架）

认证：`POST /admin/login`（口令常量时间比较）→ 签发 HMAC 签名会话 cookie（24h，HttpOnly/Secure/SameSite=Lax）；`POST /admin/logout` 过期会话 cookie。中间件守卫所有 /admin/*（login 除外）。admin POST 校验 Origin 头同域。会话为无状态 HMAC 值：登出只清本浏览器 cookie，整体吊销需轮换 `CORE_ADMIN_TOKEN`。

- `GET /admin` 仪表盘：active 订阅数、pending 评论数、近 30 天 PV/UV（按 ip_hash 去重）、各剧集近 30 天下载量。
- 订阅名单：`GET /admin/subscribers?status=&q=`（分页 50）；`POST /admin/subscribers/import`（CSV 上传，列=email[,confirmed]，重复跳过，导入即 active 并记 confirmed_at；**导入不进验证流程**——名单来自旧平台已验证用户）；`GET /admin/subscribers/export`（CSV；公式注入中和：以 `=`/`+`/`-`/`@`/tab/CR 开头的单元格前缀单引号）；`POST /admin/subscribers/{id}/delete`。
- 评论审核：`GET /admin/comments?status=pending`；`POST /admin/comments/{id}/approve|reject`。
- Newsletter：`GET /admin/newsletters`（历史列表）；`GET /admin/newsletters/new`；`POST /admin/newsletters`（subject+markdown → goldmark 渲染 html，存 draft）；`GET /admin/newsletters/{id}`（预览 HTML）；`POST /admin/newsletters/{id}/send` → 后台 goroutine 逐人发送：
  - 每人独立渲染：正文 + 页脚（含 `unsub?token=` 个人链接）；`List-Unsubscribe` + `List-Unsubscribe-Post`（RFC 8058）头；
  - 限速 1 封/秒（可配置 `CORE_SEND_INTERVAL_MS`）；写 send_log；全部完成后置 sent + sent_count/failed_count；
  - 发信中服务重启：启动时将 `sending` 状态回退为 `draft` 并日志告警（幂等优先，宁重发不丢）。
- 媒体：`POST /admin/media`（multipart，≤100MB，白名单 mime：image/*（**不含 svg/svg+xml**，存储型 XSS 载体）、audio/mpeg、audio/mp4）→ PutObject 到 R2 `images/` 或 `audio/` 前缀（文件名冲突加短随机后缀）→ 写 media 表 → 返回 `{url, bytes, key}`；`GET /admin/media` 列表。
- 统计：`GET /admin/stats` JSON + `GET /admin/stats/html` 简单图表页（表格即可）。

## 5. 邮件

- Mailer 接口：`Send(to, subject, html, text string, headers map[string]string) error`；SMTP 实现（465 直连 TLS / 587 STARTTLS）；测试用 Fake。
- 模板（html/template 内嵌）：验证邮件、欢迎邮件、评论通知（含一键链接）、newsletter 外壳（品牌名「三杯过后」，页脚退订链接 + 物理邮寄地址占位配置 `CORE_MAILING_ADDRESS`）。
- 所有邮件同时给 text 纯文本备选；header 值过滤 CRLF。

## 6. 备份

进程内 ticker（24h）：`PRAGMA wal_checkpoint(TRUNCATE)` → `VACUUM INTO` 临时文件 → 上传 R2 `backups/core-YYYYMMDD-HHMMSS.db` → 删除 30 天前的远端备份。R2 未配置则跳过并 WARN。备份完成打结构化日志。

## 7. 运维交付物

- `Dockerfile`：多阶段（golang:1.26-alpine 构建 → alpine 运行），非 root 用户运行，`VOLUME /data`，`EXPOSE 8080`，`HEALTHCHECK` curl /healthz。
- `.dockerignore`、`.env.example`、`Makefile`（test/vet/fmt/build/run/docker）。
- `.github/workflows/ci.yml`：test -race / vet / gofmt / build / docker build。
- `README.md`：架构一句话、配置表、本地开发、Zeabur 部署要点（挂 /data 卷、环境变量、域名绑定）。

## 8. 验收清单（评审逐条核对）

1. `go test ./... -race` 全绿；测试覆盖 §3 每个端点的成功/失败/限流/幂等分支；§4 每个管理动作；邮件内容断言（验证链接、List-Unsubscribe、个人退订链接）。
2. 迁移可从空库 `up` 到最新，再 `down` 回 0，再 `up` 无损。
3. 手工冒烟：`CORE_DB_PATH=/tmp/core.db go run ./cmd/core` 启动；curl 走完 订阅→（从日志拿 token）验证→评论→审核→上传（无 R2 时优雅降级）→群发（fake SMTP）→/a/x.mp3 404→建 media 后 302 全链路。
4. Dockerfile 静态审查通过（本机无 docker 时 CI 兜底）。
