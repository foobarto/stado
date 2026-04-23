package sandbox

import (
	"reflect"
	"testing"
)

func TestFilterEnv_LastValueWins(t *testing.T) {
	got := filterEnv([]string{
		"KEEP=old",
		"DROP=nope",
		"KEEP=new",
		"ALSO=1",
	}, []string{"KEEP", "ALSO"})
	want := []string{"KEEP=new", "ALSO=1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("filterEnv = %v, want %v", got, want)
	}
}
