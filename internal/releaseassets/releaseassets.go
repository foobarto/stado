package releaseassets

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

var expandedAssetDigestRE = regexp.MustCompile(`sha256:([0-9a-f]{64})`)
var expandedAssetNameRE = regexp.MustCompile(`href="[^"]+/download/[^"]+/([^"/]+)"`)
var hexDigestLineRE = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

// ParseSHA256Sidecar parses the contents of a standard "<sha>  <file>"
// checksum sidecar and returns the digest for wantName.
func ParseSHA256Sidecar(body []byte, wantName string) (string, error) {
	lines := strings.Split(strings.ReplaceAll(string(body), "\r\n", "\n"), "\n")
	for i, line := range lines {
		if strings.Contains(line, wantName) {
			for _, next := range lines[i+1:] {
				next = strings.TrimSpace(next)
				if hexDigestLineRE.MatchString(next) {
					return strings.ToLower(next), nil
				}
			}
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if fields[1] == wantName || filepath.Base(fields[1]) == wantName {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("digest for %q not found", wantName)
}

// ParseGitHubExpandedAssetsDigests extracts asset-name -> sha256 mappings
// from GitHub's public expanded_assets HTML fragment.
func ParseGitHubExpandedAssetsDigests(body []byte) (map[string]string, error) {
	out := map[string]string{}
	for _, part := range strings.Split(string(body), "<li") {
		nameMatch := expandedAssetNameRE.FindStringSubmatch(part)
		if len(nameMatch) != 2 {
			continue
		}
		digestMatch := expandedAssetDigestRE.FindStringSubmatch(part)
		if len(digestMatch) != 2 {
			continue
		}
		out[nameMatch[1]] = digestMatch[1]
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no digests found in expanded assets fragment")
	}
	return out, nil
}
