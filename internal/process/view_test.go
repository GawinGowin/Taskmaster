package process

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// viewWait は、View の書く goroutine が宛先に書くのを待つ上限。
const viewWait = 2 * time.Second

// recorder は登録の宛先にする io.Writer。Write の呼び出しごとに中身を残す。
// gate が nil でなければ、Write は gate が閉じるまで戻らない（読むのをやめた端末や attach のクライアント）。
type recorder struct {
	gate      chan struct{}
	gateOnce  sync.Once
	entered   chan struct{} // 最初の Write に入ったら閉じる
	enterOnce sync.Once

	mu     sync.Mutex
	writes []string
}

func newRecorder() *recorder {
	return &recorder{entered: make(chan struct{})}
}

// newGatedRecorder は、gate を開けるまで Write から戻らない recorder を作る。
// 登録には registerGated を使う。
func newGatedRecorder() *recorder {
	return &recorder{entered: make(chan struct{}), gate: make(chan struct{})}
}

// registerGated は rec を v に登録し、後始末で gate を開けてから登録を外す。
// 逆の順序だと、View の不具合で Write が mutex を持ったまま止まったとき、
// 登録を外す関数がその mutex を待ち、テストが失敗せずに終わらなくなる。
func registerGated(t *testing.T, v *View, match func(Source) bool, rec *recorder) (unregister func()) {
	t.Helper()
	unreg := v.Register(match, rec)
	t.Cleanup(unreg)
	t.Cleanup(rec.open) // t.Cleanup は登録の逆順に呼ばれるので、こちらが先
	return unreg
}

// open は gate を閉じ、止めていた Write を戻らせる。2 回呼んでもよい。
func (r *recorder) open() {
	if r.gate != nil {
		r.gateOnce.Do(func() { close(r.gate) })
	}
}

func (r *recorder) Write(p []byte) (int, error) {
	r.enterOnce.Do(func() { close(r.entered) })
	if r.gate != nil {
		<-r.gate
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.writes = append(r.writes, string(p))
	return len(p), nil
}

func (r *recorder) text() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.writes, "")
}

func (r *recorder) writeCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.writes)
}

// lines は、これまでに書かれた中身を行に分けて返す（最後の改行の後ろの空文字列は含めない）。
func (r *recorder) lines() []string {
	s := r.text()
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

// waitLines は、r に n 行以上が書かれるまで待って行を返す。viewWait を過ぎたらテストを止める。
func (r *recorder) waitLines(t *testing.T, n int) []string {
	t.Helper()
	deadline := time.Now().Add(viewWait)
	for {
		ls := r.lines()
		if len(ls) >= n {
			return ls
		}
		if time.Now().After(deadline) {
			t.Fatalf("%v 待っても %d 行しか届かない, want %d 行以上。届いた行: %q", viewWait, len(ls), n, ls)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// waitEntered は、r の最初の Write が始まるのを待つ。
func (r *recorder) waitEntered(t *testing.T) {
	t.Helper()
	select {
	case <-r.entered:
	case <-time.After(viewWait):
		t.Fatalf("%v 待っても宛先の Write が呼ばれない", viewWait)
	}
}

// within は f を別の goroutine で呼び、viewWait 以内に戻らなければテストを止める。
func within(t *testing.T, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		f()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(viewWait):
		t.Fatalf("%s が %v 以内に戻らない", what, viewWait)
	}
}

func all(Source) bool { return true }

func named(name string) func(Source) bool {
	return func(s Source) bool { return s.Name == name }
}

var (
	srcWeb = Source{Name: "web", Index: 0, Pid: 1}
	srcDB  = Source{Name: "db", Index: 0, Pid: 2}
)

// TestView_NoRegistration は、登録が 1 つも無いときの Write が何もせずに戻ることを見る。
// logs を打っていない間、pump はずっとこの状態で View に書く。
func TestView_NoRegistration(t *testing.T) {
	v := NewView()
	within(t, "登録の無い View への Write", func() {
		for range 2000 {
			v.Write(srcWeb, []byte("a\n"))
		}
	})
}

// TestView_DeliversToMatchingRegistration は、行が対象に合う登録の宛先にだけ届くことを見る。
func TestView_DeliversToMatchingRegistration(t *testing.T) {
	v := NewView()
	webRec, dbRec := newRecorder(), newRecorder()
	unregWeb := v.Register(named("web"), webRec)
	defer unregWeb()
	unregDB := v.Register(named("db"), dbRec)
	defer unregDB()

	v.Write(srcWeb, []byte("from web\n"))
	v.Write(srcDB, []byte("from db\n"))

	if got, want := webRec.waitLines(t, 1), []string{"web:0/1 | from web"}; !slices.Equal(got, want) {
		t.Errorf("web の宛先 = %q, want %q", got, want)
	}
	if got, want := dbRec.waitLines(t, 1), []string{"db:0/2 | from db"}; !slices.Equal(got, want) {
		t.Errorf("db の宛先 = %q, want %q", got, want)
	}

	// 合わない行が後から届いていないか、少し待ってから見る。
	time.Sleep(50 * time.Millisecond)
	if got := webRec.lines(); len(got) != 1 {
		t.Errorf("web の宛先に %d 行届いた, want 1 行: %q", len(got), got)
	}
}

// TestView_WriteDoesNotBlock は、宛先が止まっても Write が戻り、溢れた行を捨てて数えることを見る。
//
// 書く goroutine が最初の行を持って宛先の Write で止まっている間に、channel の容量（1024 行）を
// 埋め、さらに 500 行を書く。500 行は捨てられ、宛先が動き出したあと、
// 書く goroutine は次に書く行の前に "[... 500 lines dropped]" を 1 行出す。
func TestView_WriteDoesNotBlock(t *testing.T) {
	const capacity, overflow = 1024, 500

	v := NewView()
	rec := newGatedRecorder()
	registerGated(t, v, all, rec)

	v.Write(srcWeb, []byte("first\n"))
	rec.waitEntered(t)

	within(t, "止まった宛先がある View への Write", func() {
		for i := range capacity + overflow {
			v.Write(srcWeb, fmt.Appendf(nil, "n=%d\n", i))
		}
	})

	rec.open()
	got := rec.waitLines(t, 1+1+capacity)

	want := []string{"web:0/1 | first", fmt.Sprintf("[... %d lines dropped]", overflow)}
	for i := range capacity {
		want = append(want, fmt.Sprintf("web:0/1 | n=%d", i))
	}
	if !slices.Equal(got, want) {
		t.Errorf("届いた %d 行が期待と違う。先頭 3 行 %q, 末尾 %q", len(got), got[:min(3, len(got))], got[len(got)-1])
	}
}

// TestView_DropCountResets は、捨てた数を印に出したら 0 に戻し、同じ数を 2 度出さないことを見る。
func TestView_DropCountResets(t *testing.T) {
	const capacity = 1024

	v := NewView()
	rec := newGatedRecorder()
	registerGated(t, v, all, rec)

	v.Write(srcWeb, []byte("first\n"))
	rec.waitEntered(t)
	within(t, "止まった宛先がある View への Write", func() {
		for i := range capacity + 1 {
			v.Write(srcWeb, fmt.Appendf(nil, "n=%d\n", i))
		}
	})
	rec.open()
	rec.waitLines(t, 1+1+capacity)

	v.Write(srcWeb, []byte("after\n"))
	got := rec.waitLines(t, 1+1+capacity+1)
	if last := got[len(got)-1]; last != "web:0/1 | after" {
		t.Errorf("最後の行 = %q, want %q", last, "web:0/1 | after")
	}
	marks := 0
	for _, l := range got {
		if strings.HasPrefix(l, "[... ") {
			marks++
		}
	}
	if marks != 1 {
		t.Errorf("印が %d 行出た, want 1 行", marks)
	}
}

// TestView_StalledDestinationDoesNotBlockOthers は、止まった宛先が、ほかの宛先への配送を止めないことを見る。
// 書く goroutine を View 全体で 1 本にすると、止まった宛先の Write から戻らない間、ほかの宛先にも届かない。
func TestView_StalledDestinationDoesNotBlockOthers(t *testing.T) {
	v := NewView()
	stalled, live := newGatedRecorder(), newRecorder()
	registerGated(t, v, all, stalled)
	unregLive := v.Register(all, live)
	defer unregLive()

	v.Write(srcWeb, []byte("n=0\n"))
	stalled.waitEntered(t)
	for i := 1; i <= 10; i++ {
		v.Write(srcWeb, fmt.Appendf(nil, "n=%d\n", i))
	}

	got := live.waitLines(t, 11)
	if len(got) != 11 || got[10] != "web:0/1 | n=10" {
		t.Errorf("止まっていない宛先に届いた行 = %q, want n=0 から n=10 の 11 行", got)
	}
}

// TestView_BatchesQueuedLines は、channel にたまった行を 1 回の Write にまとめて書くことを見る。
// readline の rl.Stdout() は Write のたびにプロンプトの行を消して描き直すので、1 行ずつ書くと描き直しが行の数だけ起きる。
func TestView_BatchesQueuedLines(t *testing.T) {
	v := NewView()
	rec := newGatedRecorder()
	registerGated(t, v, all, rec)

	v.Write(srcWeb, []byte("first\n"))
	rec.waitEntered(t)
	for i := range 10 {
		v.Write(srcWeb, fmt.Appendf(nil, "n=%d\n", i))
	}
	rec.open()
	rec.waitLines(t, 11)

	if n := rec.writeCount(); n != 2 {
		t.Errorf("Write が %d 回呼ばれた, want 2 回（最初の行で 1 回、たまった 10 行で 1 回）", n)
	}
}

// TestView_CopiesLine は、View が受け取った line をコピーしてから channel に送ることを見る。
// pump が渡す line は ReadSlice の戻り値で、次の読み込みで上書きされる。
func TestView_CopiesLine(t *testing.T) {
	v := NewView()
	rec := newGatedRecorder()
	registerGated(t, v, all, rec)

	v.Write(srcWeb, []byte("first\n"))
	rec.waitEntered(t)

	buf := []byte("aaa\n")
	v.Write(srcWeb, buf)
	copy(buf, "bbb\n") // 書く goroutine が整形する前に、呼び出し側が buf を使い回す

	rec.open()
	got := rec.waitLines(t, 2)
	if got[1] != "web:0/1 | aaa" {
		t.Errorf("2 行目 = %q, want %q", got[1], "web:0/1 | aaa")
	}
}

// TestView_WidthPerRegistration は、プレフィックスの幅を登録ごとに持つことを見る。
// 先に登録した宛先は長いプレフィックスを見たので幅を広げ、後から登録した宛先は短いものしか見ていないので広げない。
func TestView_WidthPerRegistration(t *testing.T) {
	v := NewView()
	early := newRecorder()
	unregEarly := v.Register(all, early)
	defer unregEarly()

	v.Write(Source{Name: "workers", Index: 3, Pid: 41050}, []byte("start\n"))
	early.waitLines(t, 1)

	late := newRecorder()
	unregLate := v.Register(all, late)
	defer unregLate()

	v.Write(Source{Name: "web", Index: 0, Pid: 4101}, []byte("ready\n"))

	if got, want := early.waitLines(t, 2), []string{
		"workers:3/41050 | start",
		"web:0/4101      | ready",
	}; !slices.Equal(got, want) {
		t.Errorf("先に登録した宛先 = %q, want %q", got, want)
	}
	if got, want := late.waitLines(t, 1), []string{"web:0/4101 | ready"}; !slices.Equal(got, want) {
		t.Errorf("後から登録した宛先 = %q, want %q", got, want)
	}
}

// TestView_Unregister は、登録を外すと宛先に書かなくなり、外した後の Write も安全に戻ることを見る。
// 外す関数を 2 回呼んでも panic しない（defer と明示の呼び出しが重なる場合）。
func TestView_Unregister(t *testing.T) {
	v := NewView()
	rec := newRecorder()
	unreg := v.Register(all, rec)

	v.Write(srcWeb, []byte("before\n"))
	rec.waitLines(t, 1)

	unreg()
	within(t, "外した後の Write", func() { v.Write(srcWeb, []byte("after\n")) })
	unreg()

	time.Sleep(50 * time.Millisecond)
	if got := rec.lines(); !slices.Equal(got, []string{"web:0/1 | before"}) {
		t.Errorf("外した後も宛先に書いた: %q", got)
	}
}

// TestView_UnregisterWhileStalled は、宛先の Write が戻らなくても、登録を外す関数が戻ることを見る。
// シェルは q で戻るときにこれを呼ぶ。attach では、読むのをやめたクライアントを外すときにも呼ぶ。
func TestView_UnregisterWhileStalled(t *testing.T) {
	v := NewView()
	rec := newGatedRecorder()
	unreg := registerGated(t, v, all, rec)

	v.Write(srcWeb, []byte("first\n"))
	rec.waitEntered(t)
	v.Write(srcWeb, []byte("queued\n"))

	within(t, "宛先が止まっている登録の解除", unreg)
	within(t, "外した後の Write", func() { v.Write(srcWeb, []byte("after\n")) })
}

// TestView_ConcurrentWriters は、複数の pump が同時に Write しても、行が欠けず、
// 同じ出どころの行の順序が保たれることを見る。go test -race で走らせる。
// 4 本 × 200 行は channel の容量（1024 行）に収まるので、捨てる行は出ない。
func TestView_ConcurrentWriters(t *testing.T) {
	const writers, perWriter = 4, 200

	v := NewView()
	rec := newRecorder()
	unreg := v.Register(all, rec)
	defer unreg()

	var wg sync.WaitGroup
	for w := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			src := Source{Name: "p", Index: w, Pid: 100 + w}
			for i := range perWriter {
				v.Write(src, fmt.Appendf(nil, "n=%d\n", i))
			}
		}()
	}
	wg.Wait()

	got := rec.waitLines(t, writers*perWriter)
	if len(got) != writers*perWriter {
		t.Fatalf("%d 行届いた, want %d 行", len(got), writers*perWriter)
	}
	next := make([]int, writers)
	for _, l := range got {
		var w, pid, n int
		if _, err := fmt.Sscanf(l, "p:%d/%d | n=%d", &w, &pid, &n); err != nil {
			t.Fatalf("行の形が違う: %q (%v)", l, err)
		}
		if n != next[w] {
			t.Fatalf("p:%d の行の順序が崩れた: n=%d が来た, want n=%d", w, n, next[w])
		}
		next[w]++
	}
}
