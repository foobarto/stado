// Package budget is the durable recursive reservation ledger for agents/jobs.
package budget

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/foobarto/stado/internal/broker/wal"
	"strings"
	"sync"
)

const storeName = "budget"

var (
	ErrExhausted = errors.New("budget exhausted")
	ErrNotFound  = errors.New("budget account or reservation not found")
)

type Limits struct {
	Tokens      uint64 `json:"tokens"`
	CostMicros  uint64 `json:"cost_micros"`
	ToolCalls   uint64 `json:"tool_calls"`
	ReadBytes   uint64 `json:"read_bytes"`
	Turns       uint64 `json:"turns"`
	WallSeconds uint64 `json:"wall_seconds"`
}

func (a Limits) add(b Limits) Limits {
	return Limits{a.Tokens + b.Tokens, a.CostMicros + b.CostMicros, a.ToolCalls + b.ToolCalls, a.ReadBytes + b.ReadBytes, a.Turns + b.Turns, a.WallSeconds + b.WallSeconds}
}
func (a Limits) fits(max Limits) bool {
	return (max.Tokens == 0 || a.Tokens <= max.Tokens) && (max.CostMicros == 0 || a.CostMicros <= max.CostMicros) && (max.ToolCalls == 0 || a.ToolCalls <= max.ToolCalls) && (max.ReadBytes == 0 || a.ReadBytes <= max.ReadBytes) && (max.Turns == 0 || a.Turns <= max.Turns) && (max.WallSeconds == 0 || a.WallSeconds <= max.WallSeconds)
}

type Account struct {
	ID        string `json:"id"`
	RootID    string `json:"root_id"`
	ParentID  string `json:"parent_id,omitempty"`
	Ceiling   Limits `json:"ceiling"`
	Reserved  Limits `json:"reserved"`
	Committed Limits `json:"committed"`
}
type Reservation struct {
	ID        string `json:"id"`
	AccountID string `json:"account_id"`
	RootID    string `json:"root_id"`
	Amount    Limits `json:"amount"`
	Committed Limits `json:"committed"`
	Released  bool   `json:"released"`
}
type appender interface {
	Append(wal.Transaction) (wal.AppendResult, error)
	Records() []wal.Record
}
type Ledger struct {
	mu  sync.Mutex
	wal appender
}

func New(w appender) *Ledger { return &Ledger{wal: w} }
func (l *Ledger) GetAccount(id string) (Account, bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	accounts, _, err := fold(l.wal.Records())
	if err != nil {
		return Account{}, false, err
	}
	a, ok := accounts[id]
	return a, ok, nil
}
func (l *Ledger) CreateAccount(ctx context.Context, id, parent string, ceiling Limits, principal, actor, idem string) (Account, error) {
	_ = ctx
	l.mu.Lock()
	defer l.mu.Unlock()
	accounts, _, err := fold(l.wal.Records())
	if err != nil {
		return Account{}, err
	}
	if id == "" {
		id = mint("acct_")
	}
	if _, ok := accounts[id]; ok {
		return Account{}, errors.New("account exists")
	}
	root := id
	if parent != "" {
		p, ok := accounts[parent]
		if !ok {
			return Account{}, ErrNotFound
		}
		root = p.RootID
		if !ceiling.fits(p.Ceiling) {
			return Account{}, ErrExhausted
		}
	}
	a := Account{ID: id, RootID: root, ParentID: parent, Ceiling: ceiling}
	if err := appendEvent(l.wal, a, principal, actor, idem, "account.created"); err != nil {
		return Account{}, err
	}
	return a, nil
}
func (l *Ledger) Reserve(ctx context.Context, accountID string, amount Limits, principal, actor, idem string) (Reservation, error) {
	_ = ctx
	l.mu.Lock()
	defer l.mu.Unlock()
	accounts, reservations, err := fold(l.wal.Records())
	if err != nil {
		return Reservation{}, err
	}
	a, ok := accounts[accountID]
	if !ok {
		return Reservation{}, ErrNotFound
	}
	root := accounts[a.RootID]
	aggregate := root.Reserved.add(amount)
	if !aggregate.fits(root.Ceiling) || !a.Reserved.add(amount).fits(a.Ceiling) {
		return Reservation{}, ErrExhausted
	}
	r := Reservation{ID: mint("res_"), AccountID: accountID, RootID: a.RootID, Amount: amount}
	_ = reservations
	if err := appendEvent(l.wal, r, principal, actor, idem, "reservation.created"); err != nil {
		return Reservation{}, err
	}
	return r, nil
}
func (l *Ledger) Commit(ctx context.Context, id string, usage Limits, principal, actor, idem string) (Reservation, error) {
	_ = ctx
	l.mu.Lock()
	defer l.mu.Unlock()
	_, reservations, err := fold(l.wal.Records())
	if err != nil {
		return Reservation{}, err
	}
	r, ok := reservations[id]
	if !ok {
		return Reservation{}, ErrNotFound
	}
	if r.Released || !r.Committed.add(usage).fits(r.Amount) {
		return Reservation{}, ErrExhausted
	}
	r.Committed = r.Committed.add(usage)
	if err := appendEvent(l.wal, r, principal, actor, idem, "reservation.committed"); err != nil {
		return Reservation{}, err
	}
	return r, nil
}
func (l *Ledger) Release(ctx context.Context, id, principal, actor, idem string) (Reservation, error) {
	_ = ctx
	l.mu.Lock()
	defer l.mu.Unlock()
	_, rs, err := fold(l.wal.Records())
	if err != nil {
		return Reservation{}, err
	}
	r, ok := rs[id]
	if !ok {
		return Reservation{}, ErrNotFound
	}
	r.Released = true
	if err := appendEvent(l.wal, r, principal, actor, idem, "reservation.released"); err != nil {
		return Reservation{}, err
	}
	return r, nil
}
func fold(records []wal.Record) (map[string]Account, map[string]Reservation, error) {
	accounts := map[string]Account{}
	rs := map[string]Reservation{}
	for _, rec := range records {
		for _, ev := range rec.Transaction.Events {
			if ev.Store != storeName {
				continue
			}
			switch {
			case strings.HasPrefix(ev.Type, "account."):
				var a Account
				if err := json.Unmarshal(ev.Data, &a); err != nil {
					return nil, nil, err
				}
				accounts[a.ID] = a
			case strings.HasPrefix(ev.Type, "reservation."):
				var r Reservation
				if err := json.Unmarshal(ev.Data, &r); err != nil {
					return nil, nil, err
				}
				old := rs[r.ID]
				if old.ID != "" {
					r.Amount = old.Amount
					r.AccountID = old.AccountID
					r.RootID = old.RootID
				}
				rs[r.ID] = r
			}
		}
	}
	for id, a := range accounts {
		var reserved, committed Limits
		for _, r := range rs {
			if r.Released {
				continue
			}
			if r.AccountID == id {
				reserved = reserved.add(r.Amount)
				committed = committed.add(r.Committed)
			}
			if r.RootID == id && r.AccountID != id {
				reserved = reserved.add(r.Amount)
				committed = committed.add(r.Committed)
			}
		}
		a.Reserved = reserved
		a.Committed = committed
		accounts[id] = a
	}
	return accounts, rs, nil
}
func appendEvent(w appender, v any, principal, actor, idem, typ string) error {
	b, _ := json.Marshal(v)
	_, err := w.Append(wal.Transaction{ID: mint("tx_"), IdempotencyKey: idem, Principal: principal, Actor: actor, Events: []wal.Event{{Store: storeName, Type: typ, Data: b}}})
	return err
}
func mint(prefix string) string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return prefix + hex.EncodeToString(b[:])
}
