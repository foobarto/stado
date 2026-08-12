// Package authority implements broker-local, one-use operator-origin grants.
// The broker keeps Issuer capability out of model, plugin, and session APIs;
// those callers may hold only a Consumer.
package authority

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/foobarto/stado/internal/broker/wal"
)

const grantStore = "operator_authority"

var (
	ErrGrantRequired = errors.New("operator-origin grant required")
	ErrGrantMismatch = errors.New("operator-origin grant does not match action")
	ErrGrantExpired  = errors.New("operator-origin grant expired")
	ErrGrantConsumed = errors.New("operator-origin grant already consumed")
)

// Action is the exact canonical mutation approved by the operator. PayloadDigest
// covers final text/capabilities/corpus; Version prevents stale approval reuse.
type Action struct {
	Kind          string `json:"kind"`
	Principal     string `json:"principal"`
	ScopeDigest   string `json:"scope_digest"`
	PayloadDigest string `json:"payload_digest"`
	Version       uint64 `json:"version"`
}

func DigestAction(a Action) (string, error) {
	b, err := json.Marshal(a)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

type Grant struct {
	ID           string    `json:"id"`
	ActionDigest string    `json:"action_digest"`
	Principal    string    `json:"principal"`
	Actor        string    `json:"actor"`
	IssuedAt     time.Time `json:"issued_at"`
	ExpiresAt    time.Time `json:"expires_at"`
	Nonce        string    `json:"nonce"`
}

type appender interface {
	Append(wal.Transaction) (wal.AppendResult, error)
	Records() []wal.Record
}

type shared struct {
	mu  sync.Mutex
	wal appender
	now func() time.Time
}

// Issuer is the capability reserved for a broker-owned trusted presentation
// channel. It must never be registered as an agent/plugin/session tool.
type Issuer struct{ state *shared }

// Consumer validates and consumes grants but cannot mint them.
type Consumer struct{ state *shared }

func New(store appender) (*Issuer, *Consumer) {
	s := &shared{wal: store, now: time.Now}
	return &Issuer{state: s}, &Consumer{state: s}
}

func (i *Issuer) Issue(ctx context.Context, action Action, actor string, ttl time.Duration) (Grant, error) {
	_ = ctx
	if ttl <= 0 || action.Kind == "" || action.Principal == "" || actor == "" {
		return Grant{}, errors.New("invalid operator grant request")
	}
	digest, err := DigestAction(action)
	if err != nil {
		return Grant{}, err
	}
	now := i.state.now().UTC()
	g := Grant{ID: mint("grant_"), ActionDigest: digest, Principal: action.Principal, Actor: actor, IssuedAt: now, ExpiresAt: now.Add(ttl), Nonce: mint("")}
	b, _ := json.Marshal(g)
	_, err = i.state.wal.Append(wal.Transaction{ID: mint("tx_"), IdempotencyKey: "grant.issue:" + g.ID, Principal: g.Principal, Actor: actor, Events: []wal.Event{{Store: grantStore, Type: "grant.issued", Data: b}}})
	return g, err
}

// PrepareConsume validates a grant and returns the consumption event that must
// be committed in the same WAL transaction as the approved mutation.
func (c *Consumer) PrepareConsume(ctx context.Context, grantID string, action Action, idempotencyKey string) (wal.Event, error) {
	_ = ctx
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	grants, consumed, err := fold(c.state.wal.Records())
	if err != nil {
		return wal.Event{}, err
	}
	g, ok := grants[grantID]
	if !ok {
		return wal.Event{}, ErrGrantRequired
	}
	digest, err := DigestAction(action)
	if err != nil {
		return wal.Event{}, err
	}
	if digest != g.ActionDigest || action.Principal != g.Principal {
		return wal.Event{}, ErrGrantMismatch
	}
	if !c.state.now().Before(g.ExpiresAt) {
		return wal.Event{}, ErrGrantExpired
	}
	if prior, ok := consumed[grantID]; ok {
		if prior == idempotencyKey {
			return wal.Event{}, nil
		}
		return wal.Event{}, ErrGrantConsumed
	}
	payload, _ := json.Marshal(struct {
		GrantID string `json:"grant_id"`
		UseKey  string `json:"use_key"`
	}{grantID, idempotencyKey})
	return wal.Event{Store: grantStore, Type: "grant.consumed", Data: payload}, nil
}

func fold(records []wal.Record) (map[string]Grant, map[string]string, error) {
	grants := map[string]Grant{}
	consumed := map[string]string{}
	for _, rec := range records {
		for _, ev := range rec.Transaction.Events {
			if ev.Store != grantStore {
				continue
			}
			switch ev.Type {
			case "grant.issued":
				var g Grant
				if err := json.Unmarshal(ev.Data, &g); err != nil {
					return nil, nil, err
				}
				grants[g.ID] = g
			case "grant.consumed":
				var use struct {
					GrantID string `json:"grant_id"`
					UseKey  string `json:"use_key"`
				}
				if err := json.Unmarshal(ev.Data, &use); err != nil {
					return nil, nil, err
				}
				if _, duplicate := consumed[use.GrantID]; duplicate {
					return nil, nil, fmt.Errorf("duplicate grant consumption: %s", use.GrantID)
				}
				consumed[use.GrantID] = use.UseKey
			}
		}
	}
	return grants, consumed, nil
}

func mint(prefix string) string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return prefix + hex.EncodeToString(b[:])
}
