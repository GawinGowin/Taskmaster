package controller

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"taskmaster/internal/config"
	"taskmaster/internal/process"
)

// newRegistry は sinkFor と Close だけを使うための、レジストリしか持たない Controller を返す。
// 後始末で閉じ残しを閉じる（閉じ済みなら Close は何もしない）。
func newRegistry(t *testing.T) *Controller {
	t.Helper()
	c := &Controller{sinks: make(map[string]*process.Sink)}
	t.Cleanup(func() { c.Close() })
	return c
}

func mustSink(t *testing.T, c *Controller, path string) *process.Sink {
	t.Helper()
	s, err := c.sinkFor(path)
	if err != nil {
		t.Fatalf("sinkFor(%q): %v", path, err)
	}
	return s
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("読めない: %v", err)
	}
	return string(b)
}

// TestSinkFor_Appends は出力先を O_APPEND で開くことを固定する。
//
// taskmasterd の中では同じパスへの書き手は Sink 1 枚にまとまるので、O_APPEND が無くても書き合わない。
// 効いてくるのは同じパスを別々に開いたとき —— taskmasterd を起動し直したとき（前回の行の上に
// 先頭から書く）と、taskmasterd の外にも書き手がいるとき（独立したオフセットで書き合う）。
// 別の Controller を 2 つ作って、外の書き手を再現する。O_APPEND を外すと、どちらでも先に書いた行が消える。
func TestSinkFor_Appends(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.log")
	if err := os.WriteFile(path, []byte("previous run\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	a, b := newRegistry(t), newRegistry(t)
	mustSink(t, a, path).Write([]byte("a:1\n"))
	if err := a.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	mustSink(t, b, path).Write([]byte("b:1\n"))
	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if got, want := readFile(t, path), "previous run\na:1\nb:1\n"; got != want {
		t.Errorf("行が消えた（O_APPEND で開いていない）\ngot:  %q\nwant: %q", got, want)
	}
}

// TestSinkFor_SamePathSameSink は、同じファイルを指すパスなら書き方が違っても 1 枚の Sink が返ることを見る。
// 別々の Sink になると書き手が複数に戻り、フルバッファの子の行が混ざる。
func TestSinkFor_SamePathSameSink(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	c := newRegistry(t)

	want := mustSink(t, c, "out.log")
	for _, path := range []string{"./out.log", filepath.Join(dir, "out.log"), dir + "/sub/../out.log"} {
		if got := mustSink(t, c, path); got != want {
			t.Errorf("sinkFor(%q) が別の Sink を返した", path)
		}
	}
	if other := mustSink(t, c, "other.log"); other == want {
		t.Error("別のパスに同じ Sink を返した")
	}
	if len(c.sinks) != 2 {
		t.Errorf("レジストリに %d 枚, want 2", len(c.sinks))
	}
}

// TestSinkFor_EmptyIsDiscard は、空のパスで nil（破棄）が返り、ファイルを開かないことを見る。
func TestSinkFor_EmptyIsDiscard(t *testing.T) {
	c := newRegistry(t)
	s, err := c.sinkFor("")
	if err != nil || s != nil {
		t.Errorf("sinkFor(\"\") = %v, %v, want nil, nil", s, err)
	}
	if len(c.sinks) != 0 {
		t.Errorf("レジストリに %d 枚, want 0", len(c.sinks))
	}
}

// TestClose_FlushesAndClosesAll は、Close が全 Sink を流してから閉じ、レジストリを空にすることを見る。
// 2 回目の Close は何もしない（閉じた Sink をもう一度閉じて ErrClosed を返したりしない）。
func TestClose_FlushesAndClosesAll(t *testing.T) {
	dir := t.TempDir()
	c := newRegistry(t)
	paths := []string{filepath.Join(dir, "a.log"), filepath.Join(dir, "b.log")}
	var sinks []*process.Sink
	for _, p := range paths {
		s := mustSink(t, c, p)
		s.Write([]byte("last line\n")) // 200ms のフラッシュより前に Close する
		sinks = append(sinks, s)
	}

	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	for i, p := range paths {
		if got := readFile(t, p); got != "last line\n" {
			t.Errorf("%s = %q, want %q（Close が流していない）", p, got, "last line\n")
		}
		if err := sinks[i].Close(); !errors.Is(err, os.ErrClosed) {
			t.Errorf("%s の Sink が閉じていない: Close() = %v", p, err)
		}
	}
	if len(c.sinks) != 0 {
		t.Errorf("Close の後もレジストリに %d 枚残っている", len(c.sinks))
	}
	if err := c.Close(); err != nil {
		t.Errorf("2 回目の Close = %v, want nil", err)
	}
}

func program(stdout, stderr string) config.Program {
	return config.Program{
		Cmd:      config.Command{"/bin/true"},
		Numprocs: 2,
		Stdout:   config.EnvString(stdout),
		Stderr:   config.EnvString(stderr),
	}
}

// TestNew_SharesSinks は、New が program・numprocs・stdout / stderr をまたいで同じパスに 1 枚の Sink を配ることを見る。
func TestNew_SharesSinks(t *testing.T) {
	dir := t.TempDir()
	shared := filepath.Join(dir, "shared.log")
	c, err := New(&config.Config{Programs: map[string]config.Program{
		"a": program(shared, shared),
		"b": program(shared, filepath.Join(dir, "b.err")),
		"q": program("", ""),
	}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	a, b, q := c.groups["a"], c.groups["b"], c.groups["q"]
	if a.stdout == nil || a.stdout != a.stderr || a.stdout != b.stdout {
		t.Error("同じパスに別々の Sink を配っている")
	}
	if b.stderr == nil || b.stderr == b.stdout {
		t.Error("別のパスに同じ Sink を配っている")
	}
	if q.stdout != nil || q.stderr != nil {
		t.Error("破棄の program に Sink を配っている")
	}
	if len(c.sinks) != 2 {
		t.Errorf("レジストリに %d 枚, want 2", len(c.sinks))
	}
}

// TestNew_FailureClosesSinks は、途中の program で出力先を開けなかったとき、
// どの program のどちらの出力かをエラーに入れ、それまでに開いた Sink を閉じることを見る。
func TestNew_FailureClosesSinks(t *testing.T) {
	dir := t.TempDir()
	ok := filepath.Join(dir, "ok.log")
	_, err := New(&config.Config{Programs: map[string]config.Program{
		"a": program(ok, ok),
		"b": program(ok, filepath.Join(dir, "missing", "b.err")), // 親ディレクトリが無いので開けない
	}})
	if err == nil {
		t.Fatal("err = nil, want エラー")
	}
	t.Logf("実際のエラー出力:\n%v", err)
	if !strings.Contains(err.Error(), "b: stderr:") {
		t.Errorf("どの program のどちらの出力か分からない: %q", err)
	}

	// 閉じ忘れていれば、ok.log の fd がこのプロセスに残っている。
	ents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if target, _ := os.Readlink(filepath.Join("/proc/self/fd", e.Name())); target == ok {
			t.Errorf("失敗した New が %s を開いたまま返った（fd %s）", ok, e.Name())
		}
	}
}
