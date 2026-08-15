package epdocs

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var relationshipField = regexp.MustCompile(`^(\s*)(requires|extends|supersedes|superseded-by|extended-by|see-also):\s*\[(.*?)\]\s*(?:#.*)?$`)
var relationshipKey = regexp.MustCompile(`^\s*(requires|extends|supersedes|superseded-by|extended-by|see-also):`)
var relationshipValue = regexp.MustCompile(`^"(EP-[0-9]{4})"$`)

var displayNames = map[string]string{
	"requires":      "Requires",
	"extends":       "Extends",
	"supersedes":    "Supersedes",
	"superseded-by": "Superseded by",
	"extended-by":   "Extended by",
	"see-also":      "See also",
}

func TestRelationshipLinksMatchFrontmatter(t *testing.T) {
	dir := filepath.Join("..", "..", "docs", "eps")
	paths, err := filepath.Glob(filepath.Join(dir, "[0-9][0-9][0-9][0-9]-*.md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no EP files discovered")
	}
	sort.Strings(paths)
	targets := make(map[string]string)
	for _, path := range paths {
		base := filepath.Base(path)
		if base == "0000-template.md" {
			continue
		}
		targets["EP-"+base[:4]] = "./" + base
	}

	for _, path := range paths {
		if filepath.Base(path) == "0000-template.md" {
			continue
		}
		t.Run(filepath.Base(path), func(t *testing.T) {
			checkRelationshipLinks(t, path, targets)
		})
	}
}

func TestStrongRelationshipsAreReciprocal(t *testing.T) {
	dir := filepath.Join("..", "..", "docs", "eps")
	paths, err := filepath.Glob(filepath.Join(dir, "[0-9][0-9][0-9][0-9]-*.md"))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(paths)
	type relations map[string]map[string]bool
	all := make(map[string]relations)
	for _, path := range paths {
		base := filepath.Base(path)
		if base == "0000-template.md" {
			continue
		}
		label := "EP-" + base[:4]
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		all[label] = parseRelationshipFrontmatter(t, path, string(data))
	}

	pairs := []struct {
		forward string
		reverse string
	}{
		{forward: "extends", reverse: "extended-by"},
		{forward: "supersedes", reverse: "superseded-by"},
	}
	for source, rels := range all {
		for _, pair := range pairs {
			for target := range rels[pair.forward] {
				if !all[target][pair.reverse][source] {
					t.Errorf("%s %s %s, but %s lacks reciprocal %s %s",
						source, pair.forward, target, target, pair.reverse, source)
				}
			}
		}
	}
}

func parseRelationshipFrontmatter(t *testing.T, path, data string) map[string]map[string]bool {
	t.Helper()
	out := make(map[string]map[string]bool)
	lines := strings.Split(data, "\n")
	if len(lines) == 0 || lines[0] != "---" {
		t.Fatalf("%s: missing opening frontmatter delimiter", path)
	}
	for _, line := range lines[1:] {
		if line == "---" {
			return out
		}
		match := relationshipField.FindStringSubmatch(line)
		if match == nil || match[1] != "" {
			continue
		}
		field := match[2]
		if out[field] == nil {
			out[field] = make(map[string]bool)
		}
		for _, target := range parseValues(t, match[3]) {
			out[field][target] = true
		}
	}
	t.Fatalf("%s: missing closing frontmatter delimiter", path)
	return nil
}

func TestRelationshipFieldAllowsTrailingComment(t *testing.T) {
	match := relationshipField.FindStringSubmatch(`requires: ["EP-0001"] # dependency`)
	if match == nil || match[2] != "requires" || match[3] != `"EP-0001"` {
		t.Fatalf("unexpected match: %#v", match)
	}
}

func checkRelationshipLinks(t *testing.T, path string, targets map[string]string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) == 0 || lines[0] != "---" {
		t.Fatal("missing opening frontmatter delimiter")
	}
	close := -1
	parts := make([]string, 0)
	for i := 1; i < len(lines); i++ {
		if lines[i] == "---" {
			close = i
			break
		}
		match := relationshipField.FindStringSubmatch(lines[i])
		if match == nil {
			if relationshipKey.MatchString(lines[i]) {
				t.Fatalf("malformed relationship field %q; use a YAML list of quoted EP-NNNN labels", lines[i])
			}
			continue
		}
		indent, field := match[1], match[2]
		values := parseValues(t, match[3])
		links := make([]string, 0, len(values))
		for _, label := range values {
			target, ok := targets[label]
			if !ok {
				t.Fatalf("%s references missing %s", field, label)
			}
			links = append(links, fmt.Sprintf("[%s](%s)", label, target))
		}
		if indent == "" && len(links) > 0 {
			parts = append(parts, fmt.Sprintf("**%s:** %s", displayNames[field], strings.Join(links, ", ")))
		}
	}
	if close < 0 {
		t.Fatal("missing closing frontmatter delimiter")
	}
	if len(parts) == 0 {
		for i := close + 1; i < len(lines); i++ {
			if lines[i] == "" {
				continue
			}
			if strings.HasPrefix(lines[i], "> **Relationships:**") {
				t.Fatal("rendered relationships exist without relationship frontmatter")
			}
			break
		}
		return
	}
	want := "> **Relationships:** " + strings.Join(parts, " · ")
	for i := close + 1; i < len(lines); i++ {
		if lines[i] == "" {
			continue
		}
		if lines[i] != want {
			t.Fatalf("rendered relationships differ from frontmatter\nwant: %s\n got: %s", want, lines[i])
		}
		return
	}
	t.Fatal("missing rendered relationships")
}

func parseValues(t *testing.T, payload string) []string {
	t.Helper()
	if strings.TrimSpace(payload) == "" {
		return nil
	}
	raw := strings.Split(payload, ",")
	values := make([]string, 0, len(raw))
	for _, item := range raw {
		item = strings.TrimSpace(item)
		match := relationshipValue.FindStringSubmatch(item)
		if match == nil {
			t.Fatalf("invalid relationship value %q; use a quoted EP-NNNN label", item)
		}
		values = append(values, match[1])
	}
	return values
}
