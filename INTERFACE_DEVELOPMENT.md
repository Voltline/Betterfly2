# Betterfly2 接口新增指南

本文档说明 Betterfly2 服务端新增接口时的推荐流程。当前 `dataForwardingService`、`storageService` 与 `friendService` 已经改为模块自注册模式，新增接口时应尽量把注册逻辑放在接口所属模块内部，避免继续膨胀主 handler。

## 基本原则

- 先改 Protobuf，再改服务实现，最后补测试。
- 新接口的注册代码应放在业务模块文件内，不要直接修改主 handler 的分发逻辑。
- 主 handler 只负责解析请求、构建 router、统一分发和返回兜底错误。
- 如果新增的是一类业务能力，优先新建 `module_xxx.go`；如果只是补同类接口，放入已有模块文件。

## 通用流程

1. 更新协议定义。

   在对应 proto 文件中新增 request/response message，并把新的 payload 加入 `oneof`。

   常见位置：
   - `proto/data_forwarding/common.proto`
   - `proto/storage/storage.proto`
   - `proto/friend/friend.proto`

2. 重新生成 Protobuf 代码。

   ```bash
   cd proto
   make
   ```

3. 在服务模块中注册接口。

   新增或选择一个 `module_xxx.go`，在同一个 package 内通过 `init()` 注册模块。

4. 实现业务处理函数。

   优先把参数校验、请求转发、响应构造拆成小函数，便于单测覆盖。

5. 补充测试。

   至少覆盖正常路径、非法参数、空 payload 或未注册 payload 等关键分支。

## Data Forwarding Service

数据转发服务位于：

- `services/dataForwardingService/internal/handlers`

当前模块示例：

- `module_post.go`: 消息发送与转发
- `module_storage.go`: storage-service 查询与用户资料更新请求
- `module_friend.go`: friend-service 好友与群组请求
- `module_session.go`: 登录、注册、登出类 payload 兜底

新增 data forwarding 接口时，不需要修改 `messageHandler.go` 的 router 构建逻辑。推荐模式如下：

```go
package handlers

import (
	pb "Betterfly2/proto/data_forwarding"
	"Betterfly2/shared/dispatch"
)

func init() {
	registerDFRequestModule(registerExampleModule)
}

func registerExampleModule(router *dispatch.OneofRouter[dfRequestContext, dfRequestResult]) {
	dispatch.Register(router, func(ctx dfRequestContext, payload *pb.RequestMessage_Example) (dfRequestResult, error) {
		return dfRequestResult{}, handleExample(ctx.fromID, ctx.message)
	})
}

func handleExample(fromID int64, message *pb.RequestMessage) error {
	payload, err := authenticatedPayload(fromID, message, "示例接口", "example", (*pb.RequestMessage).GetExample)
	if err != nil {
		return err
	}

	_ = payload
	return nil
}
```

如果接口需要转发到内部服务，优先使用现有桥接函数：

- `newStorageRequest`
- `publishStorageRequest`
- `newFriendRequest`
- `publishFriendRequest`

## Storage Service

存储服务内部 MQ 接口位于：

- `services/storageService/internal/handler`

当前模块示例：

- `module_messages.go`: 消息存储与同步查询
- `module_users.go`: 用户资料查询与更新
- `module_files.go`: 文件存在性查询

新增 storage MQ 接口时，推荐模式如下：

```go
package handler

import (
	"Betterfly2/proto/storage"
	"Betterfly2/shared/dispatch"
)

func init() {
	registerStorageRequestModule(registerStorageExampleModule)
}

func registerStorageExampleModule(router *dispatch.OneofRouter[storageRequestContext, *storage.ResponseMessage]) {
	dispatch.Register(router, func(ctx storageRequestContext, payload *storage.RequestMessage_Example) (*storage.ResponseMessage, error) {
		return ctx.handler.handleExample(ctx.request, payload.Example)
	})
}
```

如果新增的是 HTTP 文件接口，入口通常在：

- `services/storageService/internal/http_server`

HTTP 接口不走 MQ router，但仍建议按 handler 文件拆分，并补充 `internal/http_server` 下的测试。

## Friend Service

好友服务内部 MQ 接口位于：

- `services/friendService/internal/handler`

当前模块示例：

- `module_contacts.go`: 好友关系接口
- `module_groups.go`: 群组接口

新增 friend MQ 接口时，推荐模式如下：

```go
package handler

import (
	friend "Betterfly2/proto/friend"
	"Betterfly2/shared/dispatch"
)

func init() {
	registerFriendRequestModule(registerFriendExampleModule)
}

func registerFriendExampleModule(router *dispatch.OneofRouter[friendRequestContext, *friend.ResponseMessage]) {
	dispatch.Register(router, func(ctx friendRequestContext, payload *friend.RequestMessage_Example) (*friend.ResponseMessage, error) {
		return ctx.handler.handleExample(ctx.request, payload.Example)
	})
}
```

## 测试建议

改完接口后，优先运行对应服务测试：

```bash
cd services/dataForwardingService
go test ./internal/handlers ./internal/consumer ./internal/redis ./internal/router
```

```bash
cd services/storageService
go test ./internal/handler ./internal/http_server
```

```bash
cd services/friendService
go test ./internal/handler
```

如果改动了 `shared/dispatch` 或 `shared/mq`：

```bash
cd shared
go test ./dispatch ./mq
```

涉及好友、群组、消息跨 pod 流程时，还应参考 `REGRESSION_TESTING.md` 运行端到端回归测试。

## 注意事项

- `init()` 注册只负责把模块注册函数加入列表，不要在其中连接数据库、访问 Redis 或做网络 I/O。
- 同一个 payload 类型只能注册一次，否则 `dispatch.Register` 会按共享分发器的规则报错或覆盖风险。
- 新增 payload 后，未更新 Protobuf 生成代码会导致 Go 编译失败。
- 如果新增接口需要客户端收到异步响应，确认 `TargetUserId` 与 `FromKafkaTopic` 能把响应路由回发起请求的 dataForwarding pod。
- 如果新增接口要穿过 Kafka，请确认 topic 命名与现有约定一致，例如 `storage-service`、`friend-service`。
