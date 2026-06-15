package runtime

import (
	"fmt"
	"strings"
	"testing"
)

func TestInstanceStore_GetSet(t *testing.T) {
	s := NewInstanceStore()
	if err := s.Set("plugA", "k1", []byte("hello")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	v, ok := s.Get("plugA", "k1")
	if !ok || string(v) != "hello" {
		t.Errorf("Get: ok=%v v=%q, want true \"hello\"", ok, string(v))
	}
}

func TestInstanceStore_PerPluginNamespacing(t *testing.T) {
	s := NewInstanceStore()
	_ = s.Set("plugA", "shared", []byte("a-secret"))
	_ = s.Set("plugB", "shared", []byte("b-secret"))

	v, _ := s.Get("plugA", "shared")
	if string(v) != "a-secret" {
		t.Errorf("plugA reads %q, want a-secret (namespace bleed)", v)
	}
	v, _ = s.Get("plugB", "shared")
	if string(v) != "b-secret" {
		t.Errorf("plugB reads %q, want b-secret (namespace bleed)", v)
	}
}

func TestInstanceStore_GetMissing(t *testing.T) {
	s := NewInstanceStore()
	_, ok := s.Get("plug", "nope")
	if ok {
		t.Error("Get on missing returned ok=true")
	}
}

func TestInstanceStore_Delete(t *testing.T) {
	s := NewInstanceStore()
	_ = s.Set("plug", "k", []byte("v"))
	s.Delete("plug", "k")
	if _, ok := s.Get("plug", "k"); ok {
		t.Error("Get after Delete returned ok=true")
	}
	// Delete again — should not panic.
	s.Delete("plug", "k")
}

func TestInstanceStore_List(t *testing.T) {
	s := NewInstanceStore()
	_ = s.Set("plug", "alpha", []byte("a"))
	_ = s.Set("plug", "beta", []byte("b"))
	_ = s.Set("plug", "alpha2", []byte("a2"))
	keys := s.List("plug", "alpha")
	if len(keys) != 2 {
		t.Errorf("List(alpha) returned %d keys, want 2: %v", len(keys), keys)
	}
	keys = s.List("plug", "")
	if len(keys) != 3 {
		t.Errorf("List(\"\") returned %d keys, want 3: %v", len(keys), keys)
	}
	// Deterministic order.
	if keys[0] != "alpha" || keys[1] != "alpha2" || keys[2] != "beta" {
		t.Errorf("List unsorted: %v", keys)
	}
}

func TestInstanceStore_PerKeyLimit(t *testing.T) {
	s := NewInstanceStore()
	tooBig := make([]byte, stateMaxValueBytes+1)
	if err := s.Set("plug", "huge", tooBig); err == nil {
		t.Error("Set should reject value above per-key limit")
	}
	atLimit := make([]byte, stateMaxValueBytes)
	if err := s.Set("plug", "limit", atLimit); err != nil {
		t.Errorf("Set at exactly the limit should succeed; got %v", err)
	}
}

func TestInstanceStore_PerPluginTotalLimit(t *testing.T) {
	s := NewInstanceStore()
	// Leave headroom for per-key byte accounting (keys are counted in totals).
	chunk := make([]byte, (stateMaxTotalBytes/16)-64)
	for i := 0; i < 16; i++ {
		if err := s.Set("plug", chunkKey(i), chunk); err != nil {
			t.Fatalf("Set chunk %d: %v", i, err)
		}
	}
	overflow := make([]byte, stateMaxValueBytes)
	if err := s.Set("plug", "overflow", overflow); err == nil {
		t.Error("17th chunk should have been rejected")
	}
}

func TestStateAccess_CanRead(t *testing.T) {
	cases := []struct {
		name         string
		readDeclared bool
		readGlobs    []string
		key          string
		want         bool
	}{
		{"undeclared read denied", false, nil, "anything", false},
		{"empty globs = match-all when declared", true, nil, "anything", true},
		{"exact match", true, []string{"foo"}, "foo", true},
		{"non-match", true, []string{"foo"}, "bar", false},
		{"glob match", true, []string{"api_*"}, "api_token", true},
		{"glob non-match", true, []string{"api_*"}, "db_pwd", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &StateAccess{ReadDeclared: tc.readDeclared, ReadGlobs: tc.readGlobs}
			if got := s.CanRead(tc.key); got != tc.want {
				t.Errorf("CanRead(%q) = %v, want %v", tc.key, got, tc.want)
			}
		})
	}
}

func TestStateAccess_WriteOnlyCannotRead(t *testing.T) {
	writeOnly := &StateAccess{WriteDeclared: true, WriteGlobs: []string{"cache_*"}}
	if writeOnly.CanRead("cache_token") {
		t.Error("write-only StateAccess must not confer read")
	}
	if !writeOnly.CanWrite("cache_token") {
		t.Error("write-only StateAccess should allow scoped write")
	}
}

func TestStateAccess_ReadOnlyCannotWrite(t *testing.T) {
	readOnly := &StateAccess{ReadDeclared: true}
	if readOnly.CanWrite("anything") {
		t.Error("read-only StateAccess must not confer write")
	}
}

func TestInstanceStore_EmptyValueCountsKeyBytes(t *testing.T) {
	s := NewInstanceStore()
	key := strings.Repeat("k", 1024)
	if err := s.Set("plug", key, nil); err != nil {
		t.Fatalf("Set empty value: %v", err)
	}
	if err := s.Set("plug", key, []byte{}); err != nil {
		t.Fatalf("Set empty slice: %v", err)
	}
}

func TestInstanceStore_EntryCountLimit(t *testing.T) {
	s := NewInstanceStore()
	for i := 0; i < stateMaxEntries; i++ {
		if err := s.Set("plug", fmt.Sprintf("k%d", i), []byte("x")); err != nil {
			t.Fatalf("Set entry %d: %v", i, err)
		}
	}
	if err := s.Set("plug", "overflow", []byte("x")); err == nil {
		t.Fatal("expected entry-count limit error")
	}
}

func TestStateAccess_NilSafety(t *testing.T) {
	var s *StateAccess
	if s.CanRead("k") {
		t.Error("nil StateAccess.CanRead should return false")
	}
	if s.CanWrite("k") {
		t.Error("nil StateAccess.CanWrite should return false")
	}
}

func chunkKey(i int) string {
	switch i {
	case 0:
		return "k0"
	case 1:
		return "k1"
	case 2:
		return "k2"
	case 3:
		return "k3"
	case 4:
		return "k4"
	case 5:
		return "k5"
	case 6:
		return "k6"
	case 7:
		return "k7"
	case 8:
		return "k8"
	case 9:
		return "k9"
	case 10:
		return "k10"
	case 11:
		return "k11"
	case 12:
		return "k12"
	case 13:
		return "k13"
	case 14:
		return "k14"
	case 15:
		return "k15"
	}
	return "kN"
}
