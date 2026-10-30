#!/bin/bash
# gb-test 服务器 /opt/apps/core/build.sh 的版本化副本——改动两处同步。
# 受限命令（authorized_keys 强制命令锁定本脚本）：CI 经 ssh 以「远程命令」形式传入
# 目标 commit sha，sshd 将其放进 $SSH_ORIGINAL_COMMAND——只接受 40 位十六进制，
# 其余一律拒绝（密钥除构建部署本仓库外做不了任何事）。
# 流程：拉源码 tarball（仓库公开，codeload 直取）→ docker build（双标签 sha+main）
# → docker push（服务器→ACR 国内路径秒级；GitHub 美国 runner 推此端点会 blob 上传
# 停滞，故构建推送整体在服务器侧，与 sanguo / novel-universe 同构）
# → exec deploy.sh（pull 幂等 + up + 冒烟）。
set -uo pipefail
cd /opt/apps/core || { echo "部署目录不存在"; exit 1; }

SHA="${SSH_ORIGINAL_COMMAND:-}"
[[ "$SHA" =~ ^[0-9a-f]{40}$ ]] || { echo "非法参数（期望 40 位 commit sha）: $SHA"; exit 1; }

# shellcheck disable=SC1091
source ./.env   # ACR_REGISTRY=...
IMG="${ACR_REGISTRY}/xiaowenz/core"

echo "== 拉源码 $SHA =="
rm -rf src && mkdir src
curl -fsSL "https://codeload.github.com/iamshaynez/xiaowenz-blog-core/tar.gz/${SHA}" -o src/src.tar.gz \
  || { echo "源码拉取失败"; exit 1; }
tar xzf src/src.tar.gz -C src --strip-components=1

echo "== 构建 $IMG:$SHA / :main =="
( cd src && docker build -t "$IMG:$SHA" -t "$IMG:main" . ) || { echo "构建失败"; exit 1; }

echo "== 推送 ACR（服务器侧，国内路径） =="
docker push "$IMG:$SHA" || { echo "推送 :$SHA 失败"; exit 1; }
docker push "$IMG:main" || { echo "推送 :main 失败"; exit 1; }

echo "== 部署并冒烟 =="
exec /opt/apps/core/deploy.sh
