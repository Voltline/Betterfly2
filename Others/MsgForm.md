# RequestMsg报文格式
> RequestType的定义
```cpp
enum RequestType
{
    Login, Exit, Post, Key, QueryUser, InsertContact, QueryGroup, InsertGroup, InsertGroupUser, File
};
```

## RequestType.Login
> 登录请求，暂时没有认证
```json
{
    "type": RequestType.Login,
    "from": iid,
    "name": name,
    "timestamp": latestTimestamp
}
```

## RequestType.Exit
> 退出登录请求，可以被第三方发包强行下线(之后再改)
```json
{
    "type": RequestType.Exit,
    "from": iid
}
```

## RequestType.Post
> 向特定用户/群组发送消息
```json
{
    "type": RequestType.Post,
    "from": from_user_id (int),
    "name": from_user_name (String),
    "is_group": is_group (Bool),
    "to"  : to_id (int),
    "msg" : "msg" (String),
    "msg_type": msg_type in ("text", "image", "gif", "file") (String),
    "timestamp": Date("yyyy-MM-dd hh:mm:ss") (Date in Swift, Datetime in SQLite)
}
```

## RequestType.QueryUser
> 查询特定用户信息
```json
{
    "type": RequestType.QueryUser,
    "from": from_user_id,
    "to":   to_query_user_id
}
```

## RequestType.InsertContact
> 添加联系人
```json
{
    "type": RequestType.InsertContact,
    "from": from_user_id,
    "to":   to_insert_user_id
}
```

## RequestType.QueryGroup
> 通过id请求群组信息
> 
> 查询没有添加的群聊信息，获取已添加群聊的信息
```json
{
    "type": RequestType.QueryGroup,
    "from": from_user_id,
    "to":   group_id,
    "msg":  如果不存在该字段或为空字符串，则说明该查询的结果需要保存到用户本地数据库
            否则说明该查询的结果不需要保存到用户本地数据库
}
```

## RequestType.InsertGroup
> 添加群组
```json
{
    "type": RequestType.InsertGroup,
    "from": from_user_id,
    "to":   group_id,
    "msg":  group_name,
}
```

## RequestType.InsertGroupUser
> 向群组中添加用户
```json
{
    "type": RequestType.InsertGroupUser,
    "from": from_user_id,
    "to":   group_id,
}
```

## RequestType.File
> 文件上传/下载请求
```json
{
    "type": RequestType.File,
    "from": from_user_id,
    "file_hash": SHA512(file),
    "file_suffix": file_suffix,
    "operation": "upload"/"download"
}
```

## RequestType.UpdateAvatar
> 上传用户头像或群头像
```json
{
    "type": RequestType.UpdateAvatar,
    "from": id,
    "msg": 头像的SHA512值,
    "is_group": is_group (Bool),
}
```

# ResponseMsg报文格式
> ResponseType的定义，其中File，Pubkey暂不使用
```cpp
enum ResponseType
{
    Refused, Server, Post, File, Warn, PubKey, UserInfo, GroupInfo
};
```
## ResponseType.Refused
> 服务器拒绝提醒
```json
{
    "type": ResponseType.Refused
}
```

## ResponseType.Server
> 服务器消息，如登录成功提醒等
```json
{
    "type": ResponseType.Server,
    "msg": "msg"
}
```

## ResponseType.Post
> 收到其他客户端发来的消息
```json
{
    "type": ResponseType.Post,
    "from": from_user_id (int),
    "name": from_user_name (String),
    "is_group": is_group (Bool),
    "to"  : to_id (int),
    "msg" : "msg" (String),
    "msg_type": msg_type in ("text", "image", "gif", "file") (String),
    "timestamp": Date("yyyy-MM-dd hh:mm:ss") (Date in Swift, Datetime in SQLite)
}
```

## ResponseType.File
> 文件下载/上传链接/已存在通知反馈
```json
{
    "type": ResponseType.File,
    "msg": file_name(file_hash.file_suffix),
    "content": upload_url/download_url/"Existed"/"Not exist",
    "file_op": "upload"/"download"
}
```

## ResponseType.Warn
> 服务器警告
```json
{
    "type": ResponseType.Warn,
    "msg": "msg"
}
```

## ResponseType.UserInfo
> 好友查询信息
```json
{
    "type": ResponseType.UserInfo,
    "msg": query_user_name,
    "to": from_user_id
}
```

## ResponseType.GroupInfo
> 群组查询信息
```json
{
    "type": ResponseType.GroupInfo,
    "from": 0:该查询需要保存; -1：该查询不需要保存,
    "to":   group_id,
    "msg":  query_group_name
}
```

## ResponseType.File
> 文件下载/上传链接/已存在通知反馈
```json
{
    "type": ResponseType.File,
    "msg": file_name(file_hash.file_suffix),
    "content": upload_url/download_url/"Existed"/"Not exist",
    "file_op": "upload"/"download"
}
```

## ResponseType.Warn
> 服务器警告
```json
{
    "type": ResponseType.Warn,
    "msg": "msg"
}
```

## ResponseType.UserInfo
> 好友查询信息
```json
{
    "type": ResponseType.UserInfo,
    "msg": query_user_name,
    "to": from_user_id
}
```

## ResponseType.GroupInfo
> 群组查询信息
```json
{
    "type": ResponseType.GroupInfo,
    "from": 0:该查询需要保存; -1：该查询不需要保存,
    "to":   group_id,
    "msg":  query_group_name
}
```
