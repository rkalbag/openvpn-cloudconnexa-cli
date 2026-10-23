package cli

import (
	"bytes"
	"testing"
)

func TestVersion(t *testing.T) {
	var out bytes.Buffer
	cmd := NewRootCmd()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"version"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "ccx dev\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
