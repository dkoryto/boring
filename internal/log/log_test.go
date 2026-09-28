package log

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setup initializes the logger for a test and restores the previous state
// afterwards.
func setup(t *testing.T, w *bytes.Buffer, interactive, colors bool) {
	t.Helper()
	prev := instance
	codes := []*string{&Reset, &Bold, &Red, &Green, &Yellow, &Blue}
	saved := make([]string, len(codes))
	for i, c := range codes {
		saved[i] = *c
	}
	t.Cleanup(func() {
		instance = prev
		for i, c := range codes {
			*c = saved[i]
		}
	})
	Init(w, interactive, colors)
}

func TestInteractive(t *testing.T) {
	t.Setenv("DEBUG", "1")
	var buf bytes.Buffer
	setup(t, &buf, true, true)

	Debugf("d%d", 1)
	Infof("i%d", 2)
	Warningf("w%d", 3)
	Errorf("e%d", 4)
	Printf("p%d\n", 5)
	Emitf("m%d\n", 6)

	out := buf.String()
	for _, want := range []string{
		"DEBUG d1", "INFO" + Reset + " i2", "WARNING" + Reset + " w3",
		"ERROR" + Reset + " e4", "p5\n", "m6\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q does not contain %q", out, want)
		}
	}
	if Reset == "" {
		t.Error("colors not enabled")
	}
}

func TestNonInteractive(t *testing.T) {
	t.Setenv("DEBUG", "1")
	var buf bytes.Buffer
	setup(t, &buf, false, false)

	Debugf("d")
	Infof("i")
	Warningf("w")
	Errorf("e")
	Printf("p")
	if buf.Len() != 0 {
		t.Fatalf("expected no output, got %q", buf.String())
	}
	// Emitted data is never suppressed
	Emitf("m")
	if buf.String() != "m" {
		t.Fatalf("got %q, want %q", buf.String(), "m")
	}
}

func TestRotate(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "log"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	l := &logger{writer: f}

	// Below the limit the file is appended to
	if _, err := l.Write([]byte("a")); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Write(bytes.Repeat([]byte("b"), maxFileSize)); err != nil {
		t.Fatal(err)
	}
	// The file is now over the limit and gets truncated before writing
	if _, err := l.Write([]byte("c")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "c" {
		t.Fatalf("expected rotated file to contain %q, got %d bytes", "c", len(data))
	}
}

func TestRotateStatError(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "log"))
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	l := &logger{writer: f}
	// Stat fails on the closed file, so rotation is skipped and the write
	// fails as well
	if _, err := l.Write([]byte("a")); err == nil {
		t.Fatal("expected write to a closed file to fail")
	}
}
