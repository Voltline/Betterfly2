package data_forwarding

import (
	"testing"

	channel "Betterfly2/proto/channel"
	storage "Betterfly2/proto/storage"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

func TestCaptionWireNumbersAndRoundTrip(t *testing.T) {
	caption := " # 公告\n**图片说明** 😀\n "
	for _, test := range []struct {
		message proto.Message
		number  protoreflect.FieldNumber
	}{
		{&Post{Caption: caption}, 10}, {&MessageRsp{Caption: caption}, 12},
		{&channel.ChannelPost{Caption: caption}, 10},
		{&storage.StoreNewMessage{Caption: caption}, 9}, {&storage.StoreMsgRsp{Caption: caption}, 12}, {&storage.MessageRsp{Caption: caption}, 12},
	} {
		field := test.message.ProtoReflect().Descriptor().Fields().ByName("caption")
		if field == nil || field.Number() != test.number {
			t.Fatalf("caption tag changed: %T", test.message)
		}
		encoded, err := proto.Marshal(test.message)
		if err != nil {
			t.Fatal(err)
		}
		decoded := test.message.ProtoReflect().New().Interface()
		if err := proto.Unmarshal(encoded, decoded); err != nil || !proto.Equal(test.message, decoded) {
			t.Fatalf("roundtrip %T: %v", test.message, err)
		}
	}
}

func TestOldImageWireDefaultsEmptyCaptionAndIgnoresNewField(t *testing.T) {
	// The old Post has image hash at tag 4 and type at tag 5, no tag 10.
	oldBytes := []byte{34, 4, 'h', 'a', 's', 'h', 42, 5, 'i', 'm', 'a', 'g', 'e'}
	post := &Post{}
	if err := proto.Unmarshal(oldBytes, post); err != nil || post.GetCaption() != "" || post.GetMsg() != "hash" || post.GetMsgType() != "image" {
		t.Fatalf("old image: %v", err)
	}
	encoded, err := proto.Marshal(post)
	if err != nil || string(encoded) != string(oldBytes) {
		t.Fatalf("empty caption added wire field: %v", err)
	}
	descriptor := protodesc.ToDescriptorProto(post.ProtoReflect().Descriptor())
	descriptor.Field = descriptor.Field[:9]
	file, err := protodesc.NewFile(&descriptorpb.FileDescriptorProto{Name: proto.String("legacy_image.proto"), Syntax: proto.String("proto3"), MessageType: []*descriptorpb.DescriptorProto{descriptor}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	legacy := dynamicpb.NewMessage(file.Messages().ByName("Post"))
	post.Caption = "announcement"
	encoded, err = proto.Marshal(post)
	if err != nil {
		t.Fatal(err)
	}
	if err = proto.Unmarshal(encoded, legacy); err != nil || legacy.Get(legacy.Descriptor().Fields().ByName("msg")).String() != "hash" {
		t.Fatalf("old reader: %v", err)
	}
	if err = proto.Unmarshal([]byte{82, 1, 0xff}, &Post{}); err == nil {
		t.Fatal("invalid utf8 caption accepted on wire")
	}
}
