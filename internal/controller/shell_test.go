package controller

import (
	"fmt"
	"io"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/chzyer/readline"
)

// wait は、テストが止まったと判断するまでの時間。readline は端末なしでもミリ秒で応答するので、
// これを超えたら Run がどこかで止まっている。
const wait = time.Second

// testShell は、端末の代わりに io.Pipe をつないだ Shell と、ループの役をするための channel を持つ。
type testShell struct {
	sh     *Shell
	in     *io.PipeWriter // 書いた行が Readline() に届く。Close すると Readline() が io.EOF を返す（Ctrl-D と同じ）
	events chan event     // 受信しなければ、シェルの送信はそこで止まる
	done   chan struct{}
	ret    chan error // Run の戻り値。Run の中で panic したら、その値を error にして入れる

	closeDone sync.Once
	closeIn   sync.Once

	writeMu   sync.Mutex
	lastWrite chan struct{} // 直前の write の goroutine が書き終えたら閉じる
}

// newTestShell は、Shell を作って Run を別の goroutine で始める。
//
// NewShell は readline の Config を中で決めていて、入出力を差し替えられない。
// そこで、NewShell と同じフィールドの Shell をここで組み立てる。
// FuncIsTerminal で端末ではないと答えると、readline は raw モードに入らず、Stdin から 1 行ずつ読む。
//
// readline の Close は渡した Stdin を閉じないので、最初の Close はパイプの書き端が閉じるまで戻らない。
// 見張り goroutine がその Close で止まったまま残るので、後始末で書き端を閉じる。
func newTestShell(t *testing.T) *testShell {
	t.Helper()

	pr, pw := io.Pipe()
	rl, err := readline.NewEx(&readline.Config{
		Prompt:         "taskmaster> ",
		Stdin:          pr,
		Stdout:         io.Discard,
		Stderr:         io.Discard,
		FuncIsTerminal: func() bool { return false },
	})
	if err != nil {
		t.Fatalf("readline.NewEx: %v", err)
	}

	ts := &testShell{
		in:     pw,
		events: make(chan event),
		done:   make(chan struct{}),
		ret:    make(chan error, 1),
	}
	ts.sh = &Shell{
		rl:     rl,
		events: ts.events,
		done:   ts.done,
		exited: make(chan struct{}),
	}

	go func() {
		defer func() {
			if r := recover(); r != nil {
				ts.ret <- fmt.Errorf("Run が panic した: %v", r)
			}
		}()
		ts.ret <- ts.sh.Run()
	}()

	t.Cleanup(func() {
		ts.closeLoop()
		ts.eof()
	})
	return ts
}

// write は、ユーザーが 1 行打って Enter を押したのと同じ。
//
// io.Pipe への書き込みは、読み手が読むまで戻らない。シェルが送信や応答待ちで止まっていると
// テストも止まるので、別の goroutine で書く。書き込みの順番は、この呼び出しの順番に揃える。
func (ts *testShell) write(t *testing.T, line string) {
	t.Helper()
	ts.writeMu.Lock()
	prev := ts.lastWrite
	next := make(chan struct{})
	ts.lastWrite = next
	ts.writeMu.Unlock()

	go func() {
		defer close(next)
		if prev != nil {
			<-prev
		}
		io.WriteString(ts.in, line+"\n") // 後始末で書き端を閉じると ErrClosedPipe になるが、そのときはもう見ない
	}()
}

// eof は Ctrl-D と同じ。
func (ts *testShell) eof() { ts.closeIn.Do(func() { ts.in.Close() }) }

// closeLoop は、ループの Run() が抜けて done を閉じたのと同じ。
func (ts *testShell) closeLoop() { ts.closeDone.Do(func() { close(ts.done) }) }

// recvRequest は、ループの役として要求を 1 つ受け取る。
func (ts *testShell) recvRequest(t *testing.T) evRequest {
	t.Helper()
	select {
	case ev := <-ts.events:
		req, ok := ev.(evRequest)
		if !ok {
			t.Fatalf("要求ではないイベントが届いた: %#v", ev)
		}
		return req
	case err := <-ts.ret:
		t.Fatalf("要求が届く前に Run が戻った: %v", err)
	case <-time.After(wait):
		t.Fatalf("%v 待っても要求が届かない", wait)
	}
	panic("unreachable")
}

// noRequest は、wait の間に要求が届かないことを確かめる。
func (ts *testShell) noRequest(t *testing.T) {
	t.Helper()
	select {
	case ev := <-ts.events:
		t.Fatalf("要求を送らないはずが、届いた: %#v", ev)
	case err := <-ts.ret:
		t.Fatalf("Run が戻った: %v", err)
	case <-time.After(wait / 10):
	}
}

// waitExit は、Run が戻り、Exited() が閉じることを確かめて、Run の戻り値を返す。
func (ts *testShell) waitExit(t *testing.T) error {
	t.Helper()
	var err error
	select {
	case err = <-ts.ret:
	case <-time.After(wait):
		t.Fatalf("%v 待っても Run が戻らない", wait)
	}
	select {
	case <-ts.sh.Exited():
	case <-time.After(wait):
		t.Fatalf("Run は戻ったが、Exited() が閉じない")
	}
	return err
}

// TestShell_LineSendsRequest は、打った 1 行が解析済みの要求として 1 つ届くことを見る。
// 応答を返したあと、次の行も読むことまで確かめる。
func TestShell_LineSendsRequest(t *testing.T) {
	ts := newTestShell(t)

	ts.write(t, "start nginx db:0")
	req := ts.recvRequest(t)
	if req.cmd != CmdStart {
		t.Errorf("cmd = %v, want CmdStart(%v)", req.cmd, CmdStart)
	}
	if want := []string{"nginx", "db:0"}; !slices.Equal(req.targets, want) {
		t.Errorf("targets = %q, want %q", req.targets, want)
	}
	if req.reply == nil {
		t.Fatal("reply が nil。ループは応答を返せない")
	}
	req.reply <- nil

	ts.write(t, "status")
	if req := ts.recvRequest(t); req.cmd != CmdStatus {
		t.Errorf("2 行目の cmd = %v, want CmdStatus(%v)", req.cmd, CmdStatus)
	}
}

// TestShell_BlankLineSendsNothing は、空行と空白だけの行をループに送らず、プロンプトに戻ることを見る。
// 次の行が普通に届けば、シェルは落ちずに読み続けている。
func TestShell_BlankLineSendsNothing(t *testing.T) {
	ts := newTestShell(t)

	ts.write(t, "")
	ts.write(t, "   \t ")
	ts.write(t, "status")
	if req := ts.recvRequest(t); req.cmd != CmdStatus {
		t.Errorf("空行のあとの最初の要求の cmd = %v, want CmdStatus(%v)", req.cmd, CmdStatus)
	}
}

// TestShell_HelpStaysInShell は、help をループに送らないことを見る。
// help の文面はディスパッチ表から作れて、Controller の状態を読まないから。
func TestShell_HelpStaysInShell(t *testing.T) {
	ts := newTestShell(t)

	ts.write(t, "help")
	ts.noRequest(t)
}

// TestShell_EOFSendsQuit は、Ctrl-D で quit を 1 つ送り、Run が nil で戻ることを見る。
// シェルは自分で終了処理をせず、quit の要求でループの shutdown に合流させる。
func TestShell_EOFSendsQuit(t *testing.T) {
	ts := newTestShell(t)

	ts.eof()
	if req := ts.recvRequest(t); req.cmd != CmdQuit {
		t.Errorf("cmd = %v, want CmdQuit(%v)", req.cmd, CmdQuit)
	}
	if err := ts.waitExit(t); err != nil {
		t.Errorf("Run = %v, want nil", err)
	}
}

// TestShell_DoneWhileReading は、Readline() で入力を待っている間に done が閉じたら、
// quit を送らずに Run が戻ることを見る。
//
// done が閉じたときには、ループはもう events を受信していない。ここで quit を送ると、
// 受け手のいない channel への送信で止まり、main が <-Exited() から先に進まない。
//
// 1 行も読ませずに done を閉じると、-race が readline の中の data race を報告する。readline v1.5.1 の
// NewTerminal は go t.ioloop() を起動し、その goroutine の中で wg.Add(1) を呼ぶ。Close の wg.Wait との間に
// 同期が無いので、時間を空けても報告される。1 行読ませると、ioloop から Readline() の戻り値を経て
// このテストまで同期がつながり、報告は消える。
func TestShell_DoneWhileReading(t *testing.T) {
	ts := newTestShell(t)

	ts.write(t, "status")
	ts.recvRequest(t).reply <- nil
	ts.closeLoop()
	ts.waitExit(t)
}

// TestShell_DoneWhileSending は、要求を送ろうとしている間に done が閉じたら、Run が戻ることを見る。
// ループが受信しないまま抜けた場合に当たる。
func TestShell_DoneWhileSending(t *testing.T) {
	ts := newTestShell(t)

	ts.write(t, "status")
	time.Sleep(wait / 10) // シェルが送信で止まるまで待つ。誰も events を受信しない
	ts.closeLoop()
	ts.waitExit(t)
}

// TestShell_DoneWhileWaitingReply は、応答を待っている間に done が閉じたら、Run が戻ることを見る。
// ループが要求を受け取ったあと、応答を返さずに抜けた場合に当たる。
func TestShell_DoneWhileWaitingReply(t *testing.T) {
	ts := newTestShell(t)

	ts.write(t, "status")
	ts.recvRequest(t) // 応答は返さない
	ts.closeLoop()
	ts.waitExit(t)
}

// TestShell_WaitsReplyBeforeNextLine は、応答が返るまで次の要求を送らないことを見る。
// 先に次の行を送ると、応答の表示と次のプロンプトの順番が入れ替わる。
func TestShell_WaitsReplyBeforeNextLine(t *testing.T) {
	ts := newTestShell(t)

	ts.write(t, "status")
	req := ts.recvRequest(t)
	ts.write(t, "status")
	ts.noRequest(t)

	req.reply <- nil
	if req := ts.recvRequest(t); req.cmd != CmdStatus {
		t.Errorf("応答のあとの cmd = %v, want CmdStatus(%v)", req.cmd, CmdStatus)
	}
}

func TestCheckTarget(t *testing.T) {
	tests := []struct {
		target string
		ok     bool
	}{
		{"all", true},
		{"app", true},
		{"app:0", true},
		{"app:12", true},
		{"all:0", false},
		{":0", false},
		{"app:", false},
		{"app:-1", false},
		{"app:+1", false},
		{"app:x", false},
		{"app:0:1", false},
	}
	for _, tt := range tests {
		err := checkTarget(tt.target)
		if (err == nil) != tt.ok {
			t.Errorf("checkTarget(%q) = %v, want ok=%v", tt.target, err, tt.ok)
		}
	}
}
