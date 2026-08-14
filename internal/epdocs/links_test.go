package epdocs

import (
	"html"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

var (
	inlineMarkdownLink  = regexp.MustCompile(`!?\[[^]]*\]\(([^\s)]+)(?:\s+[^)]*)?\)`)
	referenceLinkTarget = regexp.MustCompile(`(?m)^\s*\[[^]]+\]:\s*(\S+)`)
	atxHeading          = regexp.MustCompile(`^ {0,3}#{1,6}[ \t]+(.+?)[ \t]*#*[ \t]*$`)
	setextHeading       = regexp.MustCompile(`^ {0,3}(?:=+|-+)[ \t]*$`)
	markdownLabel       = regexp.MustCompile(`!?\[([^]]+)\]\([^)]*\)`)
	htmlTag             = regexp.MustCompile(`<[^>]+>`)
	explicitAnchor      = regexp.MustCompile(`(?i)<(?:a\s+(?:[^>]*\s)?name|[^>]+\sid)=["']([^"']+)["']`)
)

func TestRepositoryMarkdownLinksResolve(t *testing.T) {
	root := filepath.Join("..", "..")
	files := repositoryMarkdownFiles(t, root)
	anchorCache := make(map[string]map[string]bool)
	for _, source := range files {
		data, err := os.ReadFile(source)
		if err != nil {
			t.Error(err)
			continue
		}
		content := withoutFencedCode(string(data))
		targets := make([]string, 0)
		for _, match := range inlineMarkdownLink.FindAllStringSubmatch(content, -1) {
			targets = append(targets, match[1])
		}
		for _, match := range referenceLinkTarget.FindAllStringSubmatch(content, -1) {
			targets = append(targets, match[1])
		}
		for _, target := range targets {
			checkMarkdownTarget(t, root, source, target, anchorCache)
		}
	}
}

func TestGitHubHeadingSlugCompatibility(t *testing.T) {
	cases := map[string]string{
		"Current corrective release: PR #257 and v0.80.0": "current-corrective-release-pr-257-and-v0800",
		"Configuring tools & sandboxing":                  "configuring-tools--sandboxing",
		"Offline / airgap":                                "offline--airgap",
		"Provider layer":                                  "provider-layer",
	}
	for heading, want := range cases {
		if got := githubHeadingSlug(heading); got != want {
			t.Errorf("githubHeadingSlug(%q) = %q; want %q", heading, got, want)
		}
	}
}

func repositoryMarkdownFiles(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != root && (strings.HasPrefix(entry.Name(), ".") || entry.Name() == "vendor" || entry.Name() == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.EqualFold(filepath.Ext(path), ".md") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	return files
}

func checkMarkdownTarget(t *testing.T, root, source, rawTarget string, anchorCache map[string]map[string]bool) {
	t.Helper()
	rawTarget = strings.Trim(rawTarget, "<>")
	parsed, err := url.Parse(rawTarget)
	if err != nil {
		reportBrokenLink(t, root, source, rawTarget, "invalid URL: "+err.Error())
		return
	}
	if parsed.IsAbs() || parsed.Host != "" || strings.HasPrefix(rawTarget, "//") {
		return
	}
	decodedPath, err := url.PathUnescape(parsed.Path)
	if err != nil {
		reportBrokenLink(t, root, source, rawTarget, "invalid path escape")
		return
	}
	targetPath := source
	if decodedPath != "" {
		if filepath.IsAbs(decodedPath) {
			targetPath = filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(decodedPath, "/")))
		} else {
			targetPath = filepath.Join(filepath.Dir(source), filepath.FromSlash(decodedPath))
		}
		targetPath = filepath.Clean(targetPath)
		rel, err := filepath.Rel(root, targetPath)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			reportBrokenLink(t, root, source, rawTarget, "target escapes repository")
			return
		}
	}
	info, err := os.Stat(targetPath)
	if err != nil {
		reportBrokenLink(t, root, source, rawTarget, "target does not exist")
		return
	}
	if parsed.Fragment == "" {
		return
	}
	if info.IsDir() {
		targetPath = filepath.Join(targetPath, "README.md")
		if _, err := os.Stat(targetPath); err != nil {
			reportBrokenLink(t, root, source, rawTarget, "directory fragment has no README.md target")
			return
		}
	}
	if !strings.EqualFold(filepath.Ext(targetPath), ".md") {
		return
	}
	fragment, err := url.PathUnescape(parsed.Fragment)
	if err != nil {
		reportBrokenLink(t, root, source, rawTarget, "invalid fragment escape")
		return
	}
	anchors := anchorCache[targetPath]
	if anchors == nil {
		data, err := os.ReadFile(targetPath)
		if err != nil {
			reportBrokenLink(t, root, source, rawTarget, err.Error())
			return
		}
		anchors = markdownAnchors(string(data))
		anchorCache[targetPath] = anchors
	}
	if !anchors[fragment] {
		reportBrokenLink(t, root, source, rawTarget, "heading fragment does not exist")
	}
}

func reportBrokenLink(t *testing.T, root, source, target, reason string) {
	t.Helper()
	rel, err := filepath.Rel(root, source)
	if err != nil {
		rel = source
	}
	t.Errorf("%s: broken relative link %q: %s", filepath.ToSlash(rel), target, reason)
}

func markdownAnchors(markdown string) map[string]bool {
	content := withoutFencedCode(markdown)
	anchors := make(map[string]bool)
	counts := make(map[string]int)
	lines := strings.Split(content, "\n")
	for _, match := range explicitAnchor.FindAllStringSubmatch(content, -1) {
		anchors[match[1]] = true
	}
	for i, line := range lines {
		title := ""
		if match := atxHeading.FindStringSubmatch(line); match != nil {
			title = match[1]
		} else if i+1 < len(lines) && strings.TrimSpace(line) != "" && setextHeading.MatchString(lines[i+1]) {
			title = strings.TrimSpace(line)
		}
		if title == "" {
			continue
		}
		base := githubHeadingSlug(title)
		if base == "" {
			continue
		}
		slug := base
		if count := counts[base]; count > 0 {
			slug += "-" + strconv.Itoa(count)
		}
		counts[base]++
		anchors[slug] = true
	}
	return anchors
}

func githubHeadingSlug(title string) string {
	title = html.UnescapeString(title)
	title = markdownLabel.ReplaceAllString(title, "$1")
	title = htmlTag.ReplaceAllString(title, "")
	title = strings.ToLower(title)
	var slug strings.Builder
	for _, r := range title {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), r == '-', r == '_':
			slug.WriteRune(r)
		case unicode.IsSpace(r):
			slug.WriteByte('-')
		}
	}
	return slug.String()
}

func withoutFencedCode(markdown string) string {
	lines := strings.Split(markdown, "\n")
	inFence := false
	fence := ""
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !inFence && (strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~")) {
			inFence = true
			fence = trimmed[:3]
			lines[i] = ""
			continue
		}
		if inFence {
			lines[i] = ""
			if strings.HasPrefix(trimmed, fence) {
				inFence = false
				fence = ""
			}
		}
	}
	return strings.Join(lines, "\n")
}
