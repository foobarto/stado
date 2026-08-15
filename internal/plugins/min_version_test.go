package plugins

import (
	"strings"
	"testing"
)

func TestCheckMinimumStadoVersion(t *testing.T) {
	tests := []struct {
		name, minimum, current string
		wantError              string
	}{
		{name: "none", current: "0.0.0-dev"},
		{name: "equal", minimum: "0.80.0", current: "0.80.0"},
		{name: "leading v", minimum: "v0.80.0", current: "v0.81.0"},
		{name: "older", minimum: "0.80.0", current: "0.79.9", wantError: "requires stado"},
		{name: "development host", minimum: "0.80.0", current: "0.0.0-dev", wantError: "not a stable semantic version"},
		{name: "malformed host", minimum: "0.80.0", current: "development", wantError: "not a stable semantic version"},
		{name: "prerelease host", minimum: "0.80.0", current: "0.81.0-rc.1", wantError: "not a stable semantic version"},
		{name: "malformed minimum", minimum: "latest", current: "0.80.0", wantError: "stable semantic version"},
		{name: "prerelease minimum", minimum: "0.80.0-rc.1", current: "0.80.0", wantError: "stable semantic version"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := CheckMinimumStadoVersion(test.minimum, test.current)
			if test.wantError == "" && err != nil {
				t.Fatal(err)
			}
			if test.wantError != "" && (err == nil || !strings.Contains(err.Error(), test.wantError)) {
				t.Fatalf("error = %v; want %q", err, test.wantError)
			}
		})
	}
}
