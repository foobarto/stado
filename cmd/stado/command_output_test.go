package main

import (
	"io"
	"os"
	"testing"
)

// captureOutput runs fn and returns what it wrote to stdout and stderr.
func captureOutput(t *testing.T, fn func()) (string, string) {
	t.Helper()

	origOut := os.Stdout
	origErr := os.Stderr
	rOut, wOut, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	rErr, wErr, err := os.Pipe()
	if err != nil {
		t.Fatalf("stderr pipe: %v", err)
	}
	os.Stdout = wOut
	os.Stderr = wErr
	defer func() {
		os.Stdout = origOut
		os.Stderr = origErr
	}()

	outDone := make(chan []byte, 1)
	errDone := make(chan []byte, 1)
	go func() {
		buf, _ := io.ReadAll(rOut)
		outDone <- buf
	}()
	go func() {
		buf, _ := io.ReadAll(rErr)
		errDone <- buf
	}()

	fn()
	_ = wOut.Close()
	_ = wErr.Close()
	return string(<-outDone), string(<-errDone)
}
