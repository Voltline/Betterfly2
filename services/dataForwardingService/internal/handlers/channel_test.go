package handlers

import (
	channel "Betterfly2/proto/channel"
	pb "Betterfly2/proto/data_forwarding"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"testing"
)

func TestChannelRoutesRejectUnauthenticatedRequests(t *testing.T) {
	for _, message := range []*pb.RequestMessage{
		{Payload: &pb.RequestMessage_ChannelRequest{ChannelRequest: &channel.ChannelRequest{Payload: &channel.ChannelRequest_Get{Get: &channel.GetChannel{ChannelId: 9}}}}},
		{Payload: &pb.RequestMessage_QueryChannelHistory{QueryChannelHistory: &channel.QueryChannelHistory{ChannelId: 9}}},
	} {
		if _, err := getDFRequestRouter().Dispatch(dfRequestContext{fromID: 2, message: message}, message.Payload); err == nil {
			t.Fatal("channel query accepted without JWT")
		}
	}
}

func TestChannelProtocolKeepsLegacyGroupAndPostWireFields(t *testing.T) {
	// An empty legacy LogoutReq retains current-connection semantics.
	legacy := &pb.RequestMessage{}
	raw, err := proto.Marshal(&pb.RequestMessage{Payload: &pb.RequestMessage_Logout{Logout: &pb.LogoutReq{}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := proto.Unmarshal(raw, legacy); err != nil || legacy.GetLogout() == nil || legacy.GetLogout().GetScope() != pb.LogoutScope_CURRENT_CONNECTION {
		t.Fatalf("legacy logout changed: %v", err)
	}
	for _, field := range []struct {
		name   string
		number int32
	}{{"channel_request", 39}, {"query_channel_history", 40}} {
		descriptor := (&pb.RequestMessage{}).ProtoReflect().Descriptor().Fields().ByNumber(protoreflect.FieldNumber(field.number))
		if descriptor == nil || string(descriptor.Name()) != field.name {
			t.Fatalf("channel field %s number changed", field.name)
		}
	}
	empty := &pb.JoinedGroupInfo{}
	if empty.GetIsChannel() {
		t.Fatal("old group default became channel")
	}
}
