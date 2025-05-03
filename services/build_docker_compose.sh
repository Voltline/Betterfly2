set -e  # 让脚本在任意命令失败时立即退出

chmod 764 ../tool/bin/*
make -C ../proto
sudo docker compose up -d --build --remove-orphans --force-recreate
