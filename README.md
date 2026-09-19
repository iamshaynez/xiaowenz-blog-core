# xiaowen-core

自托管的动态服务，为 xiaowenz.com 静态站（Hugo）提供：订阅与 double opt-in 验证、评论与一键审核、Newsletter 群发、媒体上传（R2）、下载与访问统计、SQLite 定时备份。

**架构一句话**：单进程 Go 服务 —— stdlib `net/http`（Go 1.22+ ServeMux 模式路由）+ 嵌入式 SQLite（`modernc.org/sqlite`，纯 Go 无 CGO，WAL）+ goose 迁移内嵌 embed.FS + SMTP 发信 + R2（S3 API）对象存储，所有外部依赖接口化（Mailer / Turnstile / S3 / 时钟），测试零网络。

## 目录结构

```
cmd/core/            装配与优雅退出（signal → Shutdown → sender.Wait → db.Close）
internal/
  config/            环境变量加载 + prod 校验
  db/                打开 SQLite（WAL）+ goose 自动迁移（migrations/0001_init.sql）
  store/             全部数据访问（7 张表 + 统计口径）
  httpapi/           公开 JSON API（subscribe.go / comments.go / stats.go / downloads.go）
  mailer/            Mailer 接口、SMTP 实现（465 TLS / 587 STARTTLS）、LogMailer（dev）、嵌入模板
  s3store/           R2 S3 客户端 + 内存 Fake
  turnstile/         Cloudflare Turnstile 校验（HTTP / Fake / Noop）
  ratelimit/         内存 per-IP 令牌桶（定期清理）
  newsletter/        群发 worker（后台 goroutine，限速，send_log，状态机）
  backup/            24h ticker：wal_checkpoint → VACUUM INTO → S3 → 30 天裁剪
  admin/             服务端渲染管理台（html/template，无 JS 框架）
```

## 配置（全部环境变量）

| 变量 | 默认 | 说明 |
|---|---|---|
| `CORE_ADDR` | `:8080` | 监听地址 |
| `CORE_DB_PATH` | `/data/core.db` | SQLite 路径（容器挂卷点） |
| `CORE_BASE_URL` | `http://localhost:8080` | 对外根地址，用于邮件内链接 |
| `CORE_SITE_URL` | `http://localhost:1313` | 静态站地址，CORS 唯一允许源 + 验证后跳转回站 |
| `CORE_ADMIN_TOKEN` | 空 | 管理员口令；空 = admin 拒绝登录 |
| `CORE_SMTP_HOST` | 空 | SMTP 主机；空 = dev 日志邮件（邮件内容打进日志，不会真发） |
| `CORE_SMTP_PORT` | `587` | 465 = 直连 TLS；587/其他 = STARTTLS（对端支持时升级） |
| `CORE_SMTP_USER` / `CORE_SMTP_PASS` | 空 | SMTP 凭据（服务端仅在对方通告 AUTH 时登录） |
| `CORE_SMTP_FROM` | 空 | 发件人，如 `三杯过后 <letter@xiaowenz.com>`；**其地址同时作为评论审核通知的收件人**（SPEC 未定义单独的 admin 邮箱，保守选择，见「偏离说明」） |
| `CORE_R2_ENDPOINT` / `CORE_R2_KEY_ID` / `CORE_R2_SECRET` / `CORE_R2_BUCKET` | 空 | Cloudflare R2 S3 API；任一为空则媒体上传与备份禁用并 WARN |
| `CORE_R2_PUBLIC_BASE` | 空 | 对象公开地址前缀，如 `https://media.xiaowenz.com` |
| `CORE_TURNSTILE_SECRET` | 空 | 空 = dev 模式跳过校验并 WARN |
| `CORE_TRUSTED_PROXIES` | 空 | 可信反代列表（逗号分隔 CIDR/IP，如 `10.0.0.0/8,1.2.3.4`）；仅这些对端的 `X-Forwarded-For` 会被采信。**空 = 永不采信 XFF**，客户端 IP 一律取对端地址（防伪造绕过限流） |
| `CORE_ENV` | `dev` | `prod` 时 cookie Secure + 启动强校验 |
| `CORE_SEND_INTERVAL_MS` | `1000` | 群发限速（毫秒/封，默认 1 封/秒） |
| `CORE_MAILING_ADDRESS` | 空 | Newsletter 页脚物理邮寄地址占位 |

**prod 启动校验**（`CORE_ENV=prod`）：`CORE_ADMIN_TOKEN`、`CORE_SMTP_HOST`+`CORE_SMTP_FROM`、`CORE_BASE_URL`、`CORE_SITE_URL` 缺失即退出非零。

## 本地开发

```sh
cp .env.example .env   # 按需修改
make test              # go test ./... -race -count=1
make vet && make fmt   # go vet + gofmt 检查
make run               # CORE_DB_PATH=/tmp/core.db go run ./cmd/core
```

dev 模式下：Turnstile 跳过（WARN）；邮件打进日志（验证/退订链接可直接从日志复制）；R2 未配置时上传返回 503 JSON、备份跳过（WARN）。

## 手工冒烟（验收 §8.3）

```sh
# 终端 A：启动（dev，日志即邮件）
CORE_DB_PATH=/tmp/core.db CORE_ADMIN_TOKEN=dev-token \
  CORE_BASE_URL=http://localhost:8080 CORE_SITE_URL=http://localhost:1313 \
  go run ./cmd/core

# 终端 B：
# 1. 订阅 → 202
curl -i -X POST localhost:8080/api/subscribe \
  -H 'Content-Type: application/json' -d '{"email":"me@example.com"}'

# 2. 从终端 A 的日志拿验证链接（dev 邮件走日志），访问：
curl -i 'http://localhost:8080/api/verify?token=<从日志复制>'
# → 302 到 http://localhost:1313/subscribed/，并收到欢迎邮件（日志）

# 3. 评论 → 202，管理员通知邮件（含一键 approve/reject 链接）打进日志
curl -i -X POST localhost:8080/api/comments \
  -H 'Content-Type: application/json' \
  -d '{"post":"ep-01","name":"听众甲","email":"a@b.c","content":"赞"}'

# 4. 从日志复制 /api/comments/moderate?token=... 链接完成审核；
#    随后公开列表可见：
curl 'localhost:8080/api/comments?post=ep-01'

# 5. 下载：未建 media → 404
curl -i localhost:8080/a/ep01.mp3
#    管理台上传（无 R2 时优雅 503）：浏览器开 http://localhost:8080/admin
#    （口令 dev-token）→ 媒体 → 上传 ep01.mp3。配置 R2 后再访问 → 302 到 R2 URL。

# 6. Newsletter：管理台 → Newsletter → 新建（Markdown）→ 预览 → 开始群发
#    （dev 模式群发内容进日志；每人带个人退订链接 + List-Unsubscribe 头）

# 7. 退订：从群发日志拿 /api/unsub?token=... → GET 出确认页 → POST 执行退订
curl -i 'http://localhost:8080/api/unsub?token=<token>'          # 确认页（HTML）
curl -i -X POST -d 'token=<token>' localhost:8080/api/unsub      # 执行退订

# 8. 健康检查
curl localhost:8080/healthz    # {"db":true,"ok":true}
```

## 部署（Zeabur）

1. **挂卷**：服务挂载持久卷到 `/data`（SQLite 文件 `core.db` 所在，容器重建不丢数据）。
2. **环境变量**：按上表设置；`CORE_ENV=prod`（必填项缺失会拒绝启动）、`CORE_ADMIN_TOKEN`、SMTP、R2、`CORE_TURNSTILE_SECRET`、`CORE_BASE_URL=https://core.<你的域名>`、`CORE_SITE_URL=https://<站点域名>`、`CORE_MAILING_ADDRESS`。
3. **域名**：绑定 `core.<你的域名>` 到服务 8080 端口；静态站把 API base 指向它。
4. Dockerfile 即交付物：多阶段构建（`golang:1.26-alpine` → `alpine`）、非 root 运行、`VOLUME /data`、`EXPOSE 8080`、`HEALTHCHECK /healthz`。CI 会执行 `docker build` 兜底。
5. 备份：进程内每 24h 自动快照到 R2 `backups/`，保留 30 天（R2 未配置则跳过）。

## 安全与隐私要点

- 写接口三件套：Turnstile + 蜜罐 `website` 字段 + per-IP 限流（订阅 5/h、评论 10/h、PV 60/min）。
- IP 不落盘明文：统一 `sha256(ip + YYYY-MM-DD)`（UTC）后存储，每日轮换。
- 管理台：HMAC 签名会话 cookie（24h，HttpOnly / SameSite=Lax / prod 下 Secure），口令 `subtle.ConstantTimeCompare`，admin POST 校验 Origin 同域，未配置口令时拒绝登录。
- 评论一键审核链接 = HMAC 签名 `comment_id.action`；邮件 header 全部过滤 CRLF；SQL 全参数化；HTML 输出全走模板转义。
- 群发防 prefetch 误退订：GET `/api/unsub` 只出确认页，POST 才执行。

## 偏离 / 保守选择说明（SPEC 未明确处）

1. **评论审核通知收件人**：SPEC 未定义 `CORE_ADMIN_EMAIL`，通知发往 `CORE_SMTP_FROM` 的地址部分。
2. **PV 接口无 Turnstile/蜜罐**：SPEC §3.3 明确定义 `/api/pv` 只有 `{path, referrer}` + DNT + 限流（逐页打点无法做挑战验证）；§3 开头的三件套按订阅/评论执行。
3. **CSV 导入的 `confirmed` 列**：按 SPEC 加粗条款「导入即 active」，该列仅作旧平台导出格式兼容读取，不改变行为。
4. **`/a/{slug}.mp3` 路由**：Go ServeMux 通配符须占整段路径，故注册 `GET /a/` 后在 handler 内解析 slug（非 `.mp3` 后缀一律 404）。
5. **备份时机**：24h ticker 之外，启动时立即跑一次（部署后崩溃也留有新快照）；prune 失败只 WARN 不影响备份本体。
6. **dev 邮件**：SMTP 未配置时使用 LogMailer（日志输出全文并返回成功）——冒烟测试「从日志拿 token」依赖此行为；prod 模式 SMTP 为必填，不会静默降级。
7. **客户端 IP**：默认取 TCP 对端地址；仅当对端命中 `CORE_TRUSTED_PROXIES`（逗号分隔 CIDR/IP，默认空 = 不信任任何 XFF）时才采信 `X-Forwarded-For` 首值。部署在 Zeabur/Cloudflare 后时把平台出口网段配进来。
