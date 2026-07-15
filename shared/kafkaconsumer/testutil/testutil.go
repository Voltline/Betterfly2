package testutil

import (
	"context"

	"github.com/IBM/sarama"
)

type Session struct {
	Ctx    context.Context
	Marked []*sarama.ConsumerMessage
}

func NewSession(ctx context.Context) *Session {
	if ctx == nil {
		ctx = context.Background()
	}
	return &Session{Ctx: ctx}
}

func (s *Session) Claims() map[string][]int32                      { return nil }
func (s *Session) MemberID() string                                { return "test" }
func (s *Session) GenerationID() int32                             { return 1 }
func (s *Session) MarkOffset(string, int32, int64, string)         {}
func (s *Session) Commit()                                         {}
func (s *Session) ResetOffset(string, int32, int64, string)        {}
func (s *Session) MarkMessage(m *sarama.ConsumerMessage, _ string) { s.Marked = append(s.Marked, m) }
func (s *Session) Context() context.Context                        { return s.Ctx }

type Claim struct {
	TopicName     string
	PartitionID   int32
	MessageStream chan *sarama.ConsumerMessage
}

func NewClaim(messages ...*sarama.ConsumerMessage) *Claim {
	claim := &Claim{TopicName: "source", MessageStream: make(chan *sarama.ConsumerMessage, len(messages))}
	for _, message := range messages {
		claim.MessageStream <- message
	}
	close(claim.MessageStream)
	return claim
}

func (c *Claim) Topic() string              { return c.TopicName }
func (c *Claim) Partition() int32           { return c.PartitionID }
func (c *Claim) InitialOffset() int64       { return 0 }
func (c *Claim) HighWaterMarkOffset() int64 { return 0 }
func (c *Claim) Messages() <-chan *sarama.ConsumerMessage {
	return c.MessageStream
}
