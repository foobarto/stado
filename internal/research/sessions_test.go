package research

import (
	"context"
	"testing"

	"github.com/foobarto/stado/internal/runtime"
	"github.com/foobarto/stado/pkg/agent"
)

func TestSessionCorpusAuthorizedSearchAndBoundedOpen(t *testing.T) {
	allowed := t.TempDir()
	denied := t.TempDir()
	for _, x := range []struct {
		path string
		text string
	}{{allowed, "We decided to rebuild SQLite from the WAL."}, {denied, "unrelated secret decision"}} {
		if err := runtime.AppendMessage(x.path, agent.Text(agent.RoleUser, x.text)); err != nil {
			t.Fatal(err)
		}
	}
	c := SessionCorpus{Authorized: map[string]string{"allowed": allowed}, MaxMessages: 5}
	hits, err := c.Search(context.Background(), "SQLite", 10)
	if err != nil || len(hits) != 1 || hits[0].Ref.ID != "allowed" {
		t.Fatalf("hits=%v err=%v", hits, err)
	}
	opened, err := c.Open(context.Background(), hits[0].Ref)
	if err != nil {
		t.Fatal(err)
	}
	if opened.Ref.Locator != "messages:0-1" || opened.Body == "" {
		t.Fatalf("opened=%+v", opened)
	}
	if _, err := c.Open(context.Background(), Ref{Kind: "session", ID: "denied"}); err == nil {
		t.Fatal("unauthorized session opened")
	}
}

func TestSessionCorpusRejectsStaleConversationDigest(t *testing.T) {
	path := t.TempDir()
	if err := runtime.AppendMessage(path, agent.Text(agent.RoleUser, "first")); err != nil {
		t.Fatal(err)
	}
	c := SessionCorpus{Authorized: map[string]string{"s": path}}
	cat, err := c.Catalog(context.Background(), 10)
	if err != nil || len(cat) != 1 {
		t.Fatal(err)
	}
	if err := runtime.AppendMessage(path, agent.Text(agent.RoleAssistant, "changed")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Open(context.Background(), cat[0].Ref); err == nil {
		t.Fatal("stale digest accepted")
	}
}
