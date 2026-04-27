package session

import (
	"context"
	"fmt"
	"time"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
)

// CompactionCheckpoint records the active model-visible transcript after a
// context compaction. Stores keep the raw append-only transcript separately;
// the checkpoint is a durable projection used by future model requests.
type CompactionCheckpoint struct {
	ID              string
	CreatedAt       time.Time
	RawMessageCount int
	Messages        []model.Message
	Policy          string
	Reason          string
	SummaryHash     string
	SummaryPreview  string
}

// MessageView is the transcript view a model request should use. RawMessageCount
// is the current number of raw transcript messages at the time the view was
// read, so callers can checkpoint the exact prefix they compacted. Compaction,
// when non-nil, is the checkpoint used to build Messages; hosts can inspect it
// for observability and debugging.
type MessageView struct {
	Messages        []model.Message
	RawMessageCount int
	Compaction      *CompactionCheckpoint
}

// StoreWithCompaction is implemented by stores that persist a compacted active
// transcript view while preserving the raw append-only transcript.
type StoreWithCompaction interface {
	MessageView(context.Context, string) (MessageView, error)
	SaveCompaction(context.Context, string, CompactionCheckpoint) error
}

// MessageView returns the model-visible transcript view for id. Stores that do
// not support compaction fall back to the raw transcript.
func MessageViewForSession(ctx context.Context, store Store, id string) (MessageView, error) {
	if store == nil {
		return MessageView{}, fmt.Errorf("session store is required")
	}
	if compacting, ok := store.(StoreWithCompaction); ok {
		return compacting.MessageView(ctx, id)
	}
	messages, err := store.Messages(ctx, id)
	if err != nil {
		return MessageView{}, err
	}
	return MessageView{
		Messages:        model.CloneMessages(messages),
		RawMessageCount: len(messages),
	}, nil
}

// SaveCompaction persists checkpoint when the store supports durable
// compaction. Stores without compaction support keep the SDK's older
// send-time-only behavior.
func SaveCompaction(ctx context.Context, store Store, id string, checkpoint CompactionCheckpoint) error {
	if store == nil {
		return fmt.Errorf("session store is required")
	}
	if compacting, ok := store.(StoreWithCompaction); ok {
		return compacting.SaveCompaction(ctx, id, checkpoint)
	}
	return nil
}

func normalizeCompactionCheckpoint(checkpoint CompactionCheckpoint) (CompactionCheckpoint, error) {
	if checkpoint.RawMessageCount < 0 {
		return CompactionCheckpoint{}, fmt.Errorf("compaction raw message count must be non-negative")
	}
	if checkpoint.ID == "" {
		id, err := newID()
		if err != nil {
			return CompactionCheckpoint{}, err
		}
		checkpoint.ID = id
	} else {
		canonical, ok := CanonicalID(checkpoint.ID)
		if !ok {
			return CompactionCheckpoint{}, fmt.Errorf("invalid compaction id: %q", checkpoint.ID)
		}
		checkpoint.ID = canonical
	}
	if checkpoint.CreatedAt.IsZero() {
		checkpoint.CreatedAt = time.Now().UTC()
	} else {
		checkpoint.CreatedAt = checkpoint.CreatedAt.UTC()
	}
	checkpoint.Messages = model.CloneMessages(checkpoint.Messages)
	return checkpoint, nil
}

func messageView(raw []model.Message, checkpoint *CompactionCheckpoint) (MessageView, error) {
	if checkpoint == nil {
		return MessageView{
			Messages:        model.CloneMessages(raw),
			RawMessageCount: len(raw),
		}, nil
	}
	if checkpoint.RawMessageCount > len(raw) {
		return MessageView{}, fmt.Errorf("compaction raw message count %d exceeds transcript length %d", checkpoint.RawMessageCount, len(raw))
	}
	active := model.CloneMessages(checkpoint.Messages)
	active = append(active, model.CloneMessages(raw[checkpoint.RawMessageCount:])...)
	copied := *checkpoint
	copied.Messages = model.CloneMessages(checkpoint.Messages)
	return MessageView{
		Messages:        active,
		RawMessageCount: len(raw),
		Compaction:      &copied,
	}, nil
}
