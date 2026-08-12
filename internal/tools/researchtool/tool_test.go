package researchtool

import (
	"context"
	"encoding/json"
	"github.com/foobarto/stado/pkg/tool"
	"testing"
)

type host struct{ tool.Host }

func (host) Research(context.Context, string, string) (any, error) {
	return map[string]string{"answer": "cited"}, nil
}
func TestResearchToolDelegatesWithoutCorpusInParent(t *testing.T) {
	res, err := (Tool{Kind: "memory"}).Run(context.Background(), json.RawMessage(`{"query":"how"}`), host{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != `{"answer":"cited"}` {
		t.Fatalf("content=%s", res.Content)
	}
}
