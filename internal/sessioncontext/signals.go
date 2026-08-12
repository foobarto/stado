package sessioncontext

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/foobarto/stado/internal/broker/wal"
)

const detectorVersion = 1

// Observe commits a mechanical observation and all deterministic signals it
// produces in one WAL transaction. It never interprets free-form model prose.
func (s *Service) Observe(ctx context.Context, obs Observation, principal, actor, idem string) ([]Signal, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	if obs.SessionID == "" || obs.Kind == "" || obs.EvidenceRef == "" {
		return nil, errors.New("observation session, kind, and evidence ref required")
	}
	if obs.ID == "" {
		obs.ID = mint("obs_")
	}
	obs.CreatedAt = s.now().UTC()
	if len(obs.Attributes) > 16 {
		return nil, errors.New("too many observation attributes")
	}
	for k, v := range obs.Attributes {
		if bounded(k, 64) != nil || bounded(v, 512) != nil {
			return nil, errors.New("observation attribute exceeds limit")
		}
	}
	prior, err := foldObservations(s.wal.Records(), obs.SessionID)
	if err != nil {
		return nil, err
	}
	signals := detect(prior, obs, s.now().UTC())
	ob, _ := json.Marshal(obs)
	events := []wal.Event{{Store: sessionStore, Type: "observation.recorded", Session: obs.SessionID, Data: ob}}
	for _, sig := range signals {
		b, _ := json.Marshal(sig)
		events = append(events, wal.Event{Store: sessionStore, Type: "signal.detected", Session: obs.SessionID, Data: b})
	}
	_, err = s.wal.Append(wal.Transaction{ID: mint("tx_"), IdempotencyKey: idem, Principal: principal, Actor: actor, Events: events})
	return signals, err
}

func (s *Service) Signals(session string, includeExpired bool) ([]Signal, error) {
	return s.SignalsAt(session, includeExpired, ^uint64(0))
}

// SignalsAt returns the projection captured at a canonical WAL sequence.
func (s *Service) SignalsAt(session string, includeExpired bool, asOf uint64) ([]Signal, error) {
	now := s.now()
	var out []Signal
	for _, rec := range s.wal.Records() {
		if rec.Sequence > asOf {
			break
		}
		for _, ev := range rec.Transaction.Events {
			if ev.Store != sessionStore || ev.Session != session || ev.Type != "signal.detected" {
				continue
			}
			var sig Signal
			if err := json.Unmarshal(ev.Data, &sig); err != nil {
				return nil, err
			}
			if includeExpired || sig.ExpiresAt.After(now) {
				out = append(out, sig)
			}
		}
	}
	return out, nil
}

func foldObservations(records []wal.Record, session string) ([]Observation, error) {
	var out []Observation
	for _, rec := range records {
		for _, ev := range rec.Transaction.Events {
			if ev.Store != sessionStore || ev.Session != session || ev.Type != "observation.recorded" {
				continue
			}
			var o Observation
			if err := json.Unmarshal(ev.Data, &o); err != nil {
				return nil, err
			}
			out = append(out, o)
		}
	}
	return out, nil
}

func detect(prior []Observation, current Observation, now time.Time) []Signal {
	refs := func(a Observation) []string { return []string{a.EvidenceRef, current.EvidenceRef} }
	makeSignal := func(typ SignalType, origins []string, attrs map[string]string) Signal {
		return Signal{ID: mint("sig_"), SessionID: current.SessionID, Type: typ, DetectorVersion: detectorVersion, Confidence: "fact", OriginRefs: origins, Attributes: attrs, CreatedAt: now, ExpiresAt: now.Add(30 * 24 * time.Hour)}
	}
	var out []Signal
	switch current.Kind {
	case ObservationTool:
		for i := len(prior) - 1; i >= 0; i-- {
			p := prior[i]
			if p.Kind != ObservationTool || p.Tool != current.Tool {
				continue
			}
			if !p.Succeeded && !current.Succeeded && p.ArgsDigest == current.ArgsDigest {
				out = append(out, makeSignal(SignalRepeatedToolFailure, refs(p), map[string]string{"tool": current.Tool, "args_digest": current.ArgsDigest}))
				break
			}
			if !p.Succeeded && current.Succeeded && p.ArgsDigest != "" && current.ArgsDigest != "" && p.ArgsDigest != current.ArgsDigest {
				out = append(out, makeSignal(SignalArgumentChangedSuccess, refs(p), map[string]string{"tool": current.Tool, "failed_args": p.ArgsDigest, "successful_args": current.ArgsDigest}))
				break
			}
		}
	case ObservationVerification:
		if current.Succeeded {
			for i := len(prior) - 1; i >= 0; i-- {
				p := prior[i]
				if p.Kind == ObservationVerification && !p.Succeeded {
					out = append(out, makeSignal(SignalVerificationRecovered, refs(p), nil))
					break
				}
			}
		}
	case ObservationDenial:
		count := 0
		origins := []string{current.EvidenceRef}
		for i := len(prior) - 1; i >= 0; i-- {
			p := prior[i]
			if p.Kind == ObservationDenial && sameAttr(p.Attributes, current.Attributes, "boundary") {
				count++
				origins = append(origins, p.EvidenceRef)
				if count == 1 {
					break
				}
			}
		}
		if count >= 1 {
			out = append(out, makeSignal(SignalRecurringDenial, origins, map[string]string{"boundary": current.Attributes["boundary"]}))
		}
	case ObservationCorrection:
		if strings.EqualFold(current.Attributes["origin"], "operator") {
			out = append(out, makeSignal(SignalOperatorCorrection, []string{current.EvidenceRef}, map[string]string{"marker": current.Attributes["marker"]}))
		}
	}
	return out
}

func sameAttr(a, b map[string]string, key string) bool { return a[key] != "" && a[key] == b[key] }
