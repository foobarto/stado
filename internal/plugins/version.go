package plugins

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

type versionCore struct {
	major uint64
	minor uint64
	patch uint64
	pre   []string
}

// VersionLess reports whether a is lower precedence than b under SemVer 2.0.0.
// Build metadata is ignored. A leading "v" is accepted for operator-friendly
// plugin manifests, but otherwise versions must be canonical semver.
func VersionLess(a, b string) (bool, error) {
	av, err := parseVersion(a)
	if err != nil {
		return false, fmt.Errorf("version %q: %w", a, err)
	}
	bv, err := parseVersion(b)
	if err != nil {
		return false, fmt.Errorf("version %q: %w", b, err)
	}
	return compareVersion(av, bv) < 0, nil
}

// ValidateVersion rejects plugin manifest versions that cannot participate in
// rollback protection.
func ValidateVersion(s string) error {
	_, err := parseVersion(s)
	return err
}

func parseVersion(s string) (versionCore, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	if s == "" {
		return versionCore{}, fmt.Errorf("empty")
	}
	if i := strings.IndexByte(s, '+'); i >= 0 {
		if i == len(s)-1 {
			return versionCore{}, fmt.Errorf("empty build metadata")
		}
		if err := validateVersionIdentifiers(s[i+1:], "build", false); err != nil {
			return versionCore{}, err
		}
		s = s[:i]
	}
	core := s
	var pre []string
	if i := strings.IndexByte(s, '-'); i >= 0 {
		core = s[:i]
		preRaw := s[i+1:]
		if preRaw == "" {
			return versionCore{}, fmt.Errorf("empty prerelease")
		}
		if err := validateVersionIdentifiers(preRaw, "prerelease", true); err != nil {
			return versionCore{}, err
		}
		pre = strings.Split(preRaw, ".")
	}
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return versionCore{}, fmt.Errorf("want major.minor.patch")
	}
	nums := make([]uint64, 3)
	for i, part := range parts {
		if part == "" {
			return versionCore{}, fmt.Errorf("empty numeric component")
		}
		if len(part) > 1 && part[0] == '0' {
			return versionCore{}, fmt.Errorf("numeric component %q has leading zero", part)
		}
		if !isNumeric(part) {
			return versionCore{}, fmt.Errorf("numeric component %q is invalid", part)
		}
		n, err := strconv.ParseUint(part, 10, 64)
		if err != nil {
			return versionCore{}, err
		}
		nums[i] = n
	}
	return versionCore{major: nums[0], minor: nums[1], patch: nums[2], pre: pre}, nil
}

func validateVersionIdentifiers(raw, kind string, rejectNumericLeadingZero bool) error {
	for _, part := range strings.Split(raw, ".") {
		if part == "" {
			return fmt.Errorf("empty %s identifier", kind)
		}
		for _, r := range part {
			if !(unicode.IsDigit(r) || unicode.IsLetter(r) || r == '-') || r > unicode.MaxASCII {
				return fmt.Errorf("invalid %s identifier %q", kind, part)
			}
		}
		if rejectNumericLeadingZero && isNumeric(part) && len(part) > 1 && part[0] == '0' {
			return fmt.Errorf("numeric %s identifier %q has leading zero", kind, part)
		}
	}
	return nil
}

func compareVersion(a, b versionCore) int {
	for _, pair := range [][2]uint64{{a.major, b.major}, {a.minor, b.minor}, {a.patch, b.patch}} {
		if pair[0] < pair[1] {
			return -1
		}
		if pair[0] > pair[1] {
			return 1
		}
	}
	return comparePrerelease(a.pre, b.pre)
}

func comparePrerelease(a, b []string) int {
	if len(a) == 0 && len(b) == 0 {
		return 0
	}
	if len(a) == 0 {
		return 1
	}
	if len(b) == 0 {
		return -1
	}
	for i := 0; i < len(a) && i < len(b); i++ {
		aNum := isNumeric(a[i])
		bNum := isNumeric(b[i])
		switch {
		case aNum && bNum:
			if cmp := compareNumericIdentifier(a[i], b[i]); cmp != 0 {
				return cmp
			}
		case aNum:
			return -1
		case bNum:
			return 1
		default:
			if a[i] < b[i] {
				return -1
			}
			if a[i] > b[i] {
				return 1
			}
		}
	}
	if len(a) < len(b) {
		return -1
	}
	if len(a) > len(b) {
		return 1
	}
	return 0
}

func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func compareNumericIdentifier(a, b string) int {
	if len(a) < len(b) {
		return -1
	}
	if len(a) > len(b) {
		return 1
	}
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}
