package retained

import (
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/foobarto/stado/internal/broker/wal"
)

type FailureClass string

const (
	FailureTransient    FailureClass = "transient_runtime"
	FailureLogical      FailureClass = "logical"
	FailurePolicy       FailureClass = "policy"
	FailureBudget       FailureClass = "budget"
	FailureCancelled    FailureClass = "cancelled"
	FailureVerification FailureClass = "verification"
)

type RestartPolicy struct {
	Mode        string        `json:"mode"`
	MaxRestarts int           `json:"max_restarts"`
	Window      time.Duration `json:"window"`
	BaseBackoff time.Duration `json:"base_backoff"`
	MaxBackoff  time.Duration `json:"max_backoff"`
}
type RestartRecord struct {
	AdmissionID string        `json:"admission_id"`
	Generation  uint64        `json:"generation"`
	Class       FailureClass  `json:"class"`
	At          time.Time     `json:"at"`
	Restart     bool          `json:"restart"`
	Backoff     time.Duration `json:"backoff,omitempty"`
	Reason      string        `json:"reason"`
}

// DecideRestart records a bounded supervision decision. Only host-classified
// transient runtime failures can restart; prompts cannot choose the class.
func (r *Registry) DecideRestart(id string, class FailureClass, policy RestartPolicy, principal, actor, idem string) (RestartRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok, err := foldOne(r.wal.Records(), id)
	if err != nil {
		return RestartRecord{}, err
	}
	if !ok {
		return RestartRecord{}, ErrNotFound
	}
	now := r.now().UTC()
	record := RestartRecord{AdmissionID: id, Generation: a.Generation, Class: class, At: now}
	if policy.Mode != "on_transient_failure" || class != FailureTransient {
		record.Reason = "failure class is not restartable"
	} else {
		history, err := restartHistory(r.wal.Records(), id)
		if err != nil {
			return RestartRecord{}, err
		}
		cutoff := now.Add(-policy.Window)
		count := 0
		for _, h := range history {
			if h.Restart && h.At.After(cutoff) {
				count++
			}
		}
		if count >= policy.MaxRestarts {
			record.Reason = "restart intensity exhausted"
		} else {
			record.Restart = true
			backoff := policy.BaseBackoff * time.Duration(1<<count)
			if policy.MaxBackoff > 0 && backoff > policy.MaxBackoff {
				backoff = policy.MaxBackoff
			}
			record.Backoff = backoff
			record.Reason = "bounded transient restart"
		}
	}
	b, _ := json.Marshal(record)
	_, err = r.wal.Append(wal.Transaction{ID: mint("tx_"), IdempotencyKey: idem, Principal: principal, Actor: actor, Events: []wal.Event{{Store: storeName, Type: "supervision.restart_decision", Session: a.ChildSessionID, Data: b}}})
	return record, err
}
func restartHistory(records []wal.Record, id string) ([]RestartRecord, error) {
	var out []RestartRecord
	for _, rec := range records {
		for _, ev := range rec.Transaction.Events {
			if ev.Store != storeName || ev.Type != "supervision.restart_decision" {
				continue
			}
			var x RestartRecord
			if err := json.Unmarshal(ev.Data, &x); err != nil {
				return nil, err
			}
			if x.AdmissionID == id {
				out = append(out, x)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out, nil
}

var ErrRestartExhausted = errors.New("restart intensity exhausted")
