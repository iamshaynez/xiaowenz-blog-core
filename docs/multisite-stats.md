# 多站打点接入指南（core 作为自托管 GA-lite）

core 的 `POST /api/pv` 同时服务多个站点：站点身份取自浏览器 **Origin 头**
（跨域 POST 必带、客户端不可伪造），无需客户端传任何身份字段。core 挂掉时
各站完全不受影响——打点是火后不理（fire-and-forget）的后台 POST，不在任何
站点的关键路径上。

## 一、core 侧（管理员，一次性）

1. 管理台「系统参数」页 → **统计站点** 卡片 → 每行一个域名（如
   `ask.xiaowenz.com`），保存即时生效，无需重启。
2. 本站（`CORE_SITE_URL` 的域名）天然在列，不用登记。
3. 未登记域名的打点一律 204 静默丢弃：不落库、不报错、不污染。

各站 DAU 在管理台「统计」页的**多站总览**表查看（今日 / 近 7 天 / 近 30 天
PV/UV）。

## 二、站点侧（每个站，30 秒）

把下面这段贴进页面模板（`</body>` 前；SPA 贴进唯一的 HTML 壳即可）：

```html
<script>
(function(){
  if (navigator.doNotTrack === '1') return;           // 尊重 DNT
  var send = function(){
    try {
      var b = JSON.stringify({path: location.pathname, referrer: document.referrer});
      if (navigator.sendBeacon('https://core.xiaowenz.com/api/pv', b)) return;
      fetch('https://core.xiaowenz.com/api/pv', {method:'POST', body:b, keepalive:true, mode:'no-cors'});
    } catch(e) {}                                      // 静默失败：core 挂了也不影响本站
  };
  if (document.visibilityState === 'prerender')
    addEventListener('visibilitychange', function(){ if(document.visibilityState==='visible') send(); }, {once:true});
  else send();
})();
</script>
```

要点：

- **内联**，不从 core 加载任何 JS 文件——core 挂掉时页面渲染零影响。
- `sendBeacon` / `mode:'no-cors'` 的 text/plain POST 是"简单请求"，
  **无需 CORS 放行、没有预检**，浏览器不读响应，失败也静默。
- 无重试逻辑：core 不可用时不会反复轰炸，丢的只是统计数字。
- 有严格 CSP 的站点需在 `connect-src` 加 `https://core.xiaowenz.com`；
  没有 CSP 的站什么都不用改。
- SPA 想统计路由切换：在路由钩子里调 `send()`，并把 `path` 换成路由路径
  （协议不变）。

## 三、口径

- PV：每次 `send()` 一次；UV：同日同 ip_hash 去重（ip 按日加盐哈希，不落
  明文，跨日不可链接）。
- 每站独立统计，互不混入；明细（路径/来源/小时粒度）保留 30 天，逐日
  PV/UV 永久（见 SPEC §2 两层存储）。
- 全局限流 60 次/分/IP（跨站共享同一 IP 额度）。
