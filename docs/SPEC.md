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
| `CORE_R2_ENDPOINT/KEY_ID/SECRET/BUCKET/PUBLIC_BASE` | 空 | **媒体桶兜底**：管理台「系统参数」页（settings 表 `media_r2_*`）逐字段优先，环境变量仅补缺；有效配置不完整则媒体上传禁用。备份 R2（`backup_r2_*`）仅认后台配置，无环境变量回退 |
| `CORE_TURNSTILE_SECRET` | 空 | 空 = dev 模式跳过校验并打 WARN |
| `CORE_ENV` | `dev` | `prod` 时开启更严格行为（cookie Secure 等） |

启动时校验：`prod` 模式下 ADMIN_TOKEN、SMTP、BASE_URL/SITE_URL 为必填，缺失则退出非零。

## 2. 数据模型（迁移 0001 一次建好，后续加法式演进）

- `subscribers`: id, email UNIQUE（小写归一化）, status(`pending|active|unsubscribed`), confirm_token, confirm_expires_at, unsub_token, created_at, confirmed_at, updated_at
- `comments`: id, post_slug, author_name, author_email（不公开）, content, status(`pending|approved|rejected`), ip_hash, ua, created_at, moderated_at
- `newsletters`: id, subject, markdown, html, status(`draft|sending|sent|failed`), sent_count, failed_count, created_at, sent_at
- `downloads`: id, slug, ip_hash, ua, referer, created_at ；统计口径=按 (slug, ip_hash, 日) 去重
- `pageviews`: id, path, referrer, ip_hash, ua, site, created_at —— **有界热层**：只保留 `CORE_PAGEVIEW_RETENTION_DAYS`（默认 30）天，行宽刻意瘦身（ip_hash 16 hex、ua 只存设备分类单字符）；site 维度为多站统计预留（当前恒空串）
- `daily_stats`（迁移 0003）: site, day, pv, uv, PK(site, day) —— **永久日聚合层**：夜间压缩任务把完结日从 pageviews 精确重算（同日 ip_hash 去重）upsert 进来，随后清掉保留期外的明细。ip_hash 按日加盐意味着跨日访客本就不可链接：多日 UV=各日去重之和，清理明细对指标无损
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
- `POST /api/comments` `{post, name, email, content, turnstile_token, website}` → 202，状态 pending，限流 10 次/小时/IP；post 长度 1–200，内容长度 1–2000，name 1–50；**email 选填**（留空跳过校验，留了必须为合法地址）；给管理员发通知邮件（含 approve/reject 一键链接，带签名 token）。
- `GET /api/comments?post={slug}` → 200 `{comments:[{author_name, content, created_at}]}` 仅 approved，按时间正序。
- `GET /api/comments/moderate?token=` → admin 邮件里的一键 approve/reject（HMAC 签名 comment_id+action+expiry，链接 7 天有效）。`CORE_ADMIN_TOKEN` 为空时该端点整体禁用（签名密钥会退化为公开常量，必须 fail-closed）。

### 3.3 统计
- `POST /api/pv` `{path, referrer}` → 204；尊重 `DNT: 1`（直接丢弃）；限流 60 次/分/IP（在门禁之前，未知来源同样吃 429）。
- **多站打点门禁**：站点身份取自 **Origin 头**（浏览器跨域 POST 必带，客户端不可伪造）——`CORE_SITE_URL` 同域记为 `site=''`（博客存量连续）；管理台「系统参数」页登记的域名（settings 表 `stats_sites`，保存即时生效、无环境变量）记为其 host；其余（含无 Origin 的非浏览器请求）一律 204 静默丢弃。sendBeacon 的 text/plain 文本体按 JSON 解析（简单请求，无 CORS 预检，天然兼容多站）。接入方法见 `docs/multisite-stats.md`（内联 snippet，core 挂了不影响各站）。
- `GET /a/{slug}.mp3` → 查 media 表 key=`audio/{slug}.mp3`：命中记 download（含 Range 请求也记，但同日同 IP 去重只算一次）→ 302 到 R2 URL；未命中 404。
- **存储分两层**（防 DB 无界膨胀）：raw `pageviews` 只留保留期内（路径/来源/小时等明细维度的数据地基，全部按 site 隔离——管理台统计页均按博客口径 site='' 查询）；`daily_stats` 永久存逐日 PV/UV（按 site 分行）。读数规则：窗口落在保留期内走 raw 精确路径（精确到时间戳）；更宽的窗口合并 daily_stats + 尚未聚合的 raw 天（rollup 缺位自愈、同日不双计；raw 兜底按 (site, day) 对排除，他站 rollup 行不误伤本站）。夜间压缩（`CORE_STATS_CRON`，默认 `30 3 * * *`，先于 04:00 备份让快照已是压缩后的小库）：rollup → prune 同事务保序（未聚合的天绝不被删）→ 有删除时 wal_checkpoint+VACUUM（DELETE 不缩文件，VACUUM 才真还给文件系统）；启动即跑一次（部署即完成首次压缩，也补上停机错过的夜晚）。

### 3.4 健康检查
- `GET /healthz` → `{"ok":true,"db":true}`（db ping 失败则 503）。

## 4. 管理台

管理台整体采用 editorial 设计语言（纸感底色/宋体标题/胶囊控件），页面骨架见 `internal/admin/templates/_layout.html`；仪表盘含下载量条形图与最近待审评论就地审核，评论/订阅页状态页签带实时计数，Newsletter 发送前展示收件人数并强制确认。（/admin，服务端渲染 html/template，无前端框架）

认证：`POST /admin/login`（口令常量时间比较）→ 签发 HMAC 签名会话 cookie（365 天，HttpOnly/Secure/SameSite=Lax；管理台为个人低频工具，长期免重登录）；`POST /admin/logout` 过期会话 cookie。中间件守卫所有 /admin/*（login 除外）。admin POST 校验 Origin 头同域。会话为无状态 HMAC 值：登出只清本浏览器 cookie，整体吊销需轮换 `CORE_ADMIN_TOKEN`。

- `GET /admin` 仪表盘：active 订阅数、pending 评论数、近 30 天 PV/UV（按 ip_hash 去重）、各剧集近 30 天下载量（条形图）与最近待审评论就地审核。
- `GET /admin/stats/html?days=1|7|30|90[&site=<host>]` 流量分析台：总量卡、**浏览时间线**（PV/UV 双柱、零流量桶补零、柱顶标 PV 数字；days=1 时切小时粒度，其余日粒度）、**热门路径 TOP 20**（PV/UV/占比）、**访问来源 TOP 20**（占比条；空 referrer 显示"直接访问"）、剧集下载量（本站专属，仅默认口径显示）、**多站总览**（各接入站今日/近 7 天/近 30 天 PV/UV，即 DAU 表；**站名为链接，点进该站过滤视图**）；四档时间窗同时作用于概要卡、时间线与路径/来源榜。`site` 缺省 = 本站博客口径（site=''）；传站点 host 时**整页**（总量/时间线/路径/来源）切到该站，窗口 chips 与返回链接保留口径。时间线与总量在 90 天窗下由 daily_stats 永久层支撑（明细清理后历史日照常出数）；路径/来源榜依赖 raw 明细，覆盖保留期（默认 30 天）。日界按进程本地时区（部署侧 `TZ` 对齐受众时区，容器镜像含 tzdata）。
- Newsletter 删除：`POST /admin/newsletters/{id}/delete`（草稿/已发可删，**sending 在途拒删 409**；已发删除连带清理 send_log，事务内完成不留孤儿投递记录）。
- Newsletter 从文章导入：`POST /admin/newsletters/import`（表单 `url`）→ 抓取已公网上线的博客文章，标题进主题、`og:description`/`meta description` 进导语、原文链接进正文，直接落 **draft**（绝不自动发送）。URL 的 host 必须落在 `CORE_SITE_URL` 派生的域名后缀内（如 `blog.xiaowenz.com` → 允许 `xiaowenz.com` 与 `*.xiaowenz.com`），站外一律 400（SSRF 防线）；抓取 10s 超时、正文上限 1 MiB、非 200 → 502。
- 分发管理：`GET /admin/distribution`（媒体库入口）——feed 自检（enclosure 字节 ↔ 媒体库勾稽）、Apple 目录比对（iTunes 公开 lookup API：已收录最新一集、滞后天数、登记的 feed 域名哨兵）、小宇宙节目页配置与机制说明；平台配置（`apple_show_id`/`apple_country`/`xiaoyuzhou_url`）经 `POST /admin/distribution/settings` 存 settings 表。Apple 无可用 ping 端点，官方刷新路径是 Podcasts Connect 手动刷新；收录节奏 ≤24h。
- 播客对账：`GET /admin/media` 拉取 `{CORE_SITE_URL}/feed/audio.xml` 与媒体库 `audio/*` 互相勾稽，逐集回答：**已上线**（音频已传且 feed 有对应剧集）/ **未上线**（音频已传、剧集 md 未发——附可复制的剧集 frontmatter 片段，`audio_slug`/`audio_bytes` 与媒体库真实值对齐）/ **缺音频**（feed 剧集指向未上传的音频，enclosure 会 404，红牌警示）。feed 拉不到时对账区标注不可用，页面不 500；缓存口径同博客文章源（10 分钟）。
- 博客文章源联动：`GET /admin/newsletters/new` 顺带拉取 `{CORE_SITE_URL}/feed.xml`（派生自既有配置，不新增 env），最近文章（≤30 条，含博客与播客条目）渲染成下拉列表，选中提交即走上述 import——全程零手动输入。feed 文章链接的主机统一改写为 feed 实际来源源（Hugo feed 内是 baseURL 绝对地址，apex 切换前点 canonical 会 404）。feed 内存缓存 10 分钟；拉取失败页面照常渲染，手动粘 URL 永远是兜底。
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

进程内 cron 调度（默认 `0 4 * * *`，管理台「备份」页可改、存 settings 表 `backup_cron`，保存即生效无需重启）：`PRAGMA wal_checkpoint(TRUNCATE)` → `VACUUM INTO` 临时文件 → 上传私有桶 `backups/core-YYYYMMDD-HHMMSS.db` + 同名 `.meta.json` 配置摘要（时间/schema 版本/非机密运行配置，机密只记"已配置"布尔，绝不落值）→ 按时间戳保留最近 7 份（db 与 meta 配对删除）。备份完成打结构化日志。

两套 R2 目标统一在管理台「系统参数」页（`internal/sysconfig`）配置，存 settings 表：媒体 `media_r2_*`（公开桶，逐字段回退环境变量 `CORE_R2_*`）、备份 `backup_r2_*`（私有桶，无环境变量回退——环境变量指向公开媒体桶，快照绝不能落进去）。同页管理**统计站点** `stats_sites`（多站打点域名白名单，每行一个纯域名，归一化保存、非法整单拒绝，保存即时生效）。客户端按次重建，改动即时生效、无需重启；页面按字段显示生效来源（数据库/环境变量/未设置）。凭据为明文存储但页面永不回显（secret 留空=沿用旧值），且随数据库快照一起入桶——因此**备份桶必须私有**（不绑公网域名、不开放公开访问），建议使用仅授权该桶的 R2 API Token。配置不完整=对应功能禁用（WARN），绝不部分写入。

管理台备份页：R2 配置表单、当前 cron/下次运行/历史列表，手动「立即备份」（与定时互斥，忙时返回提示），「测试 R2 连接」（对备份桶做写入→列举→删除探针回路）。启动时立即跑一次（部署后崩溃也留有新快照）；prune 失败只 WARN 不影响备份本体。

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
