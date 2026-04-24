package memory

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const eventType = "memory"

type Item struct {
	ID          string    `json:"id"`
	Scope       string    `json:"scope"`
	RepoID      string    `json:"repo_id,omitempty"`
	SessionID   string    `json:"session_id,omitempty"`
	Kind        string    `json:"kind"`
	Summary     string    `json:"summary"`
	Body        string    `json:"body,omitempty"`
	Source      Source    `json:"source,omitempty"`
	Confidence  string    `json:"confidence"`
	Sensitivity string    `json:"sensitivity"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	ExpiresAt   time.Time `json:"expires_at,omitempty"`
	Supersedes  []string  `json:"supersedes,omitempty"`
	Tags        []string  `json:"tags,omitempty"`
}

type Source struct {
	SessionID string `json:"session_id,omitempty"`
	Turn      int    `json:"turn,omitempty"`
	Commit    string `json:"commit,omitempty"`
	CreatedBy string `json:"created_by,omitempty"`
}

type UpdateRequest struct {
	Action string `json:"action"`
	ID     string `json:"id,omitempty"`
	Item   *Item  `json:"item,omitempty"`
}

type Query struct {
	RepoID        string   `json:"repo_id,omitempty"`
	SessionID     string   `json:"session_id,omitempty"`
	Prompt        string   `json:"prompt,omitempty"`
	BudgetTokens  int      `json:"budget_tokens,omitempty"`
	MaxItems      int      `json:"max_items,omitempty"`
	AllowedScopes []string `json:"allowed_scopes,omitempty"`
}

type RankedItem struct {
	Item   Item   `json:"item"`
	Rank   int    `json:"rank"`
	Reason string `json:"reason"`
}

type QueryResult struct {
	Items []RankedItem `json:"items"`
}

type Export struct {
	Items []Item `json:"items"`
}

type Store struct {
	Path  string
	Actor string
	Now   func() time.Time
}

type event struct {
	Type      string    `json:"type"`
	Action    string    `json:"action"`
	ID        string    `json:"id,omitempty"`
	Actor     string    `json:"actor,omitempty"`
	Timestamp time.Time `json:"timestamp"`
	Item      *Item     `json:"item,omitempty"`
}

func (s *Store) Propose(_ context.Context, raw []byte) error {
	var item Item
	if err := json.Unmarshal(raw, &item); err != nil {
		return fmt.Errorf("memory propose: parse item: %w", err)
	}
	if item.Confidence == "" {
		item.Confidence = "candidate"
	}
	if item.Confidence != "candidate" {
		return fmt.Errorf("memory propose: confidence must be candidate, got %q", item.Confidence)
	}
	if err := s.prepareItem(&item); err != nil {
		return fmt.Errorf("memory propose: %w", err)
	}
	return s.append(event{
		Type:      eventType,
		Action:    "propose",
		ID:        item.ID,
		Actor:     s.actor(),
		Timestamp: s.now(),
		Item:      &item,
	})
}

func (s *Store) Update(_ context.Context, raw []byte) error {
	var req UpdateRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return fmt.Errorf("memory update: parse request: %w", err)
	}
	req.Action = strings.TrimSpace(strings.ToLower(req.Action))
	if req.Action == "" {
		return errors.New("memory update: action is required")
	}
	ev := event{
		Type:      eventType,
		Action:    req.Action,
		ID:        req.ID,
		Actor:     s.actor(),
		Timestamp: s.now(),
	}
	switch req.Action {
	case "approve", "reject", "delete":
		if ev.ID == "" {
			return fmt.Errorf("memory update %s: id is required", req.Action)
		}
		if err := s.requireExisting(ev.ID); err != nil {
			return fmt.Errorf("memory update %s: %w", req.Action, err)
		}
	case "upsert":
		if req.Item == nil {
			return errors.New("memory update upsert: item is required")
		}
		if req.Item.Confidence == "" {
			req.Item.Confidence = "approved"
		}
		if err := s.prepareItem(req.Item); err != nil {
			return fmt.Errorf("memory update upsert: %w", err)
		}
		ev.ID = req.Item.ID
		ev.Item = req.Item
	case "supersede":
		if ev.ID == "" || req.Item == nil {
			return errors.New("memory update supersede: id and item are required")
		}
		if err := s.requireExisting(ev.ID); err != nil {
			return fmt.Errorf("memory update supersede: %w", err)
		}
		if req.Item.Confidence == "" {
			req.Item.Confidence = "approved"
		}
		req.Item.Supersedes = append(req.Item.Supersedes, ev.ID)
		if err := s.prepareItem(req.Item); err != nil {
			return fmt.Errorf("memory update supersede: %w", err)
		}
		ev.Item = req.Item
	default:
		return fmt.Errorf("memory update: unknown action %q", req.Action)
	}
	return s.append(ev)
}

func (s *Store) Query(_ context.Context, q Query) (QueryResult, error) {
	items, err := s.fold()
	if err != nil {
		return QueryResult{}, err
	}
	allowed := allowedScopes(q.AllowedScopes)
	maxItems := q.MaxItems
	if maxItems <= 0 {
		maxItems = 8
	}
	type candidate struct {
		item   Item
		score  int
		reason string
	}
	candidates := make([]candidate, 0, len(items))
	now := s.now()
	for _, item := range items {
		if item.Confidence != "approved" || item.Sensitivity == "secret" {
			continue
		}
		if !item.ExpiresAt.IsZero() && !item.ExpiresAt.After(now) {
			continue
		}
		if !allowed[item.Scope] || !scopeMatches(item, q) {
			continue
		}
		score := relevanceScore(item, q.Prompt)
		reason := "scope match"
		if score > 0 {
			reason = "keyword match"
		}
		candidates = append(candidates, candidate{item: item, score: score, reason: reason})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		if !candidates[i].item.UpdatedAt.Equal(candidates[j].item.UpdatedAt) {
			return candidates[i].item.UpdatedAt.After(candidates[j].item.UpdatedAt)
		}
		return candidates[i].item.ID < candidates[j].item.ID
	})
	var result QueryResult
	usedBudget := 0
	for _, c := range candidates {
		if len(result.Items) >= maxItems {
			break
		}
		cost := estimateTokens(c.item)
		if q.BudgetTokens > 0 && usedBudget+cost > q.BudgetTokens {
			continue
		}
		usedBudget += cost
		result.Items = append(result.Items, RankedItem{
			Item:   c.item,
			Rank:   len(result.Items) + 1,
			Reason: c.reason,
		})
	}
	return result, nil
}

func (s *Store) List(_ context.Context) ([]Item, error) {
	items, err := s.fold()
	if err != nil {
		return nil, err
	}
	out := make([]Item, 0, len(items))
	for _, item := range items {
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].UpdatedAt.After(out[j].UpdatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (s *Store) Show(ctx context.Context, id string) (Item, bool, error) {
	items, err := s.List(ctx)
	if err != nil {
		return Item{}, false, err
	}
	for _, item := range items {
		if item.ID == id {
			return item, true, nil
		}
	}
	return Item{}, false, nil
}

func (s *Store) Export(ctx context.Context) (Export, error) {
	items, err := s.List(ctx)
	if err != nil {
		return Export{}, err
	}
	return Export{Items: items}, nil
}

func (s *Store) append(ev event) error {
	if s.Path == "" {
		return errors.New("memory store path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return fmt.Errorf("memory store: create dir: %w", err)
	}
	f, err := os.OpenFile(s.Path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("memory store: open append log: %w", err)
	}
	defer f.Close()
	data, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("memory store: encode event: %w", err)
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("memory store: append event: %w", err)
	}
	return nil
}

func (s *Store) fold() (map[string]Item, error) {
	items := make(map[string]Item)
	f, err := os.Open(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return items, nil
	}
	if err != nil {
		return nil, fmt.Errorf("memory store: open append log: %w", err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	line := 0
	for sc.Scan() {
		line++
		raw := strings.TrimSpace(sc.Text())
		if raw == "" {
			continue
		}
		var ev event
		if err := json.Unmarshal([]byte(raw), &ev); err != nil {
			return nil, fmt.Errorf("memory store: parse line %d: %w", line, err)
		}
		if ev.Type != eventType {
			continue
		}
		id := ev.ID
		if ev.Item != nil && ev.Item.ID != "" {
			id = ev.Item.ID
		}
		if id == "" {
			continue
		}
		switch ev.Action {
		case "propose", "upsert":
			if ev.Item == nil {
				return nil, fmt.Errorf("memory store: line %d %s event missing item", line, ev.Action)
			}
			items[id] = *ev.Item
		case "approve":
			item, ok := items[id]
			if !ok {
				continue
			}
			item.Confidence = "approved"
			item.UpdatedAt = ev.Timestamp
			items[id] = item
		case "reject":
			item, ok := items[id]
			if !ok {
				continue
			}
			item.Confidence = "rejected"
			item.UpdatedAt = ev.Timestamp
			items[id] = item
		case "delete":
			delete(items, id)
		case "supersede":
			old, ok := items[id]
			if ok {
				old.Confidence = "superseded"
				old.UpdatedAt = ev.Timestamp
				items[id] = old
			}
			if ev.Item != nil {
				items[ev.Item.ID] = *ev.Item
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("memory store: scan append log: %w", err)
	}
	return items, nil
}

func (s *Store) requireExisting(id string) error {
	items, err := s.fold()
	if err != nil {
		return err
	}
	if _, ok := items[id]; !ok {
		return fmt.Errorf("memory %q does not exist", id)
	}
	return nil
}

func (s *Store) prepareItem(item *Item) error {
	now := s.now()
	if item.ID == "" {
		item.ID = newID(now)
	}
	item.Scope = strings.TrimSpace(strings.ToLower(item.Scope))
	if item.Scope == "" {
		item.Scope = "repo"
	}
	switch item.Scope {
	case "global":
	case "repo":
		if strings.TrimSpace(item.RepoID) == "" {
			return errors.New("repo_id is required for repo-scoped memory")
		}
	case "session":
		if strings.TrimSpace(item.SessionID) == "" {
			return errors.New("session_id is required for session-scoped memory")
		}
	default:
		return fmt.Errorf("invalid scope %q", item.Scope)
	}
	item.Kind = strings.TrimSpace(strings.ToLower(item.Kind))
	if item.Kind == "" {
		item.Kind = "other"
	}
	item.Confidence = strings.TrimSpace(strings.ToLower(item.Confidence))
	switch item.Confidence {
	case "candidate", "approved", "rejected", "superseded":
	default:
		return fmt.Errorf("invalid confidence %q", item.Confidence)
	}
	item.Sensitivity = strings.TrimSpace(strings.ToLower(item.Sensitivity))
	if item.Sensitivity == "" {
		item.Sensitivity = "normal"
	}
	switch item.Sensitivity {
	case "normal", "private", "secret":
	default:
		return fmt.Errorf("invalid sensitivity %q", item.Sensitivity)
	}
	item.Summary = strings.TrimSpace(item.Summary)
	if item.Summary == "" {
		return errors.New("summary is required")
	}
	if item.CreatedAt.IsZero() {
		item.CreatedAt = now
	}
	item.UpdatedAt = now
	if item.Source.CreatedBy == "" {
		item.Source.CreatedBy = s.actor()
	}
	return nil
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *Store) actor() string {
	if strings.TrimSpace(s.Actor) == "" {
		return "stado"
	}
	return s.Actor
}

func allowedScopes(scopes []string) map[string]bool {
	if len(scopes) == 0 {
		return map[string]bool{"session": true, "repo": true, "global": true}
	}
	out := map[string]bool{}
	for _, scope := range scopes {
		scope = strings.TrimSpace(strings.ToLower(scope))
		if scope != "" {
			out[scope] = true
		}
	}
	return out
}

func scopeMatches(item Item, q Query) bool {
	switch item.Scope {
	case "global":
		return true
	case "repo":
		return item.RepoID != "" && item.RepoID == q.RepoID
	case "session":
		return item.SessionID != "" && item.SessionID == q.SessionID
	default:
		return false
	}
}

func relevanceScore(item Item, prompt string) int {
	terms := strings.Fields(strings.ToLower(prompt))
	if len(terms) == 0 {
		return 0
	}
	haystack := strings.ToLower(item.Summary + "\n" + item.Body + "\n" + strings.Join(item.Tags, " "))
	score := 0
	for _, term := range terms {
		term = strings.Trim(term, ".,:;!?()[]{}\"'")
		if len(term) < 3 {
			continue
		}
		if strings.Contains(haystack, term) {
			score++
		}
	}
	return score
}

func estimateTokens(item Item) int {
	n := len(item.Summary) + len(item.Body)
	for _, tag := range item.Tags {
		n += len(tag) + 1
	}
	if n == 0 {
		return 1
	}
	return (n + 3) / 4
}

func newID(now time.Time) string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("mem_%d", now.UnixNano())
	}
	return fmt.Sprintf("mem_%d_%s", now.UnixNano(), hex.EncodeToString(b[:]))
}
