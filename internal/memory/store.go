package memory

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/foobarto/stado/internal/workdirpath"
)

const (
	eventType       = "memory"
	MaxPayloadBytes = 1 << 20
	MaxEventBytes   = 2 << 20
	MaxStoreBytes   = 128 << 20
	// MaxCompactBytes bounds the snapshot Compact loads into memory. Reads
	// degrade past MaxStoreBytes (a churned store can legitimately exceed it),
	// but Compact must slurp the whole log to fold it, so an absurdly large
	// (e.g. hand-tampered) file is refused rather than risking OOM.
	MaxCompactBytes = 4 * MaxStoreBytes
)

type Item struct {
	ID          string    `json:"id"`
	MemoryKind  string    `json:"memory_kind,omitempty"`
	Scope       string    `json:"scope"`
	RepoID      string    `json:"repo_id,omitempty"`
	SessionID   string    `json:"session_id,omitempty"`
	Kind        string    `json:"kind"`
	Summary     string    `json:"summary"`
	Body        string    `json:"body,omitempty"`
	Lesson      string    `json:"lesson,omitempty"`
	Trigger     string    `json:"trigger,omitempty"`
	Rationale   string    `json:"rationale,omitempty"`
	Evidence    Evidence  `json:"evidence,omitempty"`
	Source      Source    `json:"source,omitempty"`
	Confidence  string    `json:"confidence"`
	Sensitivity string    `json:"sensitivity"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	ExpiresAt   time.Time `json:"expires_at,omitempty"`
	Supersedes  []string  `json:"supersedes,omitempty"`
	Tags        []string  `json:"tags,omitempty"`
}

type Evidence struct {
	SessionID string   `json:"session_id,omitempty"`
	Turns     []int    `json:"turns,omitempty"`
	Commits   []string `json:"commits,omitempty"`
	Tests     []string `json:"tests,omitempty"`
	Files     []string `json:"files,omitempty"`
	Notes     string   `json:"notes,omitempty"`
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
	MemoryKind    string   `json:"memory_kind,omitempty"`
	// AncestorSessionIDs are the session ids this session forked from
	// (EP-15 session-scope inheritance: a session sees the session-scoped
	// memories of its ancestors). Deliberately json:"-" so it is settable
	// only by trusted in-process callers (PromptContext) — a WASM plugin's
	// query JSON can never populate it and thus cannot forge ancestry to
	// read another session tree's session-scoped memories.
	AncestorSessionIDs []string `json:"-"`
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
	// MaxBytes overrides the store-size cap. Zero means MaxStoreBytes.
	// Exists so tests can exercise the size-cap paths without writing a
	// 128MB file.
	MaxBytes int64
}

func (s *Store) maxBytes() int64 {
	if s.MaxBytes > 0 {
		return s.MaxBytes
	}
	return MaxStoreBytes
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
	if err := checkMemoryPayloadBytes("memory propose", len(raw)); err != nil {
		return err
	}
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
	// A deleted memory is a terminal tombstone (EP-0015). A propose carrying an
	// explicit id that matches a tombstone would fold it back to `candidate`
	// (foldEvents replaces the entry wholesale), and a following approve — whose
	// guard now sees `candidate`, not `deleted` — would launder it into an
	// approved, prompt-injectable memory. Reachable from the plugin
	// memory:propose host bridge with an attacker-controlled payload. Refuse it
	// before prepareItem auto-assigns a fresh id (an empty id is a new memory
	// and is allowed). Re-proposing similar content must use a fresh id.
	if err := s.refuseDeletedTombstone("propose", item.ID); err != nil {
		return err
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
	if err := checkMemoryPayloadBytes("memory update", len(raw)); err != nil {
		return err
	}
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
		existing, err := s.requireExistingItem(ev.ID)
		if err != nil {
			return fmt.Errorf("memory update %s: %w", req.Action, err)
		}
		// A `deleted` item is a terminal audit tombstone: neither approve nor
		// reject may transition it. Blocking only approve leaves a laundering
		// path — delete→reject flips the tombstone to `rejected`, after which
		// approve resurrects it into a queryable, prompt-injectable memory,
		// defeating the guard. reject is a candidate-review transition, not a
		// way to un-delete; a tombstone must stay terminal under any sequence.
		// (Re-propose to bring a deleted memory back, which writes a fresh
		// audit trail.)
		if (req.Action == "approve" || req.Action == "reject") && existing.Confidence == "deleted" {
			return fmt.Errorf("memory update %s: %q is deleted; re-propose it instead of resurrecting a tombstone", req.Action, ev.ID)
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
		// A `deleted` item is a terminal audit tombstone. Without this guard an
		// upsert over a deleted id replaces the tombstone in the folded map with
		// a fresh (default-approved) item, laundering it back into a queryable,
		// prompt-injectable memory — the same defeat the approve/reject guard
		// blocks. An upsert for a new (non-deleted, or absent) id is unaffected.
		// (Re-propose to bring a deleted memory back, which writes a fresh audit
		// trail.)
		if err := s.refuseDeletedTombstone("update upsert", req.Item.ID); err != nil {
			return err
		}
		ev.ID = req.Item.ID
		ev.Item = req.Item
	case "edit":
		if ev.ID == "" || req.Item == nil {
			return errors.New("memory update edit: id and item are required")
		}
		existing, err := s.requireExistingItem(ev.ID)
		if err != nil {
			return fmt.Errorf("memory update edit: %w", err)
		}
		// Same tombstone-laundering guard as upsert: editing a deleted id at the
		// store level would otherwise rewrite the tombstone into a queryable
		// memory (the CLI edit path preserves the `deleted` confidence, but a raw
		// store edit can carry any confidence). Refuse it; re-propose instead.
		if existing.Confidence == "deleted" {
			return fmt.Errorf("memory update edit: %q is deleted; re-propose it instead of resurrecting a tombstone", ev.ID)
		}
		if req.Item.ID == "" {
			req.Item.ID = ev.ID
		}
		if req.Item.ID != ev.ID {
			return fmt.Errorf("memory update edit: item id %q does not match %q", req.Item.ID, ev.ID)
		}
		if req.Item.CreatedAt.IsZero() {
			req.Item.CreatedAt = existing.CreatedAt
		}
		if req.Item.Source == (Source{}) {
			req.Item.Source = existing.Source
		}
		if err := s.prepareItem(req.Item); err != nil {
			return fmt.Errorf("memory update edit: %w", err)
		}
		ev.Item = req.Item
	case "supersede":
		if ev.ID == "" || req.Item == nil {
			return errors.New("memory update supersede: id and item are required")
		}
		existing, err := s.requireExistingItem(ev.ID)
		if err != nil {
			return fmt.Errorf("memory update supersede: %w", err)
		}
		if existing.Confidence != "approved" {
			return fmt.Errorf("memory update supersede: only approved memories can be superseded, got %q", existing.Confidence)
		}
		if req.Item.Confidence == "" {
			req.Item.Confidence = "approved"
		}
		if req.Item.Confidence != "approved" {
			return fmt.Errorf("memory update supersede: replacement confidence must be approved, got %q", req.Item.Confidence)
		}
		req.Item.Supersedes = append(req.Item.Supersedes, ev.ID)
		if err := s.prepareItem(req.Item); err != nil {
			return fmt.Errorf("memory update supersede: %w", err)
		}
		if req.Item.ID == ev.ID {
			return errors.New("memory update supersede: replacement id must differ from superseded id")
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
	memoryKind := strings.TrimSpace(strings.ToLower(q.MemoryKind))
	if memoryKind == "" {
		memoryKind = "memory"
	}
	switch memoryKind {
	case "memory", "lesson":
	default:
		return QueryResult{}, fmt.Errorf("memory query: invalid memory_kind %q", q.MemoryKind)
	}
	for _, item := range items {
		if item.Confidence != "approved" || item.Sensitivity == "secret" {
			continue
		}
		if itemMemoryKind(item) != memoryKind {
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

// CompactResult summarises a Compact run for human/JSON reporting.
type CompactResult struct {
	EventsBefore int   `json:"events_before"`
	ItemsAfter   int   `json:"items_after"`
	BytesBefore  int64 `json:"bytes_before"`
	BytesAfter   int64 `json:"bytes_after"`
}

// Compact rewrites the append-only log to its folded state: one upsert event
// per live item (tombstones included), collapsing the redundant event history
// that accumulates from edit/approve/reject/supersede churn. The active set is
// unchanged — only the on-disk representation shrinks. This is the recovery
// path for a store that has grown past the size cap (reads degrade gracefully,
// but writes stay refused until the log is compacted back under the cap).
//
// The rewrite is atomic (temp file + rename within the store dir). Events
// appended concurrently during the fold are caught up before the swap, and a
// final size check fails the compact closed if the log grew again — so a racing
// write aborts the compaction (retry when quiescent) rather than being silently
// dropped. The store uses no cross-process lock, matching the rest of the
// codebase.
func (s *Store) Compact(_ context.Context) (CompactResult, error) {
	root, name, err := s.storeRoot(true)
	if err != nil {
		return CompactResult{}, err
	}
	defer func() { _ = root.Close() }()

	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return CompactResult{}, nil // nothing to compact
	}
	if err != nil {
		return CompactResult{}, fmt.Errorf("memory store: stat append log: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return CompactResult{}, fmt.Errorf("memory store is a symlink: %s", s.Path)
	}
	if !info.Mode().IsRegular() {
		return CompactResult{}, fmt.Errorf("memory store is not a regular file: %s", s.Path)
	}
	if info.Size() > MaxCompactBytes {
		return CompactResult{}, fmt.Errorf("memory store is %d bytes, over the %d compaction ceiling: %s — trim the log manually before compacting", info.Size(), MaxCompactBytes, s.Path)
	}

	data, err := readRootFile(root, name)
	if err != nil {
		return CompactResult{}, err
	}
	capturedLen := int64(len(data))
	items, err := foldEvents(bytes.NewReader(data), s.Path)
	if err != nil {
		return CompactResult{}, err
	}

	ids := make([]string, 0, len(items))
	for id := range items {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	var buf bytes.Buffer
	for _, id := range ids {
		item := items[id]
		ev := event{
			Type:      eventType,
			Action:    "upsert",
			ID:        id,
			Actor:     s.actor(),
			Timestamp: item.UpdatedAt,
			Item:      &item,
		}
		encoded, err := json.Marshal(ev)
		if err != nil {
			return CompactResult{}, fmt.Errorf("memory store: encode compacted event: %w", err)
		}
		buf.Write(encoded)
		buf.WriteByte('\n')
	}

	// Catch up any events appended during the fold so they survive the swap.
	incorporated := capturedLen
	if info2, err := root.Lstat(name); err == nil && info2.Mode().IsRegular() && info2.Size() > capturedLen {
		tail, err := readRootFileFrom(root, name, capturedLen)
		if err != nil {
			return CompactResult{}, fmt.Errorf("memory store: read concurrent appends: %w", err)
		}
		buf.Write(tail)
		incorporated += int64(len(tail))
	}

	tmp := name + ".compact-tmp"
	_ = root.Remove(tmp) // clear a stale temp from a previously interrupted compact
	tf, err := root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return CompactResult{}, fmt.Errorf("memory store: create compacted temp: %w", err)
	}
	if _, err := tf.Write(buf.Bytes()); err != nil {
		_ = tf.Close()
		_ = root.Remove(tmp)
		return CompactResult{}, fmt.Errorf("memory store: write compacted temp: %w", err)
	}
	if err := tf.Sync(); err != nil {
		_ = tf.Close()
		_ = root.Remove(tmp)
		return CompactResult{}, fmt.Errorf("memory store: sync compacted temp: %w", err)
	}
	if err := tf.Close(); err != nil {
		_ = root.Remove(tmp)
		return CompactResult{}, fmt.Errorf("memory store: close compacted temp: %w", err)
	}
	// Fail closed: if the log grew (or was swapped) since the catch-up read,
	// abort the swap rather than silently dropping the new events. This shrinks
	// the data-loss window to the Lstat→Rename gap below and turns the
	// remaining race into a safe retry instead of lost data. The store uses no
	// cross-process lock (matching the rest of the codebase), so the operator
	// retries compact when no writer is active.
	if info3, err := root.Lstat(name); err != nil || !info3.Mode().IsRegular() || info3.Size() != incorporated {
		_ = root.Remove(tmp)
		size := int64(-1)
		if err == nil {
			size = info3.Size()
		}
		return CompactResult{}, fmt.Errorf("memory store: log changed during compact (size %d, expected %d): %s — retry when no writer is active", size, incorporated, s.Path)
	}
	if err := root.Rename(tmp, name); err != nil {
		_ = root.Remove(tmp)
		return CompactResult{}, fmt.Errorf("memory store: replace append log: %w", err)
	}

	return CompactResult{
		EventsBefore: bytes.Count(data, []byte{'\n'}),
		ItemsAfter:   len(ids),
		BytesBefore:  capturedLen,
		BytesAfter:   int64(buf.Len()),
	}, nil
}

// readAllBounded reads at most MaxCompactBytes from r, erroring if the source
// exceeds it. The Compact size check (info.Size()) and the read are not atomic,
// so the log can grow between them; this bounds the in-memory snapshot
// regardless, keeping the OOM guard honest under concurrent/manual appends.
func readAllBounded(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxCompactBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > MaxCompactBytes {
		return nil, fmt.Errorf("memory store exceeds %d bytes during compaction read", MaxCompactBytes)
	}
	return data, nil
}

// readRootFile reads an entire file inside the store's os.Root sandbox, bounded
// by MaxCompactBytes.
func readRootFile(root *os.Root, name string) ([]byte, error) {
	f, err := root.Open(name)
	if err != nil {
		return nil, fmt.Errorf("memory store: open append log: %w", err)
	}
	defer f.Close()
	data, err := readAllBounded(f)
	if err != nil {
		return nil, fmt.Errorf("memory store: read append log: %w", err)
	}
	return data, nil
}

// readRootFileFrom reads a file from byte offset off to EOF inside the sandbox,
// bounded by MaxCompactBytes.
func readRootFileFrom(root *os.Root, name string, off int64) ([]byte, error) {
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return nil, err
	}
	return readAllBounded(f)
}

func (s *Store) append(ev event) error {
	root, name, err := s.storeRoot(true)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()

	data, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("memory store: encode event: %w", err)
	}
	appendBytes := int64(len(data) + 1)
	if appendBytes > MaxEventBytes {
		return fmt.Errorf("memory store event exceeds %d bytes", MaxEventBytes)
	}
	f, err := openMemoryAppendFile(root, name, appendBytes, s.maxBytes())
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("memory store: append event: %w", err)
	}
	if _, err := f.Write([]byte{'\n'}); err != nil {
		return fmt.Errorf("memory store: append event: %w", err)
	}
	return nil
}

func (s *Store) fold() (map[string]Item, error) {
	items := make(map[string]Item)
	root, name, err := s.storeRoot(false)
	if errors.Is(err, os.ErrNotExist) {
		return items, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()

	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return items, nil
	}
	if err != nil {
		return nil, fmt.Errorf("memory store: stat append log: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("memory store is a symlink: %s", s.Path)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("memory store is not a regular file: %s", s.Path)
	}
	// Past the size cap we WARN and read anyway rather than erroring: a store
	// that has grown beyond the cap must still be readable so `export`,
	// `list`, and `compact` (the recovery surface) keep working. Writes are
	// still refused (openMemoryAppendFile) so growth stays bounded until
	// `compact` rewrites the log to its folded state.
	if info.Size() > s.maxBytes() {
		fmt.Fprintf(os.Stderr, "memory store: %s is %d bytes, over the %d cap — reading anyway; run `stado memory compact`\n", s.Path, info.Size(), s.maxBytes())
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, fmt.Errorf("memory store: open append log: %w", err)
	}
	defer f.Close()
	openedInfo, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("memory store: stat append log: %w", err)
	}
	if !openedInfo.Mode().IsRegular() {
		return nil, fmt.Errorf("memory store is not a regular file: %s", s.Path)
	}
	if !os.SameFile(info, openedInfo) {
		return nil, fmt.Errorf("memory store changed while opening: %s", s.Path)
	}
	return foldEvents(f, s.Path)
}

// foldEvents replays an append-only memory log into the current folded item
// set. A single malformed or structurally-invalid line is skipped (R8) rather
// than bricking the whole store — every memory/learning command, including the
// recovery surface, must keep working in the presence of one bad line. The
// running skip count is reported once at the end. Extracted from fold so
// Compact can fold an in-memory snapshot of the log without re-reading it.
func foldEvents(r io.Reader, path string) (map[string]Item, error) {
	items := make(map[string]Item)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), MaxEventBytes)
	line := 0
	skipped := 0
	for sc.Scan() {
		line++
		raw := strings.TrimSpace(sc.Text())
		if raw == "" {
			continue
		}
		var ev event
		if err := json.Unmarshal([]byte(raw), &ev); err != nil {
			// R8: a single malformed line must not brick the whole store
			// (and with it every memory + learning command, including the
			// recovery surface). Skip it and keep folding the rest.
			fmt.Fprintf(os.Stderr, "memory store: skipping malformed line %d in %s: %v\n", line, path, err)
			skipped++
			continue
		}
		if ev.Type != eventType {
			continue
		}
		id := ev.ID
		if ev.Action != "supersede" && ev.Item != nil && ev.Item.ID != "" {
			id = ev.Item.ID
		}
		if id == "" {
			continue
		}
		switch ev.Action {
		case "propose", "upsert", "edit":
			if ev.Item == nil {
				// Structurally invalid event — skip rather than abort (R8).
				fmt.Fprintf(os.Stderr, "memory store: skipping line %d (%s event missing item) in %s\n", line, ev.Action, path)
				skipped++
				continue
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
			// EP-15 defense: deletion hides an item from retrieval but keeps an
			// audit tombstone (confidence="deleted"), consistent with
			// reject/supersede, so `list`, `show`, and `export` still surface
			// it as deleted. Query excludes it (approved-only). A delete for an
			// unknown id is a no-op.
			item, ok := items[id]
			if !ok {
				continue
			}
			item.Confidence = "deleted"
			item.UpdatedAt = ev.Timestamp
			items[id] = item
		case "supersede":
			// R8: don't tombstone the old entry unless the replacement is
			// present — a nil-item supersede (truncated write / hand-edit)
			// would otherwise destroy data and drop the replacement silently.
			if ev.Item == nil {
				fmt.Fprintf(os.Stderr, "memory store: skipping line %d (supersede event missing item) in %s\n", line, path)
				skipped++
				continue
			}
			if old, ok := items[id]; ok {
				old.Confidence = "superseded"
				old.UpdatedAt = ev.Timestamp
				items[id] = old
			}
			items[ev.Item.ID] = *ev.Item
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("memory store: scan append log: %w", err)
	}
	if skipped > 0 {
		fmt.Fprintf(os.Stderr, "memory store: %d malformed line(s) skipped in %s — manual inspection recommended\n", skipped, path)
	}
	return items, nil
}

func checkMemoryPayloadBytes(op string, n int) error {
	if n > MaxPayloadBytes {
		return fmt.Errorf("%s payload exceeds %d bytes", op, MaxPayloadBytes)
	}
	return nil
}

func openMemoryAppendFile(root *os.Root, name string, appendBytes, maxBytes int64) (*os.File, error) {
	if appendBytes > maxBytes {
		return nil, fmt.Errorf("memory store exceeds %d bytes: %s", maxBytes, name)
	}
	for range 2 {
		info, err := root.Lstat(name)
		switch {
		case errors.Is(err, os.ErrNotExist):
			f, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_APPEND|os.O_WRONLY, 0o600)
			if errors.Is(err, os.ErrExist) {
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("memory store: open append log: %w", err)
			}
			return f, nil
		case err != nil:
			return nil, fmt.Errorf("memory store: stat append log: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("memory store is a symlink: %s", name)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("memory store is not a regular file: %s", name)
		}
		if info.Size()+appendBytes > maxBytes {
			return nil, fmt.Errorf("memory store exceeds %d bytes: %s", maxBytes, name)
		}
		f, err := root.OpenFile(name, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, fmt.Errorf("memory store: open append log: %w", err)
		}
		openedInfo, err := f.Stat()
		if err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("memory store: stat append log: %w", err)
		}
		if !openedInfo.Mode().IsRegular() {
			_ = f.Close()
			return nil, fmt.Errorf("memory store is not a regular file: %s", name)
		}
		if !os.SameFile(info, openedInfo) {
			_ = f.Close()
			return nil, fmt.Errorf("memory store changed while opening: %s", name)
		}
		if openedInfo.Size()+appendBytes > maxBytes {
			_ = f.Close()
			return nil, fmt.Errorf("memory store exceeds %d bytes: %s", maxBytes, name)
		}
		return f, nil
	}
	return nil, fmt.Errorf("memory store changed while opening: %s", name)
}

func (s *Store) storeRoot(createDir bool) (*os.Root, string, error) {
	if strings.TrimSpace(s.Path) == "" {
		return nil, "", errors.New("memory store path is empty")
	}
	dir := filepath.Dir(s.Path)
	name := filepath.Base(s.Path)
	if name == "." || name == ".." || name == string(filepath.Separator) || strings.Contains(name, "\x00") {
		return nil, "", fmt.Errorf("invalid memory store path: %s", s.Path)
	}
	uc := workdirpath.NewUserConfigResolver()
	if createDir {
		if err := uc.MkdirAll(dir, 0o700); err != nil {
			return nil, "", fmt.Errorf("memory store: create dir: %w", err)
		}
	}
	root, err := uc.OpenRoot(dir)
	if err != nil {
		return nil, "", fmt.Errorf("memory store: open dir: %w", err)
	}
	return root, name, nil
}

func (s *Store) requireExistingItem(id string) (Item, error) {
	items, err := s.fold()
	if err != nil {
		return Item{}, err
	}
	item, ok := items[id]
	if !ok {
		return Item{}, fmt.Errorf("memory %q does not exist", id)
	}
	return item, nil
}

// refuseDeletedTombstone rejects an operation whose target id is an existing
// `deleted` tombstone, keeping the tombstone terminal. An absent id is allowed
// (propose/upsert may legitimately create a new memory). op is the full
// operation label for the error message (e.g. "update upsert", "propose"). The
// message mirrors the approve/reject guard so every resurrection path surfaces
// the same "is deleted" remediation.
func (s *Store) refuseDeletedTombstone(op, id string) error {
	if id == "" {
		return nil
	}
	items, err := s.fold()
	if err != nil {
		// A store that can't be folded can't be laundered: the resurrected
		// entry would be unqueryable (Query folds too), and the downstream
		// append surfaces the real error (e.g. the size-cap rejection). Defer
		// rather than preempt that error with a fold/scan failure here.
		return nil //nolint:nilerr // intentional: see comment.
	}
	if existing, ok := items[id]; ok && existing.Confidence == "deleted" {
		return fmt.Errorf("memory %s: %q is deleted; re-propose with a fresh id instead of resurrecting a tombstone", op, id)
	}
	return nil
}

func (s *Store) prepareItem(item *Item) error {
	now := s.now()
	item.MemoryKind = strings.TrimSpace(strings.ToLower(item.MemoryKind))
	switch item.MemoryKind {
	case "", "memory", "lesson":
	default:
		return fmt.Errorf("invalid memory_kind %q", item.MemoryKind)
	}
	if item.ID == "" {
		item.ID = newID(now, itemIDPrefix(item.MemoryKind))
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
		if item.MemoryKind == "lesson" {
			item.Kind = "lesson"
		} else {
			item.Kind = "other"
		}
	}
	item.Confidence = strings.TrimSpace(strings.ToLower(item.Confidence))
	switch item.Confidence {
	case "candidate", "approved", "rejected", "superseded", "deleted":
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
	if item.MemoryKind == "lesson" {
		if err := prepareLesson(item); err != nil {
			return err
		}
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

func prepareLesson(item *Item) error {
	item.Body = strings.TrimSpace(item.Body)
	item.Lesson = strings.TrimSpace(item.Lesson)
	if item.Lesson == "" && item.Body != "" {
		item.Lesson = item.Body
	}
	item.Trigger = strings.TrimSpace(item.Trigger)
	item.Rationale = strings.TrimSpace(item.Rationale)
	item.Evidence.Notes = strings.TrimSpace(item.Evidence.Notes)
	if item.Lesson == "" {
		return errors.New("lesson is required for lesson memory")
	}
	if item.Trigger == "" {
		return errors.New("trigger is required for lesson memory")
	}
	item.Body = item.Lesson
	if item.Evidence.empty() {
		return errors.New("evidence is required for lesson memory")
	}
	return nil
}

func (e Evidence) empty() bool {
	return e.SessionID == "" &&
		len(e.Turns) == 0 &&
		len(e.Commits) == 0 &&
		len(e.Tests) == 0 &&
		len(e.Files) == 0 &&
		strings.TrimSpace(e.Notes) == ""
}

func IsLesson(item Item) bool {
	return itemMemoryKind(item) == "lesson"
}

func itemMemoryKind(item Item) string {
	switch strings.TrimSpace(strings.ToLower(item.MemoryKind)) {
	case "lesson":
		return "lesson"
	}
	return "memory"
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
		// EP-15: a session memory applies to its own session AND every session
		// that forked from it (the querying session's ancestors). Exact match
		// is the self case; ancestry covers descendants reaching back up the
		// fork tree. AncestorSessionIDs is populated only by trusted in-process
		// callers (never plugin query JSON), so this cannot be forged.
		if item.SessionID == "" {
			return false
		}
		if item.SessionID == q.SessionID {
			return true
		}
		for _, ancestor := range q.AncestorSessionIDs {
			if item.SessionID == ancestor {
				return true
			}
		}
		return false
	default:
		return false
	}
}

func relevanceScore(item Item, prompt string) int {
	terms := strings.Fields(strings.ToLower(prompt))
	if len(terms) == 0 {
		return 0
	}
	haystack := strings.ToLower(strings.Join([]string{
		item.Summary,
		item.Body,
		item.Lesson,
		item.Trigger,
		item.Rationale,
		strings.Join(item.Tags, " "),
		evidenceText(item.Evidence),
	}, "\n"))
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
	n := len(item.Summary)
	if itemMemoryKind(item) == "lesson" {
		lessonText := item.Lesson
		if strings.TrimSpace(lessonText) == "" {
			lessonText = item.Body
		}
		n += len(lessonText) + len(item.Trigger) + len(item.Rationale)
	} else {
		n += len(item.Body)
	}
	for _, tag := range item.Tags {
		n += len(tag) + 1
	}
	if n == 0 {
		return 1
	}
	return (n + 3) / 4
}

func evidenceText(e Evidence) string {
	var parts []string
	if e.SessionID != "" {
		parts = append(parts, e.SessionID)
	}
	for _, turn := range e.Turns {
		parts = append(parts, fmt.Sprint(turn))
	}
	parts = append(parts, e.Commits...)
	parts = append(parts, e.Tests...)
	parts = append(parts, e.Files...)
	if e.Notes != "" {
		parts = append(parts, e.Notes)
	}
	return strings.Join(parts, " ")
}

func itemIDPrefix(memoryKind string) string {
	if memoryKind == "lesson" {
		return "lesson"
	}
	return "mem"
}

func newID(now time.Time, prefix string) string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%s_%d", prefix, now.UnixNano())
	}
	return fmt.Sprintf("%s_%d_%s", prefix, now.UnixNano(), hex.EncodeToString(b[:]))
}
