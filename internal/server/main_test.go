package server

import (
	"bytes"
	"strings"
	"testing"
)

func TestCommand(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := Main("kcls", []string{"-version"}, "v1", nil, &out, &errOut); code != 0 || out.String() != "kcls v1\n" {
		t.Errorf("-version: %d, %q", code, out.String())
	}
	out.Reset()
	if code := Main("kcls", []string{"-nope"}, "v1", nil, &out, &errOut); code != 2 {
		t.Errorf("unknown flag: exit %d, want 2", code)
	}
	if code := Main("kcls", []string{"extra"}, "v1", nil, &out, &errOut); code != 2 {
		t.Errorf("argument: exit %d, want 2", code)
	}
	// stdio: the client hangs up at once, without exit.
	if code := Main("kcls", []string{"-stdio"}, "v1", strings.NewReader(""), &out, &errOut); code != 1 {
		t.Errorf("hang-up: exit %d, want 1", code)
	}
}
