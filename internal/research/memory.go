package research

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/foobarto/stado/internal/artifacts"
)

type MemoryCorpus struct {
	Service *artifacts.Service
	Context artifacts.QueryContext
	Kinds   []artifacts.Kind
}

func (m MemoryCorpus) observe(a artifacts.Artifact, event artifacts.UsageEvent, evidence string) {
	usage, _ := m.Service.Usage(a.ID)
	for _, old := range usage {
		if old.Event == event && old.SessionID == m.Context.SessionID && old.EvidenceRef == evidence {
			return
		}
	}
	_, _ = m.Service.RecordUsage(context.Background(), artifacts.UsageObservation{ArtifactID: a.ID, ArtifactVersion: a.Version, Event: event, SessionID: m.Context.SessionID, EvidenceRef: evidence}, m.Context.Principal, "memory-research-host", "research:"+string(event)+":"+m.Context.SessionID+":"+a.ID+":"+evidence)
}

func (m MemoryCorpus) Catalog(ctx context.Context, max int) ([]CatalogItem, error) {
	_ = ctx
	items, err := m.Service.Query(artifacts.Query{Context: m.Context, Kinds: m.Kinds, ActiveOnly: true, MaxItems: max})
	if err != nil {
		return nil, err
	}
	return catalog(items), nil
}
func (m MemoryCorpus) Search(ctx context.Context, q string, max int) ([]CatalogItem, error) {
	_ = ctx
	items, err := m.Service.Query(artifacts.Query{Context: m.Context, Kinds: m.Kinds, ActiveOnly: true, MaxItems: 500})
	if err != nil {
		return nil, err
	}
	terms := strings.Fields(strings.ToLower(q))
	var scored []struct {
		a artifacts.Artifact
		n int
	}
	for _, a := range items {
		hay := strings.ToLower(a.SearchableText() + " " + strings.Join(a.Tags, " ") + " " + strings.Join(a.Groups, " "))
		n := 0
		for _, term := range terms {
			if strings.Contains(hay, term) {
				n++
			}
		}
		if n > 0 {
			scored = append(scored, struct {
				a artifacts.Artifact
				n int
			}{a, n})
		}
	}
	sort.Slice(scored, func(i, j int) bool { return scored[i].n > scored[j].n })
	if len(scored) > max {
		scored = scored[:max]
	}
	selected := make([]artifacts.Artifact, len(scored))
	for i, x := range scored {
		selected[i] = x.a
	}
	return catalog(selected), nil
}
func (m MemoryCorpus) Open(ctx context.Context, ref Ref) (Opened, error) {
	_ = ctx
	if ref.Kind != "memory" && ref.Kind != "lesson" {
		return Opened{}, errors.New("invalid memory ref kind")
	}
	a, ok, err := m.Service.Show(ref.ID)
	if err != nil || !ok {
		return Opened{}, errors.New("artifact not found")
	}
	authorized, err := m.Service.Query(artifacts.Query{Context: m.Context, Kinds: m.Kinds, ActiveOnly: true, MaxItems: 10000})
	if err != nil {
		return Opened{}, err
	}
	found := false
	for _, x := range authorized {
		if x.ID == a.ID && x.Version == ref.Version {
			found = true
			break
		}
	}
	if !found {
		return Opened{}, errors.New("artifact not found")
	}
	bodyBytes, _ := json.Marshal(a)
	actual := makeRef(a)
	if ref.Digest != "" && ref.Digest != actual.Digest {
		return Opened{}, errors.New("artifact digest mismatch")
	}
	m.observe(a, artifacts.UsageOpened, "artifact:"+actual.Digest)
	return Opened{Ref: actual, Body: string(bodyBytes)}, nil
}

func (m MemoryCorpus) ObserveCitations(_ context.Context, citations []Citation) {
	for _, citation := range citations {
		a, ok, err := m.Service.Show(citation.Ref.ID)
		if err != nil || !ok || a.Version != citation.Ref.Version {
			continue
		}
		excerptDigest := Digest([]byte(citation.Excerpt))
		m.observe(a, artifacts.UsageCited, "excerpt:"+excerptDigest)
	}
}
func catalog(items []artifacts.Artifact) []CatalogItem {
	out := make([]CatalogItem, 0, len(items))
	for _, a := range items {
		out = append(out, CatalogItem{Ref: makeRef(a), Summary: a.Title(), Tags: a.Tags, Groups: a.Groups})
	}
	return out
}
func makeRef(a artifacts.Artifact) Ref {
	b, _ := json.Marshal(a)
	return Ref{Kind: string(a.Kind), ID: a.ID, Version: a.Version, Digest: Digest(b)}
}
