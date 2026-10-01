package data_forwarding

import (
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

func TestLegacyPostAndSyncRequestBytes(t *testing.T) {
	post := &Post{}
	// Legacy wire bytes: from_id=1, to_id=2, msg="x".
	if err := proto.Unmarshal([]byte{8, 1, 24, 2, 34, 1, 'x'}, post); err != nil {
		t.Fatal(err)
	}
	if post.GetFromId() != 1 || post.GetToId() != 2 || post.GetMsg() != "x" || post.GetMessageId() != 0 {
		t.Fatalf("legacy Post changed: %+v", post)
	}
	query := &QuerySyncMessages{}
	if err := proto.Unmarshal([]byte{8, 1}, query); err != nil {
		t.Fatal(err)
	}
	if query.GetToUserId() != 1 || query.GetIncludeRecalledChanges() || query.GetRecallCursorTimestamp() != "" {
		t.Fatalf("legacy sync enabled recalls: %+v", query)
	}
}

func TestLegacyClientCanReadPostWithServerMessageID(t *testing.T) {
	descriptor := protodesc.ToDescriptorProto((&Post{}).ProtoReflect().Descriptor())
	descriptor.Field = descriptor.Field[:8] // Original fields 1-8.
	file, err := protodesc.NewFile(&descriptorpb.FileDescriptorProto{
		Name: proto.String("legacy_post.proto"), Syntax: proto.String("proto3"), MessageType: []*descriptorpb.DescriptorProto{descriptor},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	legacy := dynamicpb.NewMessage(file.Messages().ByName("Post"))
	encoded, err := proto.Marshal(&Post{FromId: 1, ToId: 2, Msg: "hello", MessageId: 42})
	if err != nil {
		t.Fatal(err)
	}
	if err := proto.Unmarshal(encoded, legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.Get(legacy.Descriptor().Fields().ByName("msg")).String() != "hello" {
		t.Fatal("old client lost message body")
	}
}
