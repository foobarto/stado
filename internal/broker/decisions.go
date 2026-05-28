package broker

import (
	"encoding/json"
	"io"
	"sync"
	"time"
)

// DecisionRecord is one entry in the broker-decision log. Captures
// the request, the decision, and the timestamp. Serialised as
// JSONL (one record per line) to the configured writer.
type DecisionRecord struct {
	Time     time.Time         `json:"t"`
	Request  CapabilityRequest `json:"req"`
	Decision Decision          `json:"dec"`
}

// DecisionWriter is the interface for sinks that record broker
// decisions. Phase 5 ships the file-backed implementation; phase 1
// uses an in-memory writer in tests and io.Discard in production
// (until phase 5 lands the canonical file path).
type DecisionWriter interface {
	Write(record DecisionRecord) error
}

// NewJSONLWriter wraps an io.Writer in a DecisionWriter that
// appends one JSON object per line. Concurrent-safe: serialises
// writes under an internal mutex so newline-delimited records
// never interleave.
func NewJSONLWriter(w io.Writer) DecisionWriter {
	return &jsonlWriter{w: w}
}

type jsonlWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (j *jsonlWriter) Write(r DecisionRecord) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	enc := json.NewEncoder(j.w)
	return enc.Encode(r)
}

// MemoryWriter is a DecisionWriter that records to an in-memory
// slice. Used in tests; also useful for diagnostic surfaces that
// want to display recent decisions in-process.
type MemoryWriter struct {
	mu      sync.Mutex
	records []DecisionRecord
}

// NewMemoryWriter returns an empty MemoryWriter ready to receive
// records.
func NewMemoryWriter() *MemoryWriter {
	return &MemoryWriter{}
}

// Write appends r to the in-memory record set. Never errors.
func (m *MemoryWriter) Write(r DecisionRecord) error {
	m.mu.Lock()
	m.records = append(m.records, r)
	m.mu.Unlock()
	return nil
}

// Records returns a snapshot of the recorded decisions in
// insertion order. The returned slice is a copy; the caller may
// mutate it freely.
func (m *MemoryWriter) Records() []DecisionRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]DecisionRecord, len(m.records))
	copy(out, m.records)
	return out
}
