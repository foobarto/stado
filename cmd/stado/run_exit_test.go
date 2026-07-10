package main

import (
	"errors"
	"testing"

	"github.com/foobarto/stado/internal/runtime"
)

func TestRunLoopUsesExitCode2OnlyForTypedLimits(t *testing.T) {
	for _, err := range []error{
		runtime.ErrTokenCapExceeded,
		runtime.ErrMaxTurnsExceeded,
		runtime.ErrVerifyExhausted,
	} {
		if !runLoopUsesExitCode2(err) {
			t.Fatalf("typed limit %v did not map to exit 2", err)
		}
	}
	if runLoopUsesExitCode2(errors.New("runtime: buffered verification candidate exceeded event budget")) {
		t.Fatal("untyped internal error mapped to exit 2")
	}
}
