# Betterfly2 回归测试

本文档记录当前可直接执行的回归测试入口，优先面向本地 `docker-compose` 联调环境。

## 消息 ID、撤回增量同步与投递重试

本轮回归测试分布在 `shared/db`、Storage 的 `internal/handler`、DataForwarding 的 `internal/handlers` 和 `internal/consumer`，以及 `proto/data_forwarding`：

- 原消息早于普通同步游标，但其撤回仍能独立分页补收；空普通页不会吞掉撤回页。
- 撤回墓碑遮蔽正文和文件名，沿用当前成员及入群时间权限；游标/身份错误在查询前拒绝，数据库错误不得返回部分成功。
- 自己发送的单聊也参与同步；复合分页、默认上限和空页游标保持不变。
- 入库响应、实时 Post 和 ACK 使用同一服务端 ID/时间；重复 client ID 返回原数据库内容和时间。
- ACK/cache/实时投递失败进入现有 Kafka 重试/DLQ 路径，DLQ 失败不跨过当前 offset；群聊部分失败不会伪装成功。
- 副作用短期占位崩溃恢复、旧 owner 的完成/删除 fencing，以及旧 Redis ACK 格式兼容。
- 旧 Post/同步请求字节兼容，以及数据库 v4 拒绝、v5/更高版本接受。

各模块执行 `go test ./...`；关键模块执行 `go test -race ./...`。这些测试使用 sqlmock、miniredis 和 Kafka mock，不等同于真实 Docker/APNs 端到端验证。客户端配合测试和部署顺序见 [适配说明](CLIENT_MESSAGE_SYNC_ADAPTATION.md)。

2026-09-30 验证结果：全部 15 个 Go module 的 `go test ./...`、`go vet ./...` 通过；shared、DataForwarding、Storage、Push、`proto/data_forwarding` 的 `go test -race ./...` 通过；`make -C proto` 与 `git diff --check` 通过。`TestFriendServiceEndToEnd` 因未设置 `BETTERFLY_E2E=1` 跳过，真实 Docker/APNs 和双设备客户端联调未执行。

## Friend/Group 端到端回归测试

当前好友与群聊主链路的端到端测试位于：

- [friend_service_e2e_test.go](services/dataForwardingService/integration/friend_service_e2e_test.go)

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

1. 已正确配置本地 `services/.env`，尤其是 `PGSQL_DSN`。该文件包含私密配置且被 Git 忽略，不应提交。
2. 已启动包含 Storage Service 和第二个 DataForwarding Pod 的本地环境：

```bash
cd services
./deploy_docker_compose.sh standard --enable redundancy
```

如果最近改过 `auth_service`、`df`、`df2`、`friend_service` 或共享代码，建议先重建：

```bash
./rebuild_docker_compose.sh auth df-all friend storage
```

### 运行命令

在 [services/dataForwardingService](services/dataForwardingService) 目录执行：

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

### 预期结果

```text
--- PASS: TestFriendServiceEndToEnd
PASS
ok  	data_forwarding_service/integration
```

具体耗时取决于镜像状态、Kafka 就绪速度和数据库网络，不在文档中固化某次运行结果。
