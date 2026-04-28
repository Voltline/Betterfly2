package mq

import (
	envelope "Betterfly2/proto/envelope"
	"fmt"

	"google.golang.org/protobuf/proto"
)

type Publisher func(message string, targetTopic string) error

func MarshalEnvelope(messageType envelope.MessageType, payload proto.Message) ([]byte, error) {
	payloadBytes, err := proto.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal envelope payload: %w", err)
	}
	return MarshalEnvelopeBytes(messageType, payloadBytes)
}

func MarshalEnvelopeBytes(messageType envelope.MessageType, payload []byte) ([]byte, error) {
	envBytes, err := proto.Marshal(&envelope.Envelope{
		Type:    messageType,
		Payload: payload,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal envelope: %w", err)
	}
	return envBytes, nil
}

func PublishEnvelope(publish Publisher, targetTopic string, messageType envelope.MessageType, payload proto.Message) ([]byte, error) {
	envBytes, err := MarshalEnvelope(messageType, payload)
	if err != nil {
		return nil, err
	}
	if err := publish(string(envBytes), targetTopic); err != nil {
		return nil, err
	}
	return envBytes, nil
}
