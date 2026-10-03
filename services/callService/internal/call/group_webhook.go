package call

import (
	"context"
	"errors"
	"strings"
)

// LiveKit callbacks confirm actual media connections. SID fencing prevents a
// delayed leave/room-finished callback from removing a newer connection/room.
func (s *Service) HandleMediaWebhook(ctx context.Context, body []byte, authorization string) error {
	if s.groups == nil {
		return ErrMediaUnavailable
	}
	event, err := s.groups.media.VerifyWebhook(body, authorization)
	if err != nil {
		return err
	}
	id := strings.TrimPrefix(event.Room.Name, "bf2-group-")
	if id == event.Room.Name || !validRoomID(id) {
		return nil
	}
	operation := "group-webhook:" + event.ID
	done, err := s.store.OperationCompleted(ctx, operation)
	if err != nil || done {
		return err
	}
	expected, err := s.groups.store.GetRoom(ctx, id)
	if errors.Is(err, ErrCallNotFound) {
		if event.Event == "participant_joined" {
			return s.groups.media.Execute(ctx, "RemoveParticipant", mediaEvent(operation, "RemoveParticipant", event.Room.Name, event.Participant.Identity).Payload)
		}
		return nil
	}
	if err != nil {
		return err
	}
	if event.Room.SID != expected.MediaSID {
		if event.Event == "participant_joined" {
			// Self-hosted SFU restarts may recreate a room via an old token. A
			// matching room name alone does not establish a current admission.
			return s.groups.media.Execute(ctx, "RemoveParticipant", mediaEvent(operation, "RemoveParticipant", expected.Name(), event.Participant.Identity).Payload)
		}
		return nil
	}
	updated := cloneRoom(expected)
	events := []PendingEvent{}
	switch event.Event {
	case "participant_joined":
		index := -1
		for i, p := range expected.Participants {
			if p.Identity == event.Participant.Identity {
				index = i
				break
			}
		}
		valid := expected.State == StateActive && expected.ExpiresAt.After(s.now()) && index >= 0 && event.Participant.SID != ""
		if valid {
			p := expected.Participants[index]
			if !p.Connected && !p.JoinDeadline.After(s.now()) {
				valid = false
			}
			if _, err := s.groups.access.Access(ctx, expected.GroupID, p.UserID); errors.Is(err, ErrForbidden) {
				valid = false
			} else if err != nil {
				return err
			}
		}
		if !valid {
			return s.groups.media.Execute(ctx, "RemoveParticipant", mediaEvent(operation, "RemoveParticipant", expected.Name(), event.Participant.Identity).Payload)
		}
		if updated.Participants[index].Connected && updated.Participants[index].SID == event.Participant.SID {
			return nil
		}
		updated.Participants[index].Connected = true
		updated.Participants[index].SID = event.Participant.SID
	case "participant_left":
		if expected.State != StateActive {
			return nil
		}
		updated.Participants = nil
		removed := false
		for _, p := range expected.Participants {
			if p.Identity == event.Participant.Identity && p.Connected && p.SID == event.Participant.SID {
				removed = true
			} else {
				updated.Participants = append(updated.Participants, p)
			}
		}
		if !removed {
			return nil
		}
		if len(updated.Participants) == 0 {
			updated.State = StateEnded
			events = append(events, mediaEvent(operation, "DeleteRoom", expected.Name(), ""))
		}
	case "room_finished":
		if expected.State != StateActive {
			return nil
		}
		updated.State = StateEnded
	default:
		return nil
	}
	updated.Revision++
	events, err = s.appendRoomBroadcast(ctx, updated, operation, events)
	if err != nil {
		return err
	}
	_, err = s.groups.store.CommitRoom(ctx, &expected, updated, operation, events)
	return err
}
