package handlers

import (
	pb "Betterfly2/proto/data_forwarding"
	"Betterfly2/shared/db"
)

// Recheck recipients on the receiving Pod as well: a delayed Kafka envelope
// is not proof that a user still belongs to a private channel/discussion group.
func DiscussionDeliveryRecipients(post *pb.Post, candidates []int64) ([]int64, error) {
	if post.GetDiscussionRootMessageId() <= 0 {
		return candidates, nil
	}
	ids, err := db.MessageRecipientIDsWithDB(db.DB(), post.GetMessageId())
	if err != nil {
		return nil, err
	}
	allowed := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		allowed[id] = struct{}{}
	}
	result := make([]int64, 0, len(candidates))
	for _, id := range candidates {
		if _, ok := allowed[id]; ok {
			result = append(result, id)
		}
	}
	return result, nil
}
