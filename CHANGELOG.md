# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

Full-codebase review pass (architecture, quality, bugs). All changes test-first.

### Added (newsletter full-article email & selective sending)

- **全文邮件**：文章导入改为抓取主题正文容器（玄墨 `div.article__body`，兼容
  `e-content`/`itemprop=articleBody`）整篇正文，经邮件化清洗（相对 URL 以文章
  地址绝对化、清 schema/script 噪音、注入邮件客户端安全 inline style）后落库；
  无容器回退摘要模式。邮件壳重设计：页眉「三杯过后 · 专栏博客文章订阅」、文章
  大标题、全文、「阅读原文」按钮、退订页脚；text 备选改为正文真实纯文本
  （`newsletters` 新增 `post_url` 列，迁移 0004；新依赖 `golang.org/x/net`）。
- **HTML 全邮件预览**：`GET /admin/newsletters/{id}/preview` 渲染订阅者视角
  完整邮件（与发送同一壳，退订链接 `#` 占位），草稿页 iframe 所见即所得。
- **选择发送与补发**：草稿页收件人选择器（全部/已发送/待发送统计、邮箱搜索、
  已发送置灰），`POST send` 携带 `ids[]`，空选择 400；收件人 = 勾选 ∩ active ∩
  尚未送达，**同一邮箱地址绝不发第二次**（send_log `status='sent'` 判重，
  failed 可重试）；一轮结束仍有未送达回 draft 等补发（计数累加），全部送达才
  置 sent。

### Added (per-site drill-down on the stats page)

- **统计页按站过滤**：`GET /admin/stats/html?site=<host>` 把整页（总量卡/时间线/
  热门路径/访问来源/小时粒度）切到该站口径；缺省仍是本站博客。多站总览的
  站名变为链接点进对应视图，窗口 chips 与「← 本站」返回链接全程保留口径；
  剧集下载为本站专属卡片，仅默认口径显示。store 查询本就带 site 维度，
  本次只动 admin 页与模板。

### Added (multi-site pageview beacons — core as self-hosted GA-lite)

- **多站打点**：`POST /api/pv` 升级为多站端点。站点身份取自 **Origin 头**
  （浏览器跨域 POST 必带，客户端不可伪造）：`CORE_SITE_URL` 同域记为
  `site=''`（博客统计与存量数据无缝连续）；后台登记域名记为其 host；
  其余（含无 Origin 的非浏览器请求）204 静默丢弃，不污染。
- **域名白名单进后台**（无环境变量）：「系统参数」页新增「统计站点」卡片，
  settings 表 `stats_sites` 每行一个纯域名，归一化（去 scheme/端口、小写、
  去重）保存、非法条目整单拒绝，保存即时生效无需重启。
- **sendBeacon 兼容**：text/plain 文本体按 JSON 解析——sendBeacon /
  `fetch(mode:'no-cors')` 属简单请求，无 CORS 预检，各站一段内联 snippet
  即接入（`docs/multisite-stats.md`，含"core 挂了不影响各站"的故障模式
  说明）；限流 60 次/分/IP 提前到门禁之前。
- **读数按 site 隔离**：store 全部统计查询（总量/时间线/小时/路径/来源）
  增加 site 维度，管理台统计页维持博客口径；raw 兜底合并改按 (site, day)
  对排除，他站 rollup 行不误伤本站。统计页新增**多站总览**表（各站今日/
  近 7 天/近 30 天 PV/UV，即 DAU）。

### Added (bounded pageview statistics — two-tier storage)

- **统计存储分两层，DB 不再无界膨胀**（迁移 0003）：raw `pageviews` 变为
  有界热层（默认保留 30 天，`CORE_PAGEVIEW_RETENTION_DAYS`，2–3650 可调）；
  新增 `daily_stats` 永久日聚合层（site+day 主键，pv/uv 精确值）。夜间压缩
  任务（`internal/stats`，cron `CORE_STATS_CRON` 默认 03:30，先于 04:00 备份
  让快照已是压缩后的小库）单事务内先 rollup 后 prune（未聚合的天绝不被
  删，当天未完结不动），有删除时 wal_checkpoint + VACUUM 真正回收文件体积
  （DELETE 只挂 freelist 不缩文件）；启动即跑一次，补上停机错过的夜晚。
- **读数合并语义**：窗口落在保留期内走 raw 精确路径（精确到时间戳，24 小时
  窗行为不变）；更宽的窗口（如 90 天）合并 daily_stats + 尚未聚合的 raw 天，
  rollup 缺位自愈、同日不双计。ip_hash 按日加盐意味着跨日访客本就不可链接
  ——多日 UV=各日去重之和，与旧口径一致，清理明细对指标无损。
- **行宽瘦身**：ip_hash 截到 16 hex（64 bit，按日去重尺度碰撞可忽略）；
  pageviews 的 ua 只存设备分类单字符（m/d/b/o），整条 UA 不落盘。
  comments/downloads 不变。
- **多站预留**：pageviews 加 `site` 列（当前恒空串）、daily_stats 主键含
  site 维度——后续把 core 用作多站 GA-lite 时零迁移。
- 配置项 `CORE_STATS_CRON` / `CORE_PAGEVIEW_RETENTION_DAYS` 同步进
  `.env.example` 与两份 README 配置表；SPEC §2/§3.3/§4 更新两层口径说明。

### Added (CI auto-deploy)

- **push main → gb-test 自动构建部署**（sanguo / novel-universe 同构）：CI 新增
  deploy job（go + docker 双 job 全绿后触发，`concurrency: deploy-main` 排队不
  取消），经受限 SSH 密钥把 commit sha 传给服务器上的 `build.sh`——服务器侧
  拉源码 → docker build（sha + main 双标签）→ 推阿里云 ACR → `deploy.sh`
  （pull 幂等 + compose up + `/healthz` 冒烟，失败 dump 日志打红 CI）。
  authorized_keys 强制命令锁死密钥（sha 只认 40 位十六进制），主机指纹
  DEPLOY_KNOWN_HOSTS 钉死；secrets 仅 4 个（DEPLOY_*），CI 不接触 ACR 凭据。
  服务器脚本版本化副本在 `docker/`（build-remote.sh / deploy-remote.sh /
  docker-compose.yml / 服务器初始化.md——改动两处同步）。一次性迁移已完成：
  手工 docker run 容器由 compose 接管（bind mount / env / apps 网络 / 18092
  端口逐项对齐，数据不丢；旧本地镜像 xiaowenz-core:latest 留作回滚）。

### Added (console-managed system parameters + backup scheduling)

- **Dedicated「系统参数」(system settings) admin page** (`internal/sysconfig`):
  both R2 targets — media (public bucket) and backup (private bucket) — are
  configured in the console and stored in the settings table. Resolution is
  settings-first with per-field fallback to `CORE_R2_*` env vars for media
  (existing deployments keep working; console values override without a
  restart, clients rebuild per operation). The page shows each field's
  effective source (database / env / unset); secrets are write-only (blank
  keeps the stored value, never rendered back); malformed values are
  rejected. Backup R2 is console-only by design — the env vars point at the
  public media bucket, which must never receive snapshots.
- **Cron-scheduled backups to a console-configured private R2 bucket**
  (`robfig/cron/v3`, zero transitive deps, added for battle-tested 5-field
  cron parsing). The schedule defaults to `0 4 * * *` and the whole R2 target
  (endpoint, access key, secret, bucket) is managed in the admin console —
  stored in the settings table, no environment variables involved; the client
  is rebuilt per operation so console edits apply without a restart. Backups
  can be triggered manually; retention is the most recent 7 snapshot pairs
  (was: fixed 24h ticker, 30-day retention, media bucket). Each snapshot
  ships with a `.meta.json` config digest (schema version, cron, non-secret
  service config — secrets are recorded as presence flags only). The stored
  backup secret is never rendered back (blank field keeps the old value), and
  since credentials live in the DB they travel with its backups — the console
  warns that the backup bucket must stay private. The console also gained a
  backup page (history, next-run, "run now", "test R2 connection"
  write→list→delete probe); concurrent triggers return a busy message instead
  of stacking.

### Security

- **X-Forwarded-For spoofing**: `clientIP` now walks the honored XFF chain
  right-to-left, skipping trusted proxies, instead of taking the first entry —
  a client behind the trusted CDN could prepend fake hops and rotate IPs to
  evade every per-IP rate limit (subscribe/comments) and poison download
  dedupe / UV stats.
- **One-click comment moderation is disabled when `CORE_ADMIN_TOKEN` is
  empty**: the HMAC signing key would degrade to a publicly derivable
  constant, making forged approve/reject links trivial (mirrors the
  "empty token = admin disabled" rule).
- **`POST /api/unsub` caps its body at 4 KiB**: the endpoint is public and
  unauthenticated; an unbounded JSON fallback decoder buffered
  attacker-sized payloads in memory.
- **SVG uploads are rejected** (`image/svg` / `image/svg+xml`): SVG executes
  scripts on the media origin — a stored-XSS carrier excluded by name from
  the image whitelist.
- **Subscriber CSV export neutralizes formula injection**: cells starting
  with `=`, `+`, `-`, `@`, tab or CR are prefixed with a single quote; a
  legal address like `=HYPERLINK@x.com` would otherwise execute on open in
  spreadsheet apps.
- **Moderation links expire after 7 days**: the token is now
  `id.action.expiry` under the HMAC (previously valid forever; links issued
  before this change stop working on deploy).

### Added

- Admin logout (`POST /admin/logout`, nav button) expires the session cookie.
- Newsletters carry `List-Unsubscribe-Post: List-Unsubscribe=One-Click`
  (RFC 8058) alongside `List-Unsubscribe`, enabling Gmail-style one-click
  unsubscribe (the endpoint already accepts the RFC 8058 POST).

### Fixed

- Admin stats endpoints (JSON + HTML) and the subscriber list no longer
  swallow count-query errors; they now fail with 500 like every other query.

### Changed

- Comment `post` field is capped at 200 chars (was unbounded up to the 1 MiB
  body limit), keeping junk rows out of the moderation queue.

### Performance

- CSV subscriber import runs as a single transaction instead of one implicit
  commit per row (a 10 MB legacy CSV previously fsynced once per email).

## [0.1.0] - 2026-09-19

Initial public release. Built test-first under the engineering constitution in
[AGENTS.md](AGENTS.md) (106 behavioral tests, `-race` clean, zero-network test suite)
against the functional contract in [docs/SPEC.md](docs/SPEC.md); independently audited
before release.

### Added

- **Newsletter**: double opt-in subscription flow (verify → welcome), prefetch-safe
  one-click unsubscribe, `List-Unsubscribe` header, Markdown-to-HTML campaigns
  (goldmark) sent in the background with per-recipient unsubscribe links,
  pacing, send log, and crash-safe status recovery.
- **Subscriber management**: admin list with status filter/pagination, CSV import
  (legacy-platform compatible, validated) and export.
- **Comments**: moderated queue, admin notification email with HMAC-signed
  one-click approve/reject links, public approved-only JSON feed.
- **Podcast analytics**: `GET /a/{slug}.mp3` counts downloads with same-day
  per-listener dedupe, then 302-redirects to R2 (Podtrac-style).
- **Stats**: DNT-aware page-view beacon; admin dashboard (subscribers, pending
  comments, 30-day PV/UV, per-episode downloads).
- **Media**: admin uploads to Cloudflare R2 (S3) with MIME whitelist and
  collision-safe naming.
- **Persistence**: embedded SQLite (WAL) with goose auto-migrations; daily
  snapshot backups to R2 with 30-day retention.
- **Security baseline**: Turnstile + honeypot + per-IP rate limits on public
  write endpoints, HMAC session cookies, constant-time token compare, Origin
  checks on admin POSTs, CRLF-filtered email headers, SHA-256 daily-rotated IP
  storage, trusted-proxy-only XFF, request body caps and full server timeouts.
- **Ops**: multi-stage Dockerfile (non-root, `VOLUME /data`, healthcheck),
  graceful shutdown, `slog` structured logging, CI (test/vet/fmt/build/docker).
