# Betterfly2 回归测试

本文档记录当前可直接执行的回归测试入口，优先面向本地 `docker-compose` 联调环境。

## Friend/Group 端到端回归测试

当前好友与群聊主链路的端到端测试位于：

- [friend_service_e2e_test.go](Betterfly2/services/dataForwardingService/integration/friend_service_e2e_test.go)

### 覆盖范围

这条测试会真实连接 `df` / `df2` WebSocket，并串过 `auth_service`、`friend_service`、`storage_service`、Redis、Kafka 和 PostgreSQL，覆盖以下流程：

- 用户注册与登录
- 添加好友
- 查询好友列表
- 更新好友备注
- 更新好友通知开关
- 删除好友
- 创建群组
- 查询群信息
- 加入群组
- 查询群成员列表
- 更新群头像
- 跨 pod 群消息转发
- 用户退群
- 退群后再次发送群消息，确认离群成员不再收到

### 前置条件

1. 已正确配置 [services/.env](Betterfly2/services/.env)，尤其是：
   - `PGSQL_DSN`
2. 已启动本地 `docker-compose` 环境：

```bash
cd ./Betterfly2/services
docker compose up -d
```

如果最近改过 `auth_service`、`df`、`df2`、`friend_service` 或共享代码，建议先重建：

```bash
docker compose up -d --build auth_service df df2 friend_service
```

### 运行命令

在 [services/dataForwardingService](Betterfly2/services/dataForwardingService) 目录执行：

```bash
env BETTERFLY_E2E=1 \
GOPROXY=https://proxy.golang.org,direct \
GOCACHE=/tmp/betterfly-go-cache-dataforwarding \
GOMODCACHE=/tmp/betterfly-go-mod-dataforwarding \
go test -v ./integration
```

### 说明

- 这组测试默认不会参与普通单测执行，只有设置 `BETTERFLY_E2E=1` 才会真正连本地服务。
- 测试会自动生成测试账号和群 ID，避免和日常联调数据冲突。
- 如果当前数据库环境较脏，导致注册链路异常返回 `ACCOUNT_EXIST`，测试会记录这一现象，并在必要时为后续登录链路预置测试用户，以保证后半段 friend/group 主流程仍能完成验证。

### 最近一次验证结果

在本地 `docker-compose` 环境下，以下命令已实际跑通：

```bash
env BETTERFLY_E2E=1 \
GOPROXY=https://proxy.golang.org,direct \
GOCACHE=/tmp/betterfly-go-cache-dataforwarding \
GOMODCACHE=/tmp/betterfly-go-mod-dataforwarding \
go test -v ./integration
```

结果：

```text
--- PASS: TestFriendServiceEndToEnd (7.86s)
PASS
ok  	data_forwarding_service/integration	8.145s
```
