# 用 Hugo 站对接自托管 core —— 集成指南

本文面向「自己有一个 Hugo 站、想自己部署一套 core 补齐动态能力」的人。core 是单进程 Go 服务，给静态站补齐订阅（double opt-in）、评论、Newsletter 群发、下载/访问统计、媒体存储（R2）和 SQLite 定时备份。参考实现：[xiaowenz.com](https://xiaowenz.com)（Hugo 主题「玄墨」，MIT，开源）。

## 架构一句话

```
浏览器 ──(vanilla JS, fetch)──▶ core 公开 API（/api/*, /a/*）
   │                                  │
Hugo 静态站                        SQLite + SMTP + R2
(Cloudflare Pages 等)              (Zeabur / 任意 Docker 宿主)
```

- 所有动态调用都是**浏览器端 JS 直连 core**，Hugo 构建期不参与任何 API 调用，所以对接**零构建环境变量**，改配置即生效。
- core 的 CORS 只放行 `CORE_SITE_URL` 一个源（精确匹配，含协议、不带尾斜杠）。**这个变量必须等于你的站点源，否则浏览器里所有 API 调用都会被 CORS 拦掉**——这是对接时最常见的坑。

## 第一步：部署 core

core 的交付物就是根目录的 Dockerfile（多阶段构建、非 root、健康检查 `/healthz`），任意 Docker 宿主均可跑。以 Zeabur 为例：

1. **挂持久卷到 `/data`**——SQLite 数据库 `core.db` 在这里，容器重建不丢数据。这是唯一硬依赖。
2. **配置环境变量**（完整清单见 [README](../README.zh-CN.md#配置全部环境变量)），prod 必填项：

| 变量 | 填什么 |
|---|---|
| `CORE_ENV` | `prod`（缺失必填项会直接拒启动） |
| `CORE_BASE_URL` | `https://core.<你的域名>`（邮件里的链接） |
| `CORE_SITE_URL` | `https://<你的站点域名>`——**CORS 唯一放行源，精确匹配** |
| `CORE_ADMIN_TOKEN` | 管理台登录口令（强度拉满；同时是评论审核链接的 HMAC key） |
| `CORE_SMTP_HOST` / `CORE_SMTP_FROM` | 发信通道；FROM 地址同时接收评论审核通知 |
| `CORE_TURNSTILE_SECRET` | Cloudflare Turnstile secret（强烈建议，见下） |
| `CORE_TRUSTED_PROXIES` | 平台出口网段（如 Zeabur/Cloudflare 反代后的 CIDR），不配则永不采信 XFF，限流按对端 IP 计 |
| `CORE_R2_*` / `CORE_R2_PUBLIC_BASE` | 要播客下载计数 / 媒体上传 / 自动备份才需要 |

3. **绑域名**：把 `core.<你的域名>` 指到服务的 8080 端口。

部署后先冒烟（详见 README「手工冒烟」）：`POST /api/subscribe` → 202；从日志拿验证链接 `GET /api/verify?token=…` → 302 回站点；`curl <core>/healthz` → `{"db":true,"ok":true}`。

## 第二步：接入你的 Hugo 站

### 路线 A：直接用「玄墨」主题

玄墨主题（参考实现在 [xiaowenz-blog](https://github.com/iamshaynez/xiaowenz-blog) 的 `themes/xuanmo/`）已内置全部对接：评论、订阅表单、PV 统计、播客 enclosure 计数。只需在 `config.yaml` 的 `params` 里填三个值：

```yaml
params:
  coreAPI: "https://core.<你的域名>"        # 评论/订阅/统计 API 根地址（无尾斜杠）
  turnstileSiteKey: ""                     # Cloudflare Turnstile site key；空 = 不加载验证组件
  audioBase: "https://core.<你的域名>/a/"  # 播客 enclosure 计数前缀（仅播客站需要）
```

- `coreAPI` 留空 = 主题静默关闭全部动态功能，站退化为纯静态。
- 想配 Turnstile：去 Cloudflare 控制台为你的站点域名建 widget，把 **site key** 填进 `turnstileSiteKey`，**secret key** 填进 core 的 `CORE_TURNSTILE_SECRET`。

### 路线 B：给自己的主题写对接

对接面很小：主题在页面注入一个全局配置对象，前端 JS 按下面的契约调 API 即可。参考实现：`themes/xuanmo/layouts/partials/footer.html` + `themes/xuanmo/static/js/{comments,subscribe,pv}.js`。

**1. 注入配置（Hugo 模板，通常在 footer partial）：**

```html
<script>
  window.XW = {{ dict "coreAPI" ($params.coreAPI | default "")
                     "turnstileSiteKey" ($params.turnstileSiteKey | default "") | jsonify | safeJS }};
</script>
```

**2. 前端调用契约**（全部 `POST` + `Content-Type: application/json`，成功语义见响应列）：

| 功能 | 端点 | 请求体 | 响应 |
|---|---|---|---|
| 拉评论 | `GET {coreAPI}/api/comments?post={slug}` | — | `{"comments":[{"author_name","content","created_at"}]}` |
| 发评论 | `POST {coreAPI}/api/comments` | `{post,name,email,content,website,turnstile_token}` | 202 进审核队列 |
| 订阅 | `POST {coreAPI}/api/subscribe` | `{email,name,website,turnstile_token}` | 202，验证邮件发出 |
| PV 打点 | `POST {coreAPI}/api/pv` | `{path,referrer}`，建议 `keepalive: true` | 204/忽略 |

字段约定：

- `website` 是蜜罐字段：对真人留空、对爬虫预填，**不要渲染成可见输入框**。
- 评论的 `email` **选填**：留空直接入库（author_email 为空串），留了则必须为合法地址（否则 400 `invalid email`）。前端不要给邮箱加 `required`。
- 发文后群发 Newsletter **不需要任何本地脚本**：core admin「新建 Newsletter」页可直接粘贴文章 URL 抓取生成草稿（标题/导语/链接自动填），人工检查后点发送。
- `turnstile_token`：配了 Turnstile 时必传（评论与订阅都强制校验，不配 secret 的 dev 模式除外）。在页面加载 Turnstile widget，`token = turnstile.getResponse(widgetId)`。
- 评论列表的作者是 `author_name`（不是 `name`）；时间是 RFC3339 的 `created_at`。
- 订阅/评论的 202 都是「已受理」，提示语写「请查收验证邮件 / 等待审核」。
- DNT：尊重浏览器的 Do Not Track，置位时跳过 PV 打点。

**3. 播客下载计数（可选，模板直拼 URL，不经 JS）：**

把 RSS enclosure 和页面播放器的音频地址统一写成 `{audioBase}{audio_slug}.mp3`。core 收到 `GET /a/{slug}.mp3` 后计数（同日同 IP 去重）并 302 到 R2 真实文件。要求：音频文件通过 core 管理台（`/admin` → 媒体）上传到 R2，slug 与文章 frontmatter 的 `audio_slug` 一致。

**4. 无 JS 降级（可选但推荐）**：订阅表单可以直接写 `<form method="post" action="{coreAPI}/api/subscribe">` 原生提交——core 接受 `application/x-www-form-urlencoded`，无 JS 也能完成订阅（此时没有 Turnstile token，prod 下会被拒，属预期）。

## 验收清单

部署 + 接入后逐项过一遍：

- [ ] 浏览器（非 curl）提交订阅 → 收到 double opt-in 验证邮件 → 点链接 → 302 回 `https://<你的域名>/subscribed/`
- [ ] 浏览器提交评论 → 管理台（`https://core.<你的域名>/admin`）可见 → 邮件里点 approve → 文章页评论区显示
- [ ] 管理台发一封测试 Newsletter → 订阅者收到、含个人退订链接
- [ ] 播客：播放器/feed 的 mp3 地址走 `{audioBase}` → 响应 302 → 管理台统计页计数 +1
- [ ] 控制台无 CORS 报错（有就查 `CORE_SITE_URL` 是否精确等于站点源）
- [ ] `curl https://core.<你的域名>/healthz` → `{"db":true,"ok":true}`

## 抄主题代码前注意（旧版主题的已知偏差，已修复）

参考主题「玄墨」早期版本的 JS 与 core 契约有三处不一致，**最新版主题已修复**；如果你抄的是旧快照，请按本文契约自行对齐：

1. Turnstile 字段名：core 收 `turnstile_token`，旧版主题订阅 JS 发的是 `cf-turnstile-response`。
2. 评论缺 token：旧版主题的评论 JS 不传 `turnstile_token`，prod 配了 `CORE_TURNSTILE_SECRET` 时评论提交会被 403；评论区 widget 为 JS 动态渲染，需要显式调用 `turnstile.render`。
3. 评论列表字段：core 返回 `author_name`，旧版主题 JS 读的是 `name`。

## 更多

- API/数据模型/管理台全貌：[SPEC.md](SPEC.md)
- 环境变量与 prod 强校验：[README](../README.zh-CN.md#配置全部环境变量)
- 参考站完整配置：[xiaowenz-blog 的 config.yaml](https://github.com/iamshaynez/xiaowenz-blog)
