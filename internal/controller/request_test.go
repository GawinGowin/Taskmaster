package controller

import (
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"taskmaster/internal/config"
)

// newLoop は programs から Controller を作り、Run を別の goroutine で始める。
//
// どの program も autostart: false、stdout / stderr を空にして渡すこと。
// そうすると子プロセスも出力先のファイルも作られず、全プロセスが STOPPED のままになる。
// 後始末で quit を送り、Run が返るのを待つ。テストが先に quit を送って Run が返っていれば、何もしない。
func newLoop(t *testing.T, programs map[string]config.Program) *Controller {
	t.Helper()
	c, err := New(&config.Config{Programs: programs})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	go c.Run(slog.New(slog.DiscardHandler))
	t.Cleanup(func() {
		select {
		case c.events <- evRequest{cmd: CmdQuit}:
		case <-c.done:
		}
		select {
		case <-c.done:
		case <-time.After(wait):
			t.Errorf("quit から %v 待っても Run が返らない", wait)
		}
		c.Close()
	})
	return c
}

// idle は、子を起動しない program の設定を返す。
func idle(numprocs int) config.Program {
	return config.Program{Cmd: config.Command{"true"}, Numprocs: numprocs}
}

// cmdName は失敗メッセージ用に、Command をシェルで打つ名前に戻す。
func cmdName(cmd Command) string {
	for name, c := range availableCommand {
		if c == cmd {
			return name
		}
	}
	return fmt.Sprintf("Command(%d)", int(cmd))
}

// request は要求を 1 つ送って応答を受け取り、そのあと reply が閉じていることを確かめる。
// 送信・応答・close のどれかが wait 以内に来なければ、テストを止める。
func request(t *testing.T, c *Controller, cmd Command, targets ...string) []string {
	t.Helper()
	name := cmdName(cmd)
	reply := make(chan []string)
	select {
	case c.events <- evRequest{cmd: cmd, targets: targets, reply: reply}:
	case <-time.After(wait):
		t.Fatalf("%s %v: 送信が %v 以内に終わらない", name, targets, wait)
	}

	var lines []string
	select {
	case r, ok := <-reply:
		if !ok {
			t.Fatalf("%s %v: 応答を送らずに reply を閉じた", name, targets)
		}
		lines = r
	case <-time.After(wait):
		t.Fatalf("%s %v: 応答が %v 以内に来ない", name, targets, wait)
	}

	select {
	case r, ok := <-reply:
		if ok {
			t.Fatalf("%s %v: 応答が 2 回来た。2 回目: %q", name, targets, r)
		}
	case <-time.After(wait):
		t.Fatalf("%s %v: 応答のあと reply が閉じない", name, targets)
	}
	return lines
}

// TestRequest_BadTarget は、形の誤った対象に、最初の誤りの 1 行だけを返すことを見る。
// ループは送り手を信用せず、シェルの parseLine と同じ checkTarget で検査する。
// 応答を送ったあとも、ループが次の要求を処理することを、続く status で確かめる。
func TestRequest_BadTarget(t *testing.T) {
	c := newLoop(t, map[string]config.Program{})

	for _, targets := range [][]string{
		{"a:x", ":0"},
		{"all:0"},
		{"a", ":0"},
	} {
		var want string
		for _, tg := range targets {
			if err := checkTarget(tg); err != nil {
				want = err.Error()
				break
			}
		}
		got := request(t, c, CmdStatus, targets...)
		if !slices.Equal(got, []string{want}) {
			t.Errorf("status %v = %q, want %q", targets, got, []string{want})
		}
	}
	request(t, c, CmdStatus)
}

// TestRequest_NilReply は、reply が nil の要求でループが止まらないことを見る。
// nil の channel への送信は永久に止まり、close は panic する。
// 今 nil の reply で届くのは Ctrl-D の quit だけだが、どの要求でも送る前に nil を見る。
func TestRequest_NilReply(t *testing.T) {
	c := newLoop(t, map[string]config.Program{})

	for _, targets := range [][]string{{"a:x"}, {"a"}, nil} {
		select {
		case c.events <- evRequest{cmd: CmdStatus, targets: targets}:
		case <-time.After(wait):
			t.Fatalf("status %v: 送信が %v 以内に終わらない", targets, wait)
		}
	}
	request(t, c, CmdStatus)
}

// TestRequest_EveryCommandReplies は、quit 以外のどのコマンドにも、空でない行を 1 行以上返すことを見る。
// 空の応答だと、シェルは空行を出すだけになり、成功したのか何もしなかったのかが画面から分からない。
// 対象には存在しない program を渡す。start / stop / restart を実装したあとも、子を起動せずに済む。
func TestRequest_EveryCommandReplies(t *testing.T) {
	c := newLoop(t, map[string]config.Program{"a": idle(1)})

	for _, tc := range []struct {
		cmd     Command
		targets []string
	}{
		{CmdStart, []string{"nosuch"}},
		{CmdStop, []string{"nosuch"}},
		{CmdRestart, []string{"nosuch"}},
		{CmdStatus, nil},
		{CmdReload, nil},
	} {
		got := request(t, c, tc.cmd, tc.targets...)
		if len(got) == 0 || slices.Contains(got, "") {
			t.Errorf("%s %v = %q, want 空でない行が 1 行以上", cmdName(tc.cmd), tc.targets, got)
		}
	}
}

// TestRequest_StatusErrorsAfterTable は、status のエラーの行が表の後ろに引数の順で並ぶことを見る。
// 全部エラーのときは見出し行を出さない。
func TestRequest_StatusErrorsAfterTable(t *testing.T) {
	c := newLoop(t, map[string]config.Program{"a": idle(2)})

	got := request(t, c, CmdStatus, "nosuch", "a:0", "a:5")
	wantErrs := []string{
		"nosuch: ERROR (no such program)",
		"a:5: ERROR (no such process, numprocs is 2)",
	}
	if len(got) != 4 {
		t.Fatalf("status nosuch a:0 a:5 = %q, want 見出し・a:0・エラー 2 行の 4 行", got)
	}
	if !strings.HasPrefix(got[0], "NAME") || !strings.HasPrefix(got[1], "a:0 ") {
		t.Errorf("表が先頭にない: %q", got)
	}
	if !slices.Equal(got[2:], wantErrs) {
		t.Errorf("エラーの行 = %q, want %q", got[2:], wantErrs)
	}

	got = request(t, c, CmdStatus, "nosuch", "a:5")
	if !slices.Equal(got, wantErrs) {
		t.Errorf("status nosuch a:5 = %q, want %q（見出し行なし）", got, wantErrs)
	}
}

// TestRequest_Quit は、quit に応答を返したあと Run が返り、done が閉じることを見る。
func TestRequest_Quit(t *testing.T) {
	c := newLoop(t, map[string]config.Program{"a": idle(1)})

	got := request(t, c, CmdQuit)
	if len(got) == 0 || !strings.HasPrefix(got[0], "shutting down") {
		t.Errorf("quit = %q, want shutting down で始まる行", got)
	}
	select {
	case <-c.done:
	case <-time.After(wait):
		t.Fatalf("quit の応答から %v 待っても done が閉じない", wait)
	}
}
