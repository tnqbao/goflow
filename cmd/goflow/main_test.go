package main

import (
	"bytes"
	"errors"
	"testing"
)

func TestRun(t *testing.T) {
	var output bytes.Buffer
	if err := run(&output); err != nil {
		t.Fatalf("run: %v", err)
	}

	want := "GoFlow foundation: address=:8080 database=/data/goflow.db browser=/data/browser\n"
	if output.String() != want {
		t.Errorf("output = %q, want %q", output.String(), want)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("write failed")
}

func TestRunWriteError(t *testing.T) {
	if err := run(failingWriter{}); err == nil {
		t.Fatal("run succeeded with a failing writer")
	}
}
