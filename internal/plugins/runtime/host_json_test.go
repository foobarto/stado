package runtime

import (
	"strings"
	"testing"
)

func TestJSONGetByPath(t *testing.T) {
	raw := []byte(`{"user":{"name":"alice","tags":["admin","ops"]},"count":42,"active":true,"meta":null}`)
	cases := []struct {
		path    string
		want    string
		wantErr bool
	}{
		{"user.name", `"alice"`, false},
		{"user.tags.0", `"admin"`, false},
		{"user.tags.1", `"ops"`, false},
		{"user.tags.2", "", true},  // out of range
		{"user.tags.x", "", true},  // not numeric
		{"count", "42", false},
		{"active", "true", false},
		{"meta", "null", false},
		{"nope", "", true}, // missing
		{"", "", false},    // root → whole object
		{".", "", false},   // root alias
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			got, err := jsonGetByPath(raw, tc.path)
			if tc.wantErr {
				if err == nil {
					t.Errorf("path=%q: expected error, got %s", tc.path, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("path=%q: unexpected err: %v", tc.path, err)
			}
			if tc.path == "" || tc.path == "." {
				// Root → the whole object (key order is Go-sorted, so
				// just check it round-trips to a valid JSON object).
				if !strings.HasPrefix(string(got), `{`) || !strings.Contains(string(got), `"user":`) {
					t.Errorf("root path: unexpected output: %s", got)
				}
				return
			}
			if string(got) != tc.want {
				t.Errorf("path=%q: got %s, want %s", tc.path, got, tc.want)
			}
		})
	}
}

func TestJSONGetByPath_NestedObject(t *testing.T) {
	raw := []byte(`{"a":{"b":{"c":"deep"}}}`)
	got, err := jsonGetByPath(raw, "a.b.c")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `"deep"` {
		t.Errorf("got %s, want \"deep\"", got)
	}
}

func TestJSONGetByPath_MalformedJSON(t *testing.T) {
	if _, err := jsonGetByPath([]byte(`{not valid`), "x"); err == nil {
		t.Error("expected error for malformed JSON")
	}
}

func TestJSONFormat_Compact(t *testing.T) {
	raw := []byte(`{
  "a":  1,
  "b" :  [ 2, 3 ]
}`)
	got, err := jsonFormat(raw, 0)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"a":1,"b":[2,3]}` {
		t.Errorf("compact output: %s", got)
	}
}

func TestJSONFormat_Indent(t *testing.T) {
	raw := []byte(`{"a":1,"b":[2,3]}`)
	got, err := jsonFormat(raw, 2)
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"a\": 1,\n  \"b\": [\n    2,\n    3\n  ]\n}"
	if string(got) != want {
		t.Errorf("indent=2 output:\n%s\nwant:\n%s", got, want)
	}
}

func TestJSONFormat_Malformed(t *testing.T) {
	if _, err := jsonFormat([]byte(`not json`), 0); err == nil {
		t.Error("expected error for malformed JSON")
	}
}

// TestJSONSetByPath: set updates the document at the path and
// returns the canonical bytes; missing keys on existing objects
// are added; out-of-range array indices are rejected.
func TestJSONSetByPath(t *testing.T) {
	raw := []byte(`{"user":{"name":"alice","tags":["admin","ops"]},"count":42}`)

	cases := []struct {
		name      string
		path      string
		value     string
		wantInOut string // substring expected in output
		wantErr   bool
	}{
		{"replace string", "user.name", `"bob"`, `"name":"bob"`, false},
		{"replace number", "count", `100`, `"count":100`, false},
		{"replace array elem", "user.tags.0", `"superuser"`, `"superuser"`, false},
		{"add new key", "user.email", `"x@y"`, `"email":"x@y"`, false},
		{"replace object", "user", `{"role":"guest"}`, `"role":"guest"`, false},
		{"oor array index", "user.tags.99", `"x"`, "", true},
		{"non-numeric on array", "user.tags.q", `"x"`, "", true},
		{"value not json", "count", `not-json`, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := jsonSetByPath(raw, tc.path, []byte(tc.value))
			if tc.wantErr {
				if err == nil {
					t.Errorf("expected error; got %s", out)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if !strings.Contains(string(out), tc.wantInOut) {
				t.Errorf("output missing %q: %s", tc.wantInOut, out)
			}
		})
	}
}

// TestJSONSetByPath_RootReplace: empty path replaces the whole document.
func TestJSONSetByPath_RootReplace(t *testing.T) {
	out, err := jsonSetByPath([]byte(`{"a":1}`), "", []byte(`[1,2,3]`))
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `[1,2,3]` {
		t.Errorf("got %s, want [1,2,3]", out)
	}
}

// TestJSONSetByPath_NilDescent: setting deep into a missing key chain
// auto-creates the intermediate objects (treating nil as empty object).
func TestJSONSetByPath_NilDescent(t *testing.T) {
	out, err := jsonSetByPath([]byte(`{}`), "a.b.c", []byte(`"deep"`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"c":"deep"`) {
		t.Errorf("nested set missing: %s", out)
	}
}

// TestJSONGetByPath_ReturnsCanonicalForms confirms that round-tripping
// `_get` output back into `_get` works (number → number, string keeps
// quotes, object → object).
func TestJSONGetByPath_ReturnsCanonicalForms(t *testing.T) {
	raw := []byte(`{"obj":{"x":1},"arr":[1,2,3]}`)
	objGet, err := jsonGetByPath(raw, "obj")
	if err != nil {
		t.Fatal(err)
	}
	xGet, err := jsonGetByPath(objGet, "x")
	if err != nil {
		t.Fatalf("re-feed get: %v", err)
	}
	if string(xGet) != "1" {
		t.Errorf("re-feed: got %s, want 1", xGet)
	}
	arrGet, err := jsonGetByPath(raw, "arr")
	if err != nil {
		t.Fatal(err)
	}
	idxGet, err := jsonGetByPath(arrGet, "1")
	if err != nil {
		t.Fatalf("re-feed array: %v", err)
	}
	if string(idxGet) != "2" {
		t.Errorf("re-feed array idx: got %s", idxGet)
	}
}
