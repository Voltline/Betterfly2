package main

import (
	"errors"
	"testing"

	"github.com/IBM/sarama"
)

func replayTestMessage(topic string) *sarama.ConsumerMessage {
	return &sarama.ConsumerMessage{
		Value:   []byte("raw-payload"),
		Headers: []*sarama.RecordHeader{{Key: []byte("original_topic"), Value: []byte(topic)}},
	}
}

func TestReplayDefaultsToDryRunWithoutPublishingOrConfirming(t *testing.T) {
	published := false
	handler := &replayHandler{
		config:  replayConfig{dryRun: true, allowed: parseAllowlist("allowed-topic")},
		publish: func(string, []byte) error { published = true; return nil },
	}
	mark, err := handler.process(replayTestMessage("allowed-topic"))
	if err != nil || mark || published {
		t.Fatalf("dry-run changed state: mark=%v published=%v err=%v", mark, published, err)
	}
}

func TestReplayRejectsTopicOutsideAllowlist(t *testing.T) {
	handler := &replayHandler{config: replayConfig{allowed: parseAllowlist("allowed-topic")}}
	if mark, err := handler.process(replayTestMessage("forbidden-topic")); err == nil || mark {
		t.Fatalf("forbidden topic was accepted: mark=%v err=%v", mark, err)
	}
}

func TestReplayMarksOnlyAfterSuccessfulPublish(t *testing.T) {
	handler := &replayHandler{
		config:  replayConfig{allowed: parseAllowlist("allowed-topic")},
		publish: func(string, []byte) error { return errors.New("publish failed") },
	}
	if mark, err := handler.process(replayTestMessage("allowed-topic")); err == nil || mark {
		t.Fatalf("failed publish was confirmed: mark=%v err=%v", mark, err)
	}
	handler.publish = func(topic string, payload []byte) error {
		if topic != "allowed-topic" || string(payload) != "raw-payload" {
			t.Fatalf("replay changed payload: topic=%s payload=%q", topic, payload)
		}
		return nil
	}
	if mark, err := handler.process(replayTestMessage("allowed-topic")); err != nil || !mark {
		t.Fatalf("successful replay was not confirmed: mark=%v err=%v", mark, err)
	}
}
