package table

import (
	"testing"
)

func TestString(t *testing.T) {
	tbl := New("NAME", "PORT")
	tbl.AddRow("\x1b[31mlonger-name\x1b[0m", 22)
	want := "NAME         PORT  \n" +
		"\x1b[31mlonger-name\x1b[0m  22    \n"
	if got := tbl.String(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestAddRowWrongColumns(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("expected panic for wrong number of columns")
		}
	}()
	New("A", "B").AddRow("only one")
}
