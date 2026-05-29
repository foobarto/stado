package main

import (
	"reflect"
	"testing"
)

func TestSplitBannerLines(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"newline only", "\n", nil},
		{"whitespace stays", "   ", []string{"   "}},
		{"single line no newline", "one", []string{"one"}},
		{"single line trailing newline", "one\n", []string{"one"}},
		{"multi line", "a\nb\nc", []string{"a", "b", "c"}},
		{"multi line trailing", "a\nb\n", []string{"a", "b"}},
		{"crlf trailing", "a\r\nb\r\n", []string{"a", "b"}},
		{"crlf internal", "a\r\nb", []string{"a", "b"}},
		{"blank interior line preserved", "a\n\nb", []string{"a", "", "b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := splitBannerLines(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("splitBannerLines(%q) = %#v, want %#v", tc.in, got, tc.want)
			}
		})
	}
}
