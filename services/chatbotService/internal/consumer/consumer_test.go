package consumer

import (
	"context"
	"errors"
	"testing"

	chatbotpb "Betterfly2/proto/chatbot"
	envelope "Betterfly2/proto/envelope"
	"Betterfly2/shared/kafkaconsumer"
	"Betterfly2/shared/mq"
	"chatbotService/internal/platform"

	"github.com/IBM/sarama"
)

type stubService struct {
	err     error
	trigger *chatbotpb.ChatbotTrigger
	event   *chatbotpb.BotEvent
}

func (s *stubService) HandleTrigger(_ context.Context, trigger *chatbotpb.ChatbotTrigger) error {
	s.trigger = trigger
	return s.err
}

func (s *stubService) HandleEvent(_ context.Context, event *chatbotpb.BotEvent) error {
	s.event = event
	return s.err
}

func encode(t *testing.T, messageType envelope.MessageType, request *chatbotpb.RequestMessage) *sarama.ConsumerMessage {
	t.Helper()
	payload, err := mq.MarshalEnvelope(messageType, request)
	if err != nil {
		t.Fatal(err)
	}
	return &sarama.ConsumerMessage{Value: payload}
}

func TestProcessClassifiesResults(t *testing.T) {
	trigger := &chatbotpb.RequestMessage{Payload: &chatbotpb.RequestMessage_Trigger{Trigger: &chatbotpb.ChatbotTrigger{MessageId: 7}}}
	cases := map[string]struct {
		message *sarama.ConsumerMessage
		err     error
		want    kafkaconsumer.Outcome
	}{
		"success":      {encode(t, envelope.MessageType_CHATBOT_REQUEST, trigger), nil, kafkaconsumer.OutcomeSuccess},
		"transient":    {encode(t, envelope.MessageType_CHATBOT_REQUEST, trigger), errors.New("db down"), kafkaconsumer.OutcomeTransient},
		"invalid":      {encode(t, envelope.MessageType_CHATBOT_REQUEST, trigger), platform.ErrInvalidTrigger, kafkaconsumer.OutcomePermanent},
		"wrong type":   {encode(t, envelope.MessageType_PUSH_REQUEST, trigger), nil, kafkaconsumer.OutcomePermanent},
		"empty":        {encode(t, envelope.MessageType_CHATBOT_REQUEST, &chatbotpb.RequestMessage{}), nil, kafkaconsumer.OutcomePermanent},
		"not protobuf": {&sarama.ConsumerMessage{Value: []byte{0xff, 0xff}}, nil, kafkaconsumer.OutcomePermanent},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			service := &stubService{err: tc.err}
			result := NewHandler(service, nil).process(context.Background(), tc.message)
			if result.Outcome != tc.want {
				t.Fatalf("outcome = %s (%v)", result.Outcome, result.Err)
			}
			if tc.want == kafkaconsumer.OutcomeSuccess && service.trigger.GetMessageId() != 7 {
				t.Fatalf("trigger = %+v", service.trigger)
			}
		})
	}
}

func TestProcessRoutesEvents(t *testing.T) {
	service := &stubService{}
	message := encode(t, envelope.MessageType_CHATBOT_REQUEST, &chatbotpb.RequestMessage{
		Payload: &chatbotpb.RequestMessage_Event{Event: &chatbotpb.BotEvent{Type: "friend_added", BotUserId: 9}},
	})
	if result := NewHandler(service, nil).process(context.Background(), message); result.Outcome != kafkaconsumer.OutcomeSuccess ||
		service.event.GetType() != "friend_added" || service.trigger != nil {
		t.Fatalf("result=%v event=%v", result.Outcome, service.event)
	}
}
