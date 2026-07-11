# APNs 紧急通知调研

## 结论

APNs 的 `apns-priority: 10` 只要求 APNs 立即尝试投递，不代表通知可以穿透系统专注模式。iOS 15 之后，真正影响展示时机的是 payload 中的 `aps.interruption-level`。

Betterfly2 后续应优先支持 `time-sensitive`，暂不开放 `critical`：

- `time-sensitive` 可以绕过通知摘要和部分 Focus 控制，但用户可以在系统设置中关闭。客户端必须启用 Time Sensitive Notifications capability。
- `critical` 可以绕过静音和勿扰模式，需要 Apple 审核并授予 Critical Alerts entitlement，同时还需要客户端单独申请用户授权。
- 普通聊天消息不得默认标记为紧急。紧急发送必须来自明确的用户动作或受控的服务端业务规则，并进入审计日志。

Apple 官方说明：

- [Time Sensitive interruption level](https://developer.apple.com/documentation/usernotifications/unnotificationinterruptionlevel/timesensitive)
- [Critical interruption level](https://developer.apple.com/documentation/usernotifications/unnotificationinterruptionlevel/critical)
- [Critical Alert authorization](https://developer.apple.com/documentation/usernotifications/unauthorizationoptions/criticalalert)
- [Remote notification payload keys](https://developer.apple.com/documentation/usernotifications/generating-a-remote-notification)

## 推荐协议

后续为内部消息推送协议增加显式枚举，而不是允许客户端直接填写 APNs 字符串：

```text
NORMAL
TIME_SENSITIVE
CRITICAL
```

服务端策略：

- `NORMAL` 为默认值，对应 `interruption-level: active` 或省略该字段。
- `TIME_SENSITIVE` 只允许指定消息类型或明确的“紧急发送”操作，并记录发送者、接收者和原因。
- `CRITICAL` 在取得 Apple entitlement 前始终拒绝；取得后仍需要服务端白名单和速率限制。
- 群聊紧急通知需要限制接收人数和发送频率，防止一次操作穿透大量用户的 Focus。

## 客户端准备

富消息头像和紧急通知都需要客户端配合：

1. 增加 Notification Service Extension，并与主 App 共享必要的认证或缓存数据。
2. 在扩展中读取 `sender_name`、`group_name` 和 `avatar`，将头像标识解析为本地图片或经过授权的下载资源。
3. 使用 `INSendMessageIntent` 更新 `UNMutableNotificationContent`，获得 Communication Notifications 的头像与群聊展示样式。
4. 主 App 启用 Communication Notifications、SiriKit 和 Time Sensitive Notifications capability，并在 `NSUserActivityTypes` 中声明 `INSendMessageIntent`。
5. 发送 Time Sensitive 前检查 `UNNotificationSettings.timeSensitiveSetting`，在权限关闭时退化为普通通知。

Apple 的 Communication Notifications 示例明确要求物理设备、Notification Service Extension、Communication Notifications、Time Sensitive Notifications 和 SiriKit capability：[Handling Communication Notifications and Focus Status Updates](https://developer.apple.com/documentation/usernotifications/handling-communication-notifications-and-focus-status-updates)。

## 建议实施顺序

1. 客户端先完成 Notification Service Extension 和头像展示。
2. 服务端增加 `TIME_SENSITIVE` 枚举、权限策略、频率限制和审计字段。
3. 在内网 Push Console 中加入 Time Sensitive 测试，但 production 环境要求二次确认。
4. 仅在 Apple 批准 Critical Alerts entitlement 后评估 `CRITICAL`，不提前下发该 interruption level。
