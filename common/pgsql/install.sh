sudo apt update
sudo apt install postgresql postgresql-contrib -y
sudo systemctl start postgresql
sudo systemctl enable postgresql      # 开机自启
sudo systemctl status postgresql      # 查看状态
