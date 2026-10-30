#!/bin/bash
# gb-test 服务器 /opt/apps/core/deploy.sh 的版本化副本——改动两处同步。
# 由 build.sh 末尾 exec 触发，也可手动重部署当前 :main：
#   拉镜像(3 次重试抗跨境瞬断) → docker compose up -d → 冒烟轮询 /healthz
#   (200 即就绪，Go 进程含 goose 迁移，多给点轮次) → 失败 dump 容器日志
#   并以非零退出把 CI 打红。
# 回滚：/opt/apps/core/docker-compose.yml 的 image 改回本地旧镜像
#   xiaowenz-core:latest（迁移期手建，仍保留在服务器）再 up -d。
set -uo pipefail
cd /opt/apps/core || { echo "部署目录不存在"; exit 1; }

ok=0
for i in 1 2 3; do
  if docker compose pull; then ok=1; break; fi
  echo "pull 失败,第 $i 次重试"; sleep 8
done
[ "$ok" = 1 ] || { echo "镜像 pull 三次均失败"; exit 1; }

docker compose up -d || { echo "compose up 失败"; docker logs xiaowenz-core --tail 30; exit 1; }

for i in $(seq 20); do
  C=$(curl -s -o /dev/null -w "%{http_code}" http://127.0.0.1:18092/healthz || true)
  case "$C" in
    200) echo "core 就绪($C,第 $i 轮)"; exit 0;;
  esac
  sleep 3
done

echo "core 未就绪"
docker logs xiaowenz-core --tail 30
exit 1
