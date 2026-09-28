package paths

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestReplaceTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"~":     home,
		"~/foo": filepath.Join(home, "foo"),
		"/abs":  "/abs",
	}
	for in, want := range cases {
		if got := ReplaceTilde(in); got != want {
			t.Errorf("ReplaceTilde(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestReplaceTildeNoHome(t *testing.T) {
	// os.UserHomeDir only consults the environment on these systems
	switch runtime.GOOS {
	case "windows", "plan9", "android", "ios":
		t.Skip("home directory not taken from $HOME on", runtime.GOOS)
	}
	t.Setenv("HOME", "")
	defer func() {
		if recover() == nil {
			t.Error("expected panic when the home directory is unknown")
		}
	}()
	ReplaceTilde("~")
}
