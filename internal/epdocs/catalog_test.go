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

type epFrontmatter struct {
	EP     string
	Title  string
	Status string
	Type   string
}

type catalogueEntry struct {
	Number string
	Title  string
	Link   string
	Type   string
	Status string
}

var catalogueRow = regexp.MustCompile(`^\| ([0-9]{4}) \| \[([^]]+)\]\(([^)]+)\) \| ([^|]+) \| ([^|]+) \|$`)
var topLevelEPLink = regexp.MustCompile(`\[EP-([0-9]{4})\]\((docs/eps/[^)#]+\.md)\)`)

func TestCatalogueMatchesEPFrontmatter(t *testing.T) {
	eps := loadEPFrontmatter(t)
	entries := loadCatalogue(t)

	if len(entries) != len(eps) {
		t.Errorf("EP catalogue has %d entries for %d proposal files", len(entries), len(eps))
	}
	seen := make(map[string]bool, len(entries))
	previous := ""
	for _, entry := range entries {
		if previous != "" && entry.Number <= previous {
			t.Errorf("EP catalogue is not in strict numerical order: EP-%s follows EP-%s", entry.Number, previous)
		}
		previous = entry.Number
		path, ok := eps[entry.Number]
		if !ok {
			t.Errorf("catalogue entry EP-%s has no proposal file", entry.Number)
			continue
		}
		if seen[entry.Number] {
			t.Errorf("EP-%s appears more than once in the catalogue", entry.Number)
			continue
		}
		seen[entry.Number] = true
		wantLink := "./" + filepath.Base(path.path)
		if entry.Title != path.meta.Title {
			t.Errorf("EP-%s catalogue title = %q; frontmatter title = %q", entry.Number, entry.Title, path.meta.Title)
		}
		if entry.Status != path.meta.Status {
			t.Errorf("EP-%s catalogue status = %q; frontmatter status = %q", entry.Number, entry.Status, path.meta.Status)
		}
		if entry.Type != path.meta.Type {
			t.Errorf("EP-%s catalogue type = %q; frontmatter type = %q", entry.Number, entry.Type, path.meta.Type)
		}
		if entry.Link != wantLink {
			t.Errorf("EP-%s catalogue link = %q; want %q", entry.Number, entry.Link, wantLink)
		}
	}
	for number := range eps {
		if !seen[number] {
			t.Errorf("EP-%s is missing from docs/eps/README.md", number)
		}
	}
}

func TestLiveStandardsEPsAreIntegrated(t *testing.T) {
	eps := loadEPFrontmatter(t)
	design := readRepoFile(t, "DESIGN.md")
	plan := readRepoFile(t, "PLAN.md")

	for number, doc := range eps {
		if doc.meta.Type != "Standards" || !isLiveStatus(doc.meta.Status) {
			continue
		}
		label := "EP-" + number
		if !strings.Contains(design, label) && !strings.Contains(plan, label) {
			t.Errorf("%s is %s but is referenced by neither DESIGN.md nor PLAN.md", label, doc.meta.Status)
		}
		if (doc.meta.Status == "Accepted" || doc.meta.Status == "Partial") && !strings.Contains(plan, label) {
			t.Errorf("%s is %s and must remain explicitly referenced in PLAN.md", label, doc.meta.Status)
		}
	}
}

func TestTopLevelEPLinksUseCanonicalTargets(t *testing.T) {
	eps := loadEPFrontmatter(t)
	for _, name := range []string{"DESIGN.md", "PLAN.md"} {
		data := readRepoFile(t, name)
		for _, match := range topLevelEPLink.FindAllStringSubmatch(data, -1) {
			doc, ok := eps[match[1]]
			if !ok {
				t.Errorf("%s links missing EP-%s", name, match[1])
				continue
			}
			want := filepath.ToSlash(strings.TrimPrefix(doc.path, filepath.Join("..", "..")+string(filepath.Separator)))
			if match[2] != want {
				t.Errorf("%s link for EP-%s targets %q; want canonical %q", name, match[1], match[2], want)
			}
		}
	}
}

type epDocument struct {
	path string
	meta epFrontmatter
}

func loadEPFrontmatter(t *testing.T) map[string]epDocument {
	t.Helper()
	root := filepath.Join("..", "..")
	paths, err := filepath.Glob(filepath.Join(root, "docs", "eps", "[0-9][0-9][0-9][0-9]-*.md"))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(paths)
	docs := make(map[string]epDocument, len(paths))
	for _, path := range paths {
		if filepath.Base(path) == "0000-template.md" {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		frontmatter, err := frontmatterPayload(data)
		if err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		meta, err := parseEPFrontmatter(frontmatter)
		if err != nil {
			t.Errorf("%s: parse frontmatter: %v", path, err)
			continue
		}
		number := filepath.Base(path)[:4]
		if strings.TrimLeft(meta.EP, "0") != strings.TrimLeft(number, "0") {
			t.Errorf("%s: frontmatter ep is %q; want %s", path, meta.EP, number)
			continue
		}
		if _, exists := docs[number]; exists {
			t.Errorf("duplicate EP-%s", number)
			continue
		}
		docs[number] = epDocument{path: path, meta: meta}
	}
	if len(docs) == 0 {
		t.Fatal("no EP files discovered")
	}
	return docs
}

func parseEPFrontmatter(data []byte) (epFrontmatter, error) {
	var meta epFrontmatter
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			continue
		}
		key, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		value = strings.TrimSpace(value)
		switch key {
		case "ep":
			meta.EP = value
		case "title":
			meta.Title = value
		case "status":
			meta.Status = value
		case "type":
			meta.Type = value
		}
	}
	if meta.EP == "" || meta.Title == "" || meta.Status == "" || meta.Type == "" {
		return epFrontmatter{}, fmt.Errorf("required ep/title/status/type field missing")
	}
	return meta, nil
}

func frontmatterPayload(data []byte) ([]byte, error) {
	text := string(data)
	if !strings.HasPrefix(text, "---\n") {
		return nil, fmt.Errorf("missing opening frontmatter delimiter")
	}
	end := strings.Index(text[4:], "\n---\n")
	if end < 0 {
		return nil, fmt.Errorf("missing closing frontmatter delimiter")
	}
	return []byte(text[4 : 4+end]), nil
}

func loadCatalogue(t *testing.T) []catalogueEntry {
	t.Helper()
	data := readRepoFile(t, filepath.Join("docs", "eps", "README.md"))
	lines := strings.Split(data, "\n")
	inIndex := false
	entries := make([]catalogueEntry, 0)
	for _, line := range lines {
		if line == "## Index" {
			inIndex = true
			continue
		}
		if inIndex && strings.HasPrefix(line, "## ") {
			break
		}
		if !inIndex || !strings.HasPrefix(line, "|") || strings.HasPrefix(line, "| #") || strings.HasPrefix(line, "|-") {
			continue
		}
		match := catalogueRow.FindStringSubmatch(line)
		if match == nil {
			t.Errorf("malformed EP catalogue row: %q", line)
			continue
		}
		entries = append(entries, catalogueEntry{
			Number: match[1],
			Title:  match[2],
			Link:   match[3],
			Type:   strings.TrimSpace(match[4]),
			Status: strings.TrimSpace(match[5]),
		})
	}
	if !inIndex {
		t.Fatal("docs/eps/README.md has no Index section")
	}
	return entries
}

func isLiveStatus(status string) bool {
	switch status {
	case "Accepted", "Partial", "Implemented":
		return true
	default:
		return false
	}
}

func readRepoFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", path))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
