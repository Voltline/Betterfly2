package apns

import (
	"encoding/json"
	pushservice "pushService/internal/push"
	"testing"
	"time"
)

func TestChannelPayloadCarriesIdentityAndOptionalChannelMarker(t *testing.T) {
	for _, isChannel := range []bool{false, true} {
		n := pushservice.Notification{IsGroup: true, IsChannel: isChannel, ConversationID: 9, MessageID: 42, Title: "频道", Body: "公告", Avatar: "channel-avatar", AvatarIsGroup: true, ConversationName: "频道", ConversationAvatar: "channel-avatar", SentAt: time.Now()}
		data, err := marshalMessagePayload(n)
		if err != nil {
			t.Fatal(err)
		}
		var payload map[string]any
		if err := json.Unmarshal(data, &payload); err != nil {
			t.Fatal(err)
		}
		if (payload["is_channel"] == true) != isChannel || payload["conversation_name"] != "频道" || payload["conversation_avatar"] != "channel-avatar" || payload["avatar_is_group"] != true {
			t.Fatal("channel identity not preserved")
		}
		if !isChannel {
			if _, present := payload["is_channel"]; present {
				t.Fatal("ordinary group payload unnecessarily changed")
			}
		}
		recall, err := marshalMessageRecallPayload(n)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(recall, &payload); err != nil {
			t.Fatal(err)
		}
		if (payload["is_channel"] == true) != isChannel {
			t.Fatal("channel recall loses conversation kind")
		}
	}
}

func TestImageCaptionAPNsPayloadContainsOnlySummary(t *testing.T) {
	n := pushservice.Notification{IsGroup: true, IsChannel: true, ConversationID: 9, MessageID: 41, MessageType: "image", Title: "公告频道", Body: "[图片] 公告内容", Avatar: "channel-avatar", AvatarIsGroup: true, SentAt: time.Now()}
	raw, err := marshalMessagePayload(n)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	alert := payload["aps"].(map[string]any)["alert"].(map[string]any)
	if alert["title"] != "公告频道" || alert["body"] != "[图片] 公告内容" || payload["is_channel"] != true || payload["avatar"] != "channel-avatar" {
		t.Fatal("APNs image summary/identity changed")
	}
	for _, field := range []string{"caption", "image", "image_data", "file_hash"} {
		if _, present := payload[field]; present {
			t.Fatal("unexpected image attachment/full caption protocol")
		}
	}
}
