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

var relationshipField = regexp.MustCompile(`^(requires|extends|supersedes|superseded-by|extended-by|see-also):\s*\[(.*)\]\s*$`)
var relationshipKey = regexp.MustCompile(`^(requires|extends|supersedes|superseded-by|extended-by|see-also):`)
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
		values := parseValues(t, match[2])
		links := make([]string, 0, len(values))
		for _, label := range values {
			target, ok := targets[label]
			if !ok {
				t.Fatalf("%s references missing %s", match[1], label)
			}
			links = append(links, fmt.Sprintf("[%s](%s)", label, target))
		}
		if len(links) > 0 {
			parts = append(parts, fmt.Sprintf("**%s:** %s", displayNames[match[1]], strings.Join(links, ", ")))
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
