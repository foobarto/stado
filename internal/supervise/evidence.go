package supervise

import (
	"context"
	"errors"
	"path"
	"strings"
)

type EvidenceSection string

const (
	EvidenceState        EvidenceSection = "state"
	EvidenceContract     EvidenceSection = "contract"
	EvidencePlan         EvidenceSection = "plan"
	EvidenceEvents       EvidenceSection = "events"
	EvidenceTranscript   EvidenceSection = "transcript"
	EvidenceTools        EvidenceSection = "tools"
	EvidenceDiff         EvidenceSection = "diff"
	EvidenceVerification EvidenceSection = "verification"
	EvidenceBudgets      EvidenceSection = "budgets"
	EvidenceChildren     EvidenceSection = "children"
	EvidenceRepository   EvidenceSection = "repository"
)

type EvidenceQuery struct {
	Section EvidenceSection `json:"section"`
	Cursor  string          `json:"cursor,omitempty"`
	Limit   int             `json:"limit,omitempty"`
	Pattern string          `json:"pattern,omitempty"`
	Path    string          `json:"path,omitempty"`
	Kinds   []string        `json:"kinds,omitempty"`
}

type EvidenceItem struct {
	ID         string            `json:"id"`
	Section    EvidenceSection   `json:"section"`
	Sequence   uint64            `json:"sequence,omitempty"`
	Summary    string            `json:"summary"`
	Content    string            `json:"content,omitempty"`
	Attributes map[string]string `json:"attributes,omitempty"`
}

type EvidencePage struct {
	AsOf       Anchor         `json:"as_of"`
	Items      []EvidenceItem `json:"items"`
	NextCursor string         `json:"next_cursor,omitempty"`
}

// EvidenceSource is the watchdog/verifier's only observational authority.
// Implementations expose filtered session-tree evidence and never mutation,
// approval, credential, shell, network, or arbitrary filesystem operations.
type EvidenceSource interface {
	Read(context.Context, EvidenceQuery) (EvidencePage, error)
	Search(context.Context, EvidenceQuery) (EvidencePage, error)
	Follow(context.Context, EvidenceQuery) (EvidencePage, error)
}

func validateEvidenceQuery(q EvidenceQuery, search bool) (EvidenceQuery, error) {
	switch q.Section {
	case EvidenceState, EvidenceContract, EvidencePlan, EvidenceEvents, EvidenceTranscript,
		EvidenceTools, EvidenceDiff, EvidenceVerification, EvidenceBudgets, EvidenceChildren, EvidenceRepository:
	default:
		return EvidenceQuery{}, errors.New("supervise: unknown evidence section")
	}
	if q.Limit == 0 {
		q.Limit = 20
	}
	if q.Limit < 1 || q.Limit > 100 {
		return EvidenceQuery{}, errors.New("supervise: evidence limit must be 1 to 100")
	}
	if len(q.Cursor) > 256 || len(q.Pattern) > 512 || len(q.Path) > 4096 || len(q.Kinds) > 16 {
		return EvidenceQuery{}, errors.New("supervise: evidence query exceeds limit")
	}
	if q.Section != EvidenceRepository && strings.TrimSpace(q.Path) != "" {
		return EvidenceQuery{}, errors.New("supervise: path is only valid for repository evidence")
	}
	if q.Section == EvidenceRepository && strings.TrimSpace(q.Path) != "" {
		clean := path.Clean(strings.ReplaceAll(strings.TrimSpace(q.Path), "\\", "/"))
		if clean == "." || clean == ".." || path.IsAbs(clean) || strings.HasPrefix(clean, "../") {
			return EvidenceQuery{}, errors.New("supervise: repository path must stay within the session tree")
		}
		q.Path = clean
	}
	if search && strings.TrimSpace(q.Pattern) == "" {
		return EvidenceQuery{}, errors.New("supervise: search pattern required")
	}
	return q, nil
}

func validateEvidencePage(page EvidencePage, expected Anchor) error {
	if page.AsOf != expected {
		return ErrStaleVerdict
	}
	if len(page.Items) > 100 || len(page.NextCursor) > 256 {
		return errors.New("supervise: evidence page exceeds limit")
	}
	for _, item := range page.Items {
		if len(item.ID) > 256 || len(item.Summary) > 4096 || len(item.Content) > 256<<10 || len(item.Attributes) > 32 {
			return errors.New("supervise: evidence item exceeds limit")
		}
	}
	return nil
}
