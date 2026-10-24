package controller

import (
	"os"
	"testing"
)

// TestOpenRedirect_Append は出力先を O_APPEND で開くことを固定する。
//
// taskmasterd の中では同じパスへの書き手は Sink 1 枚にまとまるので、O_APPEND が無くても書き合わない。
// 効いてくるのは同じパスを別々に開いたとき —— taskmasterd を起動し直したとき（前回の行の上に
// 先頭から書く）と、taskmasterd の外にも書き手がいるとき（独立したオフセットで書き合う）。
// O_APPEND を外すと、どちらでも先に書いた行が消える。
func TestOpenRedirect_Append(t *testing.T) {
	path := t.TempDir() + "/out.log"
	if err := os.WriteFile(path, []byte("previous run\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	a, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	for _, w := range []struct {
		f    *os.File
		line string
	}{{a, "a:1\n"}, {b, "b:1\n"}, {a, "a:2\n"}} {
		if _, err := w.f.WriteString(w.line); err != nil {
			t.Fatal(err)
		}
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := "previous run\na:1\nb:1\na:2\n"; string(got) != want {
		t.Errorf("行が消えた（O_APPEND で開いていない）\ngot:  %q\nwant: %q", got, want)
	}
}
