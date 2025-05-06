#!/bin/bash
set -e  # 让脚本在任意命令失败时立即退出

./../common/ws_ssl/generate_self_signed_cert.sh

make -C ../proto
sudo docker compose up -d --build --remove-orphans --force-recreate
