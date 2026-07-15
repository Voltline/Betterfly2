package consumer

import (
	"context"

	envelope "Betterfly2/proto/envelope"
	friendpb "Betterfly2/proto/friend"
	"Betterfly2/shared/kafkaconsumer"
	"friendService/internal/handler"
	"friendService/internal/publisher"

	"github.com/IBM/sarama"
	"google.golang.org/protobuf/proto"
)

type messageHandler interface {
	HandleMessage(context.Context, []byte) error
}

type KafkaConsumerGroupHandler struct {
	handler  messageHandler
	reliable *kafkaconsumer.Handler
}

func NewKafkaConsumerGroupHandler(friendHandler messageHandler) *KafkaConsumerGroupHandler {
	return &KafkaConsumerGroupHandler{handler: friendHandler}
}

func (h *KafkaConsumerGroupHandler) Setup(session sarama.ConsumerGroupSession) error {
	h.initialize()
	return h.reliable.Setup(session)
}

func (h *KafkaConsumerGroupHandler) Cleanup(session sarama.ConsumerGroupSession) error {
	h.initialize()
	return h.reliable.Cleanup(session)
}

func (h *KafkaConsumerGroupHandler) ConsumeClaim(session sarama.ConsumerGroupSession, claim sarama.ConsumerGroupClaim) error {
	h.initialize()
	return h.reliable.ConsumeClaim(session, claim)
}

func (h *KafkaConsumerGroupHandler) initialize() {
	if h.handler == nil {
		h.handler = handler.NewFriendHandler()
	}
	if h.reliable != nil {
		return
	}
	h.reliable = kafkaconsumer.New(
		kafkaconsumer.LoadConfig("friend", "FRIEND", "friend-service-dlq"),
		h.process,
		func(ctx context.Context, topic string, payload []byte, headers []sarama.RecordHeader) error {
			return publisher.PublishRawMessageContext(ctx, payload, topic, headers)
		},
	)
}

func (h *KafkaConsumerGroupHandler) process(ctx context.Context, message *sarama.ConsumerMessage) kafkaconsumer.Result {
	env := &envelope.Envelope{}
	if err := proto.Unmarshal(message.Value, env); err != nil {
		return kafkaconsumer.Permanentf("decode friend envelope: %v", err)
	}
	if env.GetType() != envelope.MessageType_FRIEND_REQUEST {
		return kafkaconsumer.Permanentf("unexpected friend envelope type: %s", env.GetType())
	}
	request := &friendpb.RequestMessage{}
	if len(env.GetPayload()) == 0 {
		return kafkaconsumer.Permanentf("empty friend request payload")
	}
	if err := proto.Unmarshal(env.GetPayload(), request); err != nil {
		return kafkaconsumer.Permanentf("decode friend request: %v", err)
	}
	if request.GetPayload() == nil || request.GetFromKafkaTopic() == "" {
		return kafkaconsumer.Permanentf("incomplete friend request")
	}
	if err := h.handler.HandleMessage(ctx, env.GetPayload()); err != nil {
		return kafkaconsumer.ResultForError(err)
	}
	return kafkaconsumer.Success()
}
