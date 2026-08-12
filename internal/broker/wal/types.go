// Package wal implements the broker-owned durable transaction log used by
// adaptive context state. It is intentionally model-agnostic: callers submit
// typed JSON events; the WAL owns ordering, idempotency, chaining, and recovery.
package wal

import (
	"encoding/json"
	"errors"
)

const SchemaVersion = 1

var (
	ErrLocked      = errors.New("broker wal: store is locked by another broker")
	ErrClosed      = errors.New("broker wal: store is closed")
	ErrCorrupt     = errors.New("broker wal: corrupt log")
	ErrConflict    = errors.New("broker wal: idempotency key reused with different transaction")
	ErrInvalidTail = errors.New("broker wal: invalid final record recovered")
)

// Event is one typed mutation committed atomically with the other events in a
// Transaction. Data must be JSON; package users own its schema.
type Event struct {
	Store   string          `json:"store"`
	Type    string          `json:"type"`
	Session string          `json:"session,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// Transaction is the caller-supplied atomic unit.
type Transaction struct {
	ID             string  `json:"id"`
	IdempotencyKey string  `json:"idempotency_key"`
	Principal      string  `json:"principal"`
	Actor          string  `json:"actor"`
	CausationID    string  `json:"causation_id,omitempty"`
	Events         []Event `json:"events"`
}

// Record is the durable canonical envelope. Digest covers every field except
// Digest itself, including PreviousDigest and the entire transaction.
type Record struct {
	Schema         int         `json:"schema"`
	Sequence       uint64      `json:"sequence"`
	Epoch          uint64      `json:"epoch"`
	Timestamp      string      `json:"timestamp"`
	PreviousDigest string      `json:"previous_digest,omitempty"`
	Transaction    Transaction `json:"transaction"`
	Digest         string      `json:"digest"`
}

// AppendResult reports whether an idempotency retry found an existing record.
type AppendResult struct {
	Record     Record
	Previously bool
}
