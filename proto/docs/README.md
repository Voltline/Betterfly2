# Protobuf 协议

`proto/` 保存 Betterfly2 的客户端和服务间协议：

| 目录 | 用途 |
| --- | --- |
| `data_forwarding/` | WebSocket 顶层请求、响应和业务 payload |
| `storage/` | 消息、用户资料、同步与文件控制面 |
| `friend/` | 好友、群组和群成员操作 |
| `call/` | WebRTC 通话信令与状态事件 |
| `push/` | 普通 APNs/VoIP token 注册和内部推送任务 |
| `envelope/` | Kafka 消息信封 |
| `server_rpc/` | Auth Service gRPC 接口及生成配置 |

## 生成代码

先安装 Go 插件：

```bash
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
```

然后从仓库根目录执行：

```bash
make -C proto
```

修改协议后必须同时提交 `.proto` 和生成结果，并运行受影响服务的测试。新增业务 payload 的服务端注册方式见 [接口新增指南](../../INTERFACE_DEVELOPMENT.md)。
