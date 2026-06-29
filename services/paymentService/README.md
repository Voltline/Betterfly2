# Payment Service

PaymentService 是 Betterfly2 的统一支付服务。一期目标不是直接接入真实资金渠道，而是先把支付服务必须具备的工程骨架补齐：订单状态机、客户端幂等、渠道回调验签、支付事件记录、用户订单查询和 Docker Compose 接入。

当前 provider 为 `mock`，后续可以在 `payment.Provider` 接口后接入微信、支付宝或 Stripe。

## 架构

```text
Client
  -> paymentService HTTP API
  -> authService JWT validation
  -> payment_orders / payment_events
  -> provider adapter
  -> provider callback
  -> paymentService state machine
```

核心原则:

- 客户端只能创建和查询自己的订单。
- 订单号由服务端生成，客户端必须通过 `Idempotency-Key` 避免重复创建。
- 渠道回调不能相信客户端状态，必须验签、校验金额、记录事件并经过状态机。
- 真实支付渠道只应该实现 provider adapter，不应污染订单核心逻辑。

## 环境变量

```bash
PORT=8084
PGSQL_DSN=...
AUTH_RPC_ADDR=auth_service:50051
PAYMENT_ADMIN_TOKEN=
PAYMENT_PUBLIC_BASE_URL=http://localhost:8084
PAYMENT_MOCK_CALLBACK_SECRET=dev-payment-secret
```

如果 `PAYMENT_ADMIN_TOKEN` 为空，管理和 mock 支付接口不会强制管理 token，方便本地开发。生产环境必须配置。

## API

### 创建订单

```bash
curl -X POST http://localhost:8084/payment/v1/orders \
  -H "Authorization: Bearer $JWT" \
  -H "X-User-ID: 1" \
  -H "Idempotency-Key: req-001" \
  -H "Content-Type: application/json" \
  -d '{
    "subject": "Betterfly Premium",
    "amount_cents": 1200,
    "currency": "CNY",
    "provider": "mock",
    "client_payload": {"sku": "premium_monthly"}
  }'
```

### 查询订单

```bash
curl http://localhost:8084/payment/v1/orders/{order_no} \
  -H "Authorization: Bearer $JWT" \
  -H "X-User-ID: 1"
```

### Mock 支付成功

```bash
curl -X POST http://localhost:8084/payment/v1/mock/pay/{order_no} \
  -H "X-Admin-Token: $PAYMENT_ADMIN_TOKEN"
```

### 查询管理端订单

```bash
curl "http://localhost:8084/payment/admin/api/orders?limit=20" \
  -H "X-Admin-Token: $PAYMENT_ADMIN_TOKEN"
```

## 状态机

当前订单状态:

- `created`: 本地订单已创建，尚未完成 provider 下单。
- `pending`: provider 下单完成，等待支付回调。
- `paid`: provider 回调确认支付成功。
- `failed`: provider 回调确认失败。
- `closed`: provider 回调确认关闭。

回调进入 `paid` 后不会被 `failed/closed` 覆盖，避免迟到失败事件污染已支付订单。

## 后续接真实渠道

- 新增 provider adapter，实现 `Name`、`CreateOrder` 和 `VerifyCallback`。
- 为该渠道单独增加 callback route，例如 `/payment/v1/providers/alipay/callback`。
- 使用渠道官方验签和证书，不复用 mock HMAC。
- 增加退款表和退款状态机，不要直接在订单状态上硬塞退款语义。
- 增加业务兑换/发货事件，支付成功只代表资金确认，不代表业务权益已经发放。
