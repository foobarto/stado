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
