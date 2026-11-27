package process

import (
	"bytes"
	"errors"
	"os"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

// runPump は pipe の読み端に pump を付け、write で書き端に書かせてから閉じる。
// pump が終わるのを待ち、Sink を Close してファイルの中身を返す（200ms のフラッシュを待たない）。
func runPump(t *testing.T, write func(w *os.File)) []byte {
	t.Helper()
	s, path := newTestSink(t)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}

	done := make(chan struct{})
	go func() {
		pump(r, s, nil, Source{})
		close(done)
	}()

	write(w)
	if err := w.Close(); err != nil {
		t.Fatalf("書き端の Close: %v", err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("書き端を閉じても pump が終わらない")
	}

	// 終わるときに読み端を閉じていなければ、ここで nil が返る。
	if err := r.Close(); !errors.Is(err, os.ErrClosed) {
		t.Errorf("pump の後の r.Close() = %v, want os.ErrClosed（読み端を閉じていない）", err)
	}

	if err := s.Close(); err != nil {
		t.Fatalf("Sink の Close: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("読めない: %v", err)
	}
	return b
}

func writeAll(t *testing.T, w *os.File, p []byte) {
	t.Helper()
	if _, err := w.Write(p); err != nil {
		t.Fatalf("書き端に書けない: %v", err)
	}
}

// TestPump_CopiesBytes は、書いたバイト列がプレフィックスも欠けも無くファイルに届くことを見る。
// 空行や CRLF も混ぜ、行の中身に手を入れていないことを確かめる。
func TestPump_CopiesBytes(t *testing.T) {
	want := []byte("first\n\n{\"k\":1}\nwith\r\nlast\n")

	got := runPump(t, func(w *os.File) { writeAll(t, w, want) })
	if !bytes.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestPump_LongLine は、16 KiB を超える行が欠けずに届き、その後の行も読み続けることを見る。
// bufio.Scanner に戻すと、上限を超えた行で ErrTooLong を返して以後を読まなくなる。
func TestPump_LongLine(t *testing.T) {
	var want []byte
	want = append(want, bytes.Repeat([]byte{'x'}, 40000)...)
	want = append(want, '\n')
	want = append(want, "after\n"...)

	got := runPump(t, func(w *os.File) { writeAll(t, w, want) })
	if !bytes.Equal(got, want) {
		t.Errorf("%d B 届いた, want %d B", len(got), len(want))
	}
}

// TestPump_TrailingFragment は、改行で終わらない最後の断片が落ちないことを見る。
// EOF と一緒に返るので、err を先に見ると捨ててしまう。
func TestPump_TrailingFragment(t *testing.T) {
	cases := map[string][]byte{
		"行の後ろ":     []byte("line\nno newline"),
		"断片だけ":     []byte("no newline"),
		"16 KiB 超": bytes.Repeat([]byte{'y'}, maxLine+100),
	}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			got := runPump(t, func(w *os.File) { writeAll(t, w, want) })
			if !bytes.Equal(got, want) {
				t.Errorf("%d B 届いた, want %d B", len(got), len(want))
			}
		})
	}
}

// TestPump_WaitsForAllWriters は、書き端が 1 本でも残っていれば pump が終わらないことを見る。
// 親が cmd.Start() の後に書き端を閉じ忘れたり、孫が握っていたりすると、子が死んでも EOF が来ない。
func TestPump_WaitsForAllWriters(t *testing.T) {
	s, _ := newTestSink(t)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	fd, err := syscall.Dup(int(w.Fd()))
	if err != nil {
		t.Fatalf("dup: %v", err)
	}
	other := os.NewFile(uintptr(fd), "other")

	done := make(chan struct{})
	go func() {
		pump(r, s, nil, Source{})
		close(done)
	}()

	w.Close()
	select {
	case <-done:
		t.Fatal("書き端がもう 1 本残っているのに pump が終わった")
	case <-time.After(100 * time.Millisecond):
	}

	other.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("最後の書き端を閉じても pump が終わらない")
	}
}

// runPumpView は runPump と同じく pump を走らせ、src を出どころにして View にも書かせる。
// ファイルの中身と、View に登録した宛先に届いた行（wantLines 行が届くまで待つ）を返す。
func runPumpView(t *testing.T, src Source, wantLines int, write func(w *os.File)) (file []byte, view []string) {
	t.Helper()
	s, path := newTestSink(t)
	v := NewView()
	rec := newRecorder()
	unreg := v.Register(all, rec)
	defer unreg()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	done := make(chan struct{})
	go func() {
		pump(r, s, v, src)
		close(done)
	}()
	write(w)
	if err := w.Close(); err != nil {
		t.Fatalf("書き端の Close: %v", err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("書き端を閉じても pump が終わらない")
	}

	view = rec.waitLines(t, wantLines)
	if err := s.Close(); err != nil {
		t.Fatalf("Sink の Close: %v", err)
	}
	file, err = os.ReadFile(path)
	if err != nil {
		t.Fatalf("読めない: %v", err)
	}
	return file, view
}

// TestPump_ToView は、pump が同じ行をファイル層には素のまま、ビュー層には出どころを付けて渡すことを見る。
func TestPump_ToView(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  Source
		in   string
		want []string
	}{
		{
			name: "stdout",
			src:  Source{Name: "p", Index: 0, Pid: 123},
			in:   "a\n{\"k\":1}\n",
			want: []string{"p:0/123 | a", `p:0/123 | {"k":1}`},
		},
		{
			name: "stderr",
			src:  Source{Name: "p", Index: 2, Pid: 9, Stderr: true},
			in:   "oops\n",
			want: []string{"p:2/9 ! oops"},
		},
		{
			name: "改行で終わらない最後の断片",
			src:  Source{Name: "p", Index: 0, Pid: 123},
			in:   "line\nno newline",
			want: []string{"p:0/123 | line", "p:0/123 | no newline ↵"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file, view := runPumpView(t, tc.src, len(tc.want), func(w *os.File) { writeAll(t, w, []byte(tc.in)) })
			if string(file) != tc.in {
				t.Errorf("ファイル = %q, want %q（ファイル層は書いたバイト列のまま）", file, tc.in)
			}
			if !slices.Equal(view, tc.want) {
				t.Errorf("ビュー = %q, want %q", view, tc.want)
			}
		})
	}
}

// TestPump_LongLineToView は、40000 バイトの 1 行が、ビュー層では 16 KiB ごとの 3 行に分かれ、
// 改行で終わらない 2 つの塊に ↵ が付くことを見る。ファイル層では元の 1 行のまま。
func TestPump_LongLineToView(t *testing.T) {
	const n = 40000
	in := append(bytes.Repeat([]byte{'x'}, n), '\n')
	src := Source{Name: "p", Index: 0, Pid: 1}

	file, view := runPumpView(t, src, 3, func(w *os.File) { writeAll(t, w, in) })
	if !bytes.Equal(file, in) {
		t.Errorf("ファイルに %d B 届いた, want %d B", len(file), len(in))
	}

	prefix := "p:0/1 | "
	want := []string{
		prefix + strings.Repeat("x", maxLine) + " ↵",
		prefix + strings.Repeat("x", maxLine) + " ↵",
		prefix + strings.Repeat("x", n-2*maxLine),
	}
	if len(view) != len(want) {
		t.Fatalf("ビューに %d 行届いた, want %d 行", len(view), len(want))
	}
	for i := range want {
		if view[i] != want[i] {
			t.Errorf("ビューの %d 行目 = %q, want %q", i, abbrev(view[i]), abbrev(want[i]))
		}
	}
}
