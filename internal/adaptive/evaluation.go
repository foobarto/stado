package adaptive

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/foobarto/stado/internal/broker/wal"
)

type Evaluation struct {
	SessionID        string    `json:"session_id"`
	PromptDigest     string    `json:"prompt_digest"`
	CorpusSequence   uint64    `json:"corpus_sequence"`
	PolicyVersion    string    `json:"policy_version"`
	Scores           []Score   `json:"scores"`
	ActuallySurfaced []string  `json:"actually_surfaced"`
	CreatedAt        time.Time `json:"created_at"`
}

type evaluationAppender interface {
	Append(wal.Transaction) (wal.AppendResult, error)
	Records() []wal.Record
}

func Record(w evaluationAppender, e Evaluation, principal, actor, idem string) error {
	records := w.Records()
	for _, record := range records {
		if record.Transaction.IdempotencyKey == idem {
			return nil
		}
	}
	if len(records) > 0 {
		e.CorpusSequence = records[len(records)-1].Sequence
	}
	e.PolicyVersion, e.CreatedAt = PolicyVersion, time.Now().UTC()
	b, _ := json.Marshal(e)
	_, err := w.Append(wal.Transaction{ID: randomID("eval_"), IdempotencyKey: idem, Principal: principal, Actor: actor, Events: []wal.Event{{Store: "adaptive_retrieval", Type: "shadow.evaluated", Session: e.SessionID, Data: b}}})
	return err
}

func randomID(prefix string) string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return prefix + hex.EncodeToString(b[:])
}
