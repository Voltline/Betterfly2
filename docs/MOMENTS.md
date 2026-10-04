# 好友动态 / MomentsService

MomentsService 是独立 HTTP 服务，网页与原生客户端使用同一 JSON API，不经过聊天
Post、Kafka、APNs 或 DataForwarding。支持文字、最多九张图片、好友时间流、个人动态、
点赞及取消、评论及一层展示回复、删除。所有有权看动态的人都能看到全部点赞和评论，
不限共同好友。暂不支持 public、视频、编辑、转发、推荐流或动态推送。

## 部署

入口 `http://localhost:8087/moments/`，API `/moments/api/v1`，探针 `/health`、`/ready`，
指标 `/metrics`（遵循已有 `METRICS_ENABLED`）。正式网页使用仓库 Betterfly Logo。

`standard` / `full` 预设默认启用动态；`minimal` 不启用，可单独追加 `--enable moments`。
该 profile 会启动 RustFS，但不要求 StorageService 随之启动。

在 `services/.env` 配置实际浏览器入口及客户端能访问的 S3 地址：

```dotenv
MOMENTS_PUBLIC_URL=https://chat.example.com
MOMENTS_BUCKET=betterfly-moments
RUSTFS_EXTERNAL_ENDPOINT_URL=https://objects.example.com
MOMENTS_DB_MAX_OPEN_CONNS=6
MOMENTS_DB_MAX_IDLE_CONNS=2
```

`MOMENTS_PUBLIC_URL` 只能是 scheme + host + 可选 port，不带路径；用于精确 Origin 校验、
Cookie Secure 标志及 S3 CORS。默认 `http://localhost:8087` 只适用于本机测试。
HTTPS 反向代理完整保留 `/moments/` 路径，透传 Cookie、Origin，不记录请求正文或认证头。
浏览器图片 SHA-512 使用 Web Crypto，需要 HTTPS 或 localhost。

首次部署需要迁移；增量重建默认 `--no-deps`，不会自动重跑已完成的 migration 容器：

```bash
cd services
docker compose build db_migrate moments_service
docker compose run --rm --no-deps db_migrate
./deploy_docker_compose.sh standard --no-build

# 已完成 v10 后，仅重建动态服务
./rebuild_docker_compose.sh --no-sudo moments
```

追加迁移 v10 只创建 `moment_posts`、`moment_assets`、`moment_comments`、`moment_likes`
及索引，不重写聊天记录，v1-v9 未改变。每个版本仍在事务及原有迁移锁下执行，可重跑、
失败重试。所有采用新版 shared 的业务 Pod 都要求至少 v10，因此先迁移，再滚动服务。
回滚旧镜像不必删除新表；服务进程不执行 DDL。

生产应为服务建立专用数据库用户与 S3 凭据。初始化私有 bucket 时需要 Head/Create、
GetBucketPolicy/GetBucketAcl/PutBucketCors；服务启动拒绝非空 bucket policy 和任何
group ACL，检查失败不会当作“私有”。默认 Compose 复用现有 RustFS 凭据只是测试便利。
不允许使用通用 `betterfly-files` bucket 作为动态附件 bucket。

数据库预算：每副本最多 6 个连接，2 个空闲；增加 N 个动态副本需在原预算上加 `6 × N`，
并预留迁移、管理和其它业务连接。图片解码最多两路，单张上限 40 百万像素，生产内存
建议至少 512 MiB，Kubernetes 示例限额 1 GiB。

### Kubernetes

发布对应固定 tag/digest 的 migration 镜像及 Moments 镜像，创建
`betterfly2-moments` ConfigMap（`MOMENTS_PUBLIC_URL`、`RUSTFS_EXTERNAL_ENDPOINT_URL`）。
先执行 `deploy/k8s/migrations/schema-v10-job.yaml` 并等待 Job 成功，再滚动业务 Pod，
按需 apply `deploy/k8s/moments.yaml`。后者不加入默认 base，需给同域 Ingress 增加
`/moments` → `moments-service:8087` 路由。Job 更换 schema 版本时使用新名称。

## 认证与可见性

原生 HTTP 请求：`Authorization: Bearer <JWT>`、`X-User-ID: <本人十进制 ID>`。
用户 ID 不是可信声明，每次都由现有 Auth.CheckJwt 验证绑定；失效返回 401，Auth
故障返回 503。网页登录调用 Auth.Login，Cookie 为 HttpOnly、SameSite=Strict，HTTPS
入口开启 Secure。JWT 不进入 URL、localStorage 或网页源码。

Cookie 写请求必须带同源 Origin 和 `X-CSRF-Token`，token 由 session / me 响应提供，
网页只存内存。Bearer 与不同的 Cookie JWT 冲突时拒绝，不隐式切换账号。
登录入口有每实例、每 IP 每分钟 10 次的有界限流；多副本或开放公网还需入口层限流，
不相信客户端 X-Forwarded-For。

`friends`：本人或当前双向有效好友；`private`：仅本人。新增好友可看先前的 friends
动态；删除任一方向关系后，后续详情、列表、互动及图片签名重新检查权限。
未授权与不存在统一 404。作者可删动态；评论作者或动态作者可删评论，评论作者即使
失去动态读取权限也能删自己的评论，但不会因此得到动态正文。

## JSON API

下面的路径相对 `/moments/api/v1`。ID 一律十进制字符串；时间为 RFC3339 UTC，网页
显示 UTC+8。分页默认 20，上限 50，`cursor` 不透明，必须沿用服务端返回值。

| 方法 | 路径 | 请求 / 返回 |
| --- | --- | --- |
| POST | /session | `{account,password}` 网页登录；或 Bearer + X-User-ID + `{}` 嵌入登录，两种模式互斥；都要求准确 Origin；返回 user_id、csrf_token |
| DELETE | /session | 同源 CSRF 校验后清除网页 Cookie，即使 JWT 已过期；不撤销原生会话 |
| GET | /me | `{user:{id,name,avatar},csrf_token}` |
| GET | /feed | cursor、limit；自己及好友的可见动态 |
| GET | /users/{id}/posts | cursor、limit；按当前查看者权限过滤 |
| GET | /users/{id}/avatar | 现有 verified 头像标识解析为短期 URL；无有效头像 404 |
| POST | /posts | `{client_post_id,content,visibility,asset_ids}`；返回 canonical Post |
| GET / DELETE | /posts/{id} | 完整 Post / 幂等删除结果 |
| PUT / DELETE | /posts/{id}/like | 添加 / 取消自己的赞；返回 liked_by_me |
| GET | /posts/{id}/likes | 分页全部点赞者，items 元素为 `{author,created_at}` |
| GET | /posts/{id}/comments | 分页平铺评论，已删除评论为 tombstone |
| POST | /posts/{id}/comments | `{client_comment_id,content,reply_to_comment_id?}`；返回 canonical Comment |
| DELETE | /comments/{id} | 作者或动态作者删除，返回 `{success:true}` |
| POST | /assets | `{client_asset_id,hash,size,mime}`；返回 asset、status、upload_url、headers、expires_at |
| POST | /assets/{id}/verify | 仅上传者；成功返回 asset、status=verified |
| GET | /assets/{id}/download | 当前权限校验后返回 `{url,expires_at}` |

文本保留换行和 Markdown 原文，仅作为纯文本显示，不接收 HTML。正文最多 4096 Unicode
code points / 16 KiB，评论最多 1000 / 4 KiB；不截断、拒绝无效 UTF-8。纯空白且无图
拒绝。客户端幂等 ID 为 1-128 字符，只允许 ASCII 字母、数字、`._-`，推荐 UUID。

Post 包含 author、content、visibility、created_at、assets、like_count、comment_count、
liked_by_me、can_delete，以及最多六个 likes_preview、两个 comments_preview。
Comment 包含 id、author、content、created_at、可选 reply_to_comment_id、deleted、
can_delete。回复保持平铺，引用目标必须属于同一动态，不能回复已删除评论。
可见者能查看他人的完整互动，并不因此获取互动者的好友动态权限。

列表格式 `{items:[],next_cursor:"",has_more:false}`。动态按时间/ID 倒序，评论和赞
按时间/ID 正序；时间相同仍稳定分页。游标绑定当前用户和列表上下文，不替代鉴权。
一个动态页用固定七次批量查询，包括预览，不逐条读取作者/附件/计数。

## 图片与重试

JPEG/PNG，最多九张，每张 10 MiB，宽高不超过 8192 且最多 40 百万像素；HEIC 需客户端
转换/缩放。申请上传得到最多 10 分钟 PUT URL，PUT 使用返回 headers（Content-Type），
然后 verify。服务限制读取字节数，校验 SHA-512、真实类型、尺寸并实际解码；将同一份
验证后的字节写入只有服务端能写的 sealed key，再提交 verified 元数据。
旧 PUT URL 即使再次写 staging，也不能替换已封存图片。

发布仅绑定本人、verified、未绑定的 asset，绑定顺序即展示顺序；不是聊天文件 hash，
不写旧通用 file_metadata。下载鉴权后签发 60 秒 URL，所有 API 响应禁止共享缓存。
已签发 URL 到期前不能即时撤销，已下载文件也不能收回。

同一作者 + client_post_id，相同正文/可见性/附件顺序只产生一条动态，同一 ID / 时间。
同键不同请求返回 409，不覆盖；删除后同键重试 410，不能复活。评论同理；赞使用复合
主键。正文发布和图片绑定在同一事务，写互动与删除按同一帖子行串行判定。
HTTP 响应丢失后保留原请求及幂等 ID 重试，不把本地超时视为业务未提交。

服务故障返回 503，参数 400，认证 401，不可见/不存在 404，冲突 409，作者重试已删
对象 410，请求体超过 64 KiB 返回 413，登录频率 429。错误格式为
`{"error":{"code":"UNAVAILABLE","message":"..."}}`，不返回数据库细节。
元数据保存失败不会误报校验成功，sealed 对象保留供安全重试。

## 原生接入示例

无需改 Protobuf。直接使用原生 Bearer 模式，或对受控同域 POST session 建立 WebView
Cookie 后再进入 `/moments/`，不要把 JWT 拼进 URL。网页和原生互通相同动态、评论与赞。

```http
POST /moments/api/v1/posts
Authorization: Bearer <JWT>
X-User-ID: 1
Content-Type: application/json

{"client_post_id":"e63d590e-6d37-4d1f-8adf-3401eb9cc8b8","content":"今天的记录\n保留原文。","visibility":"friends","asset_ids":[]}
```

```json
{"id":"1201","author":{"id":"1","name":"Voltline","avatar":""},"content":"今天的记录\n保留原文。","visibility":"friends","created_at":"2026-10-04T08:00:00Z","assets":[],"like_count":0,"comment_count":0,"liked_by_me":false,"can_delete":true,"likes_preview":[],"comments_preview":[]}
```

有图时将已 verify 的 asset IDs 依序附上；附件返回 `{id,width,height}`，渲染时单独调用
download，不把短期 URL 当永久地址存储。原生可以继续用现有头像解析方式，网页提供
上述独立 avatar 签名入口。不发送图片二进制给动态发布接口。

## 验证与当前边界

测试命令及独立环境设置见 [回归测试文档](REGRESSION_TESTING.md)。数据库迁移、并发
幂等、好友撤销、分页、私有图片签名及 staging 覆写隔离均有真实 PG/RustFS 测试。
HTTP 集成测试用测试 Auth adapter，现有 Auth gRPC 另有单测，不等于真实 iOS 跨服务验收。

`betterfly_moments_assets{status}`、`betterfly_moments_asset_bytes{status}` 提供数据库附件
状态/声明体积，抓取失败有单独指标；不等于 bucket 实际磁盘占用，RustFS 容量仍由其
本身监控。未发布、已删除及测试遗留对象不会自动回收，首期不增加垃圾回收后台任务。
只面向可信账号好友圈；开放陌生人使用前另行确定配额、内容审核和举报治理。
