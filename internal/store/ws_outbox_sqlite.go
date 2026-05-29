package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/relayra/relayra/internal/models"
)

func (s *SQLite) NextWSOutboundSeq(ctx context.Context, scope string) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin next websocket seq tx: %w", err)
	}
	defer tx.Rollback()

	now := time.Now().Unix()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO ws_sequence_states (scope, next_outbound_seq, last_received_seq, updated_at)
		VALUES (?, 0, 0, ?)
		ON CONFLICT(scope) DO NOTHING
	`, scope, now); err != nil {
		return 0, fmt.Errorf("seed websocket sequence state: %w", err)
	}

	var nextSeq int64
	if err := tx.QueryRowContext(ctx, `
		SELECT next_outbound_seq
		FROM ws_sequence_states
		WHERE scope = ?
	`, scope).Scan(&nextSeq); err != nil {
		return 0, fmt.Errorf("load websocket next seq: %w", err)
	}
	nextSeq++

	if _, err := tx.ExecContext(ctx, `
		UPDATE ws_sequence_states
		SET next_outbound_seq = ?, updated_at = ?
		WHERE scope = ?
	`, nextSeq, now, scope); err != nil {
		return 0, fmt.Errorf("update websocket next seq: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit websocket next seq tx: %w", err)
	}
	return nextSeq, nil
}

func (s *SQLite) EnqueueWSOutbox(ctx context.Context, scope string, seq int64, msgType, refID, payload string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO ws_outbox (scope, seq, type, ref_id, payload, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(scope, seq) DO UPDATE SET
			type = excluded.type,
			ref_id = excluded.ref_id,
			payload = excluded.payload,
			created_at = excluded.created_at
	`, scope, seq, msgType, refID, payload, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("enqueue websocket outbox message: %w", err)
	}
	return nil
}

func (s *SQLite) ListWSOutbox(ctx context.Context, scope string, afterSeq int64, limit int) ([]models.WSOutboxMessage, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT seq, type, ref_id, payload, created_at
		FROM ws_outbox
		WHERE scope = ? AND seq > ?
		ORDER BY seq ASC
		LIMIT ?
	`, scope, afterSeq, limit)
	if err != nil {
		return nil, fmt.Errorf("list websocket outbox: %w", err)
	}
	defer rows.Close()

	messages := make([]models.WSOutboxMessage, 0, limit)
	expectedSeq := afterSeq + 1
	for rows.Next() {
		var msg models.WSOutboxMessage
		var createdAt int64
		var refID sql.NullString
		if err := rows.Scan(&msg.Seq, &msg.Type, &refID, &msg.Payload, &createdAt); err != nil {
			return nil, err
		}
		if msg.Seq != expectedSeq {
			return nil, fmt.Errorf("%w: scope %s expected seq %d but found %d", ErrWSOutboxGap, scope, expectedSeq, msg.Seq)
		}
		msg.Scope = scope
		msg.RefID = refID.String
		msg.CreatedAt = time.Unix(createdAt, 0)
		messages = append(messages, msg)
		expectedSeq++
	}
	return messages, rows.Err()
}

func (s *SQLite) AckWSOutboxThrough(ctx context.Context, scope string, seq int64) ([]models.WSOutboxMessage, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin websocket outbox ack tx: %w", err)
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `
		SELECT seq, type, ref_id, payload, created_at
		FROM ws_outbox
		WHERE scope = ? AND seq <= ?
		ORDER BY seq ASC
	`, scope, seq)
	if err != nil {
		return nil, fmt.Errorf("query websocket outbox ack set: %w", err)
	}

	var acked []models.WSOutboxMessage
	for rows.Next() {
		var msg models.WSOutboxMessage
		var createdAt int64
		var refID sql.NullString
		if err := rows.Scan(&msg.Seq, &msg.Type, &refID, &msg.Payload, &createdAt); err != nil {
			rows.Close()
			return nil, err
		}
		msg.Scope = scope
		msg.RefID = refID.String
		msg.CreatedAt = time.Unix(createdAt, 0)
		acked = append(acked, msg)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if _, err := tx.ExecContext(ctx, `
		DELETE FROM ws_outbox
		WHERE scope = ? AND seq <= ?
	`, scope, seq); err != nil {
		return nil, fmt.Errorf("delete websocket outbox acked set: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit websocket outbox ack tx: %w", err)
	}
	return acked, nil
}

func (s *SQLite) GetWSSequenceState(ctx context.Context, scope string) (*models.WSSequenceState, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT next_outbound_seq, last_received_seq, updated_at
		FROM ws_sequence_states
		WHERE scope = ?
	`, scope)

	var state models.WSSequenceState
	var updatedAt int64
	if err := row.Scan(&state.NextOutboundSeq, &state.LastReceivedSeq, &updatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get websocket sequence state: %w", err)
	}
	state.Scope = scope
	state.UpdatedAt = time.Unix(updatedAt, 0)
	return &state, nil
}

func (s *SQLite) SetWSLastReceivedSeq(ctx context.Context, scope string, seq int64) error {
	now := time.Now().Unix()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO ws_sequence_states (scope, next_outbound_seq, last_received_seq, updated_at)
		VALUES (?, 0, ?, ?)
		ON CONFLICT(scope) DO UPDATE SET
			last_received_seq = CASE
				WHEN excluded.last_received_seq > ws_sequence_states.last_received_seq
				THEN excluded.last_received_seq
				ELSE ws_sequence_states.last_received_seq
			END,
			updated_at = excluded.updated_at
	`, scope, seq, now)
	if err != nil {
		return fmt.Errorf("set websocket last received seq: %w", err)
	}
	return nil
}
