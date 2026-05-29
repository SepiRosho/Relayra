package store

import (
	"context"
	"errors"
	"testing"
)

func TestSQLiteListWSOutboxRejectsSequenceGaps(t *testing.T) {
	s := newTestSQLite(t)
	ctx := context.Background()

	if err := s.EnqueueWSOutbox(ctx, "listener:peer-1", 1, "push_chunk", "req-1:0", `{"seq":1}`); err != nil {
		t.Fatalf("EnqueueWSOutbox(seq=1) error = %v", err)
	}
	if err := s.EnqueueWSOutbox(ctx, "listener:peer-1", 3, "push_chunk", "req-1:2", `{"seq":3}`); err != nil {
		t.Fatalf("EnqueueWSOutbox(seq=3) error = %v", err)
	}

	_, err := s.ListWSOutbox(ctx, "listener:peer-1", 0, 10)
	if !errors.Is(err, ErrWSOutboxGap) {
		t.Fatalf("ListWSOutbox() error = %v, want ErrWSOutboxGap", err)
	}
}

func TestSQLiteListWSOutboxAllowsContiguousResumeWindow(t *testing.T) {
	s := newTestSQLite(t)
	ctx := context.Background()

	if err := s.EnqueueWSOutbox(ctx, "listener:peer-1", 2, "push_chunk", "req-1:1", `{"seq":2}`); err != nil {
		t.Fatalf("EnqueueWSOutbox(seq=2) error = %v", err)
	}
	if err := s.EnqueueWSOutbox(ctx, "listener:peer-1", 3, "push_chunk", "req-1:2", `{"seq":3}`); err != nil {
		t.Fatalf("EnqueueWSOutbox(seq=3) error = %v", err)
	}

	msgs, err := s.ListWSOutbox(ctx, "listener:peer-1", 1, 10)
	if err != nil {
		t.Fatalf("ListWSOutbox() error = %v", err)
	}
	if len(msgs) != 2 || msgs[0].Seq != 2 || msgs[1].Seq != 3 {
		t.Fatalf("ListWSOutbox() = %+v, want seqs [2 3]", msgs)
	}
}
