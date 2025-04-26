import base64
import idl.request_pb2
import idl.common_pb2

def create_request_with_post(from_id, to_id, message):
    # 构建 Post 消息
    post = idl.common_pb2.Post()
    post.from_id = from_id
    post.to_id = to_id
    post.msg = message
    post.is_group = False
    post.msg_type = "text"
    post.timestamp = "2024-04-26T00:00:00Z"
    post.real_file_name = ""

    # 封装进 RequestMessage
    request = idl.request_pb2.RequestMessage()
    request.post.CopyFrom(post)

    # 序列化
    binary_data = request.SerializeToString()
    return binary_data

if __name__ == "__main__":
    from_id = 10000
    to_id = 10001
    message = "你好，这是一条封装在 RequestMessage 中的 Post 消息"

    binary_data = create_request_with_post(from_id, to_id, message)

    # 转成 base64 并写入文件
    base64_data = base64.b64encode(binary_data).decode("utf-8")
    with open("request_message_base64.txt", "w") as f:
        f.write(base64_data)

    print(f"封装的 RequestMessage 已保存，大小 {len(binary_data)} 字节")