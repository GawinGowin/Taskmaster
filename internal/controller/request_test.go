package controller

import (
	"bytes"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"taskmaster/internal/config"
	"taskmaster/internal/process"
)

// newLoop は programs から Controller を作り、Run を別の goroutine で始める。イベントログは捨てる。
//
// どの program も autostart: false、stdout / stderr を空にして渡すこと。
// そうすると出力先のファイルは作られず、start を送るまで子プロセスも起動しない。
// 後始末で quit を送り、Run が返るのを wait まで待つ。テストが先に quit を送って Run が返っていれば、何もしない。
// 子を起動するテストは、quit から wait 以内に全プロセスが止まる stopsignal / stoptime にするか、
// テストの中で quitAndWait を使って Run が返るのを待つこと。
func newLoop(t *testing.T, programs map[string]config.Program) *Controller {
	t.Helper()
	return newLoopWithLogger(t, programs, slog.New(slog.DiscardHandler))
}

// newLoopWithLogger は、イベントログを logger に書く newLoop。
func newLoopWithLogger(t *testing.T, programs map[string]config.Program, logger *slog.Logger) *Controller {
	t.Helper()
	c, err := New(&config.Config{Programs: programs})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	go c.Run(logger)
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

// TestResolve は、対象の引数をプロセスとエラーの行に展開する規則を見る。
// New だけを呼び、Run は動かさない。resolve はループの外から呼んでも、状態を読み書きしない。
//
// program は名前の昇順と numprocs の 2 桁を見るために 3 つ置く。
// w の numprocs を 12 にするのは、文字列の昇順だと w:10 が w:2 より前に来るから。
func TestResolve(t *testing.T) {
	c, err := New(&config.Config{Programs: map[string]config.Program{
		"w":       idle(12),
		"healthy": idle(2),
		"a":       idle(1),
	}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	allIDs := []string{"a:0", "healthy:0", "healthy:1"}
	for i := range 12 {
		allIDs = append(allIDs, fmt.Sprintf("w:%d", i))
	}

	for _, tc := range []struct {
		name     string
		targets  []string
		wantIDs  []string
		wantErrs []string
	}{
		{
			name:    "all は program 名の昇順、その中は index の数の昇順",
			targets: []string{"all"},
			wantIDs: allIDs,
		},
		{
			name:    "name はその program の全プロセス",
			targets: []string{"healthy"},
			wantIDs: []string{"healthy:0", "healthy:1"},
		},
		{
			name:    "重複は 2 回目以降を捨て、最初に出てきた位置に並ぶ",
			targets: []string{"healthy:1", "healthy"},
			wantIDs: []string{"healthy:1", "healthy:0"},
		},
		{
			name:    "重複は ID で見るので、先頭に 0 を付けた index も同じプロセス",
			targets: []string{"healthy:1", "healthy:01"},
			wantIDs: []string{"healthy:1"},
		},
		{
			name:    "all の前に書いたプロセスは先頭に来て、all の中では捨てる",
			targets: []string{"w:11", "all"},
			wantIDs: append([]string{"w:11"}, slices.DeleteFunc(slices.Clone(allIDs), func(id string) bool { return id == "w:11" })...),
		},
		{
			name:     "エラーの行は重複を捨てない",
			targets:  []string{"nosuch", "nosuch"},
			wantErrs: []string{"nosuch: ERROR (no such program)", "nosuch: ERROR (no such program)"},
		},
		{
			name:     "index が numprocs 以上なら範囲外",
			targets:  []string{"healthy:2"},
			wantErrs: []string{"healthy:2: ERROR (no such process, numprocs is 2)"},
		},
		{
			name:     "index が int を桁あふれしても範囲外",
			targets:  []string{"healthy:99999999999999999999"},
			wantErrs: []string{"healthy:99999999999999999999: ERROR (no such process, numprocs is 2)"},
		},
		{
			name:     "エラーの引数があっても、ほかの引数は展開する",
			targets:  []string{"nosuch", "a", "a:1"},
			wantIDs:  []string{"a:0"},
			wantErrs: []string{"nosuch: ERROR (no such program)", "a:1: ERROR (no such process, numprocs is 1)"},
		},
		{
			name:    "引数なしは何も返さない",
			targets: nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, tg := range tc.targets {
				if err := checkTarget(tg); err != nil {
					t.Fatalf("テストの入力 %q が checkTarget を通らない: %v", tg, err)
				}
			}
			procs, errs := c.resolve(tc.targets)
			var ids []string
			for _, p := range procs {
				ids = append(ids, p.ID())
			}
			if !slices.Equal(ids, tc.wantIDs) {
				t.Errorf("resolve(%q) のプロセス = %q, want %q", tc.targets, ids, tc.wantIDs)
			}
			if !slices.Equal(errs, tc.wantErrs) {
				t.Errorf("resolve(%q) のエラー = %q, want %q", tc.targets, errs, tc.wantErrs)
			}
		})
	}
}

// sleeper は、子を起動して STARTING に留まる program の設定を返す。
// starttime を 10 秒にして、テストの間に RUNNING へ進まないようにする。
// 後始末の quit で SIGTERM を送ると、sleep はすぐに終わる。
func sleeper(numprocs int) config.Program {
	return config.Program{
		Cmd:        config.Command{"sleep", "30"},
		Numprocs:   numprocs,
		Starttime:  10,
		Stopsignal: config.Stopsignal(syscall.SIGTERM),
		Stoptime:   1,
	}
}

// missing は、cmd が存在しないパスで、Process.Start() がその場でエラーを返す program の設定を返す。
func missing(startretries int) config.Program {
	return config.Program{Cmd: config.Command{"/nonexistent"}, Numprocs: 1, Startretries: startretries}
}

var pidRe = regexp.MustCompile(`pid \d+`)

// maskPid は、応答の行の pid を N に置き換える。pid は実行ごとに変わるので、行を丸ごと比べるために使う。
func maskPid(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = pidRe.ReplaceAllString(l, "pid N")
	}
	return out
}

// TestRequest_Start は、start の応答の行を見る。
// 引数は左から 1 つずつ処理し、前の引数で扱った ID は捨てる。
// 何もしなかったプロセスに行を出すのは個体指定だけで、まとめ指定は行を出さない。
// まとめ指定で 1 プロセスも対象にならなかったときだけ nothing to start を出す。
//
// ケースごとにループを作り直し、setup の start を順に送ってから targets を送る。
// healthy は STARTING に留まるので、2 回目の start では何もしない。
func TestRequest_Start(t *testing.T) {
	const spawnErr = "fork/exec /nonexistent: no such file or directory"

	for _, tc := range []struct {
		name    string
		setup   [][]string
		targets []string
		want    []string
	}{
		{
			name:    "STOPPED のプロセスを起動する",
			targets: []string{"healthy"},
			want: []string{
				"healthy:0: starting (pid N, gen 1)",
				"healthy:1: starting (pid N, gen 1)",
			},
		},
		{
			name:    "個体指定で何もしなかったら already を出す",
			setup:   [][]string{{"healthy:0"}},
			targets: []string{"healthy:0", "healthy"},
			want: []string{
				"healthy:0: ERROR (already STARTING)",
				"healthy:1: starting (pid N, gen 1)",
			},
		},
		{
			name:    "前の引数で扱ったプロセスがあれば、まとめ指定は nothing to start を出さない",
			setup:   [][]string{{"healthy"}},
			targets: []string{"healthy:0", "healthy"},
			want:    []string{"healthy:0: ERROR (already STARTING)"},
		},
		{
			name:    "まとめ指定が先なら、後ろの個体指定は捨てる",
			setup:   [][]string{{"healthy:0"}},
			targets: []string{"healthy", "healthy:0"},
			want:    []string{"healthy:1: starting (pid N, gen 1)"},
		},
		{
			name:    "まとめ指定で 1 プロセスも対象にならなければ nothing to start",
			setup:   [][]string{{"healthy"}},
			targets: []string{"healthy"},
			want:    []string{"healthy: nothing to start"},
		},
		{
			name:    "起動がその場で失敗したら spawn failed と BACKOFF を出し、ERROR を付けない",
			targets: []string{"nocmd"},
			want:    []string{"nocmd:0: spawn failed (" + spawnErr + "), now BACKOFF"},
		},
		{
			name:    "startretries が 0 なら spawn failed のあと FATAL",
			targets: []string{"nocmd0"},
			want:    []string{"nocmd0:0: spawn failed (" + spawnErr + "), now FATAL"},
		},
		{
			name:    "BACKOFF のプロセスは起動しない",
			setup:   [][]string{{"nocmd"}},
			targets: []string{"nocmd:0", "nocmd"},
			want:    []string{"nocmd:0: ERROR (already BACKOFF)"},
		},
		{
			name:    "FATAL のプロセスは起動し直す",
			setup:   [][]string{{"nocmd0"}},
			targets: []string{"nocmd0"},
			want:    []string{"nocmd0:0: spawn failed (" + spawnErr + "), now FATAL"},
		},
		{
			name:    "エラーの引数には nothing to start を出さない",
			targets: []string{"nosuch", "healthy:5"},
			want: []string{
				"nosuch: ERROR (no such program)",
				"healthy:5: ERROR (no such process, numprocs is 2)",
			},
		},
		{
			name:    "エラーの引数があっても、ほかの引数は起動する",
			targets: []string{"nosuch", "healthy:1"},
			want: []string{
				"nosuch: ERROR (no such program)",
				"healthy:1: starting (pid N, gen 1)",
			},
		},
		{
			name:    "対象が無ければ requires a target",
			targets: nil,
			want:    []string{"start: requires a target"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newLoop(t, map[string]config.Program{
				"healthy": sleeper(2),
				"nocmd":   missing(3),
				"nocmd0":  missing(0),
			})
			for _, s := range tc.setup {
				request(t, c, CmdStart, s...)
			}
			got := maskPid(request(t, c, CmdStart, tc.targets...))
			if !slices.Equal(got, tc.want) {
				t.Errorf("start %q = %q, want %q", tc.targets, got, tc.want)
			}
		})
	}
}

// TestRequest_StartWhileShuttingDown は、shutdown が始まったあとの start に、引数ごとに shutting down を返すことを見る。
//
// shutdown の間に要求を受け付けさせるため、停止シグナルを SIGWINCH にして STOPPING に留める。
// sleep は SIGWINCH を無視するので、stoptime の 1 秒後に SIGKILL で終わり、そこで Run が返る。
func TestRequest_StartWhileShuttingDown(t *testing.T) {
	p := sleeper(1)
	p.Stopsignal = config.Stopsignal(syscall.SIGWINCH)
	c := newLoop(t, map[string]config.Program{"slow": p})

	request(t, c, CmdStart, "slow")
	request(t, c, CmdQuit)
	got := request(t, c, CmdStart, "slow", "nosuch")
	want := []string{
		"slow: ERROR (shutting down)",
		"nosuch: ERROR (shutting down)",
	}
	if !slices.Equal(got, want) {
		t.Errorf("shutdown 中の start slow nosuch = %q, want %q", got, want)
	}

	// 後始末の newLoop は Run が返るのを wait（1 秒）しか待たないので、SIGKILL の分をここで待つ。
	select {
	case <-c.done:
	case <-time.After(3 * time.Second):
		t.Fatalf("quit から 3 秒待っても Run が返らない")
	}
}

// stubborn は、停止シグナルを受けても終わらず、STOPPING に留まる program の設定を返す。
// sleep は SIGWINCH を無視するので、stoptime の 1 秒後に SIGKILL で終わる。
func stubborn() config.Program {
	p := sleeper(1)
	p.Stopsignal = config.Stopsignal(syscall.SIGWINCH)
	return p
}

// quitAndWait は quit を送り、Run が返るのを d まで待つ。
// newLoop の後始末は wait（1 秒）しか待たないので、STOPPING のプロセスが SIGKILL で終わるのを待つときに使う。
func quitAndWait(t *testing.T, c *Controller, d time.Duration) {
	t.Helper()
	request(t, c, CmdQuit)
	select {
	case <-c.done:
	case <-time.After(d):
		t.Fatalf("quit から %v 待っても Run が返らない", d)
	}
}

// stateOf は、status で id の状態の欄を読む。
// ループの外から Process を直接読むと単一ライターを破るので、status の応答を通す。
func stateOf(t *testing.T, c *Controller, id string) string {
	t.Helper()
	got := request(t, c, CmdStatus, id)
	if len(got) != 2 {
		t.Fatalf("status %s = %q, want 見出しと 1 行", id, got)
	}
	return strings.Fields(got[1])[1]
}

// awaitState は、id の状態が want になるまで status を打ち直して待つ。
func awaitState(t *testing.T, c *Controller, id string, want process.State) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		got := stateOf(t, c, id)
		if got == want.String() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: 3 秒待っても %s にならない（今は %s）", id, want, got)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type step struct {
	cmd     Command
	targets []string
}

// TestRequest_Stop は、stop の応答の行を見る。
// 引数の扱いは start と同じで、左から 1 つずつ処理し、前の引数で扱った ID は捨てる。
// 何もしなかったプロセスに行を出すのは個体指定だけで、まとめ指定は行を出さない。
// まとめ指定で 1 プロセスも対象にならなかったときだけ nothing to stop を出す。
//
// ケースごとにループを作り直し、setup を順に送ってから targets の stop を送る。
// healthy は STARTING に留まり、SIGTERM ですぐ終わる。stubborn は STOPPING に留まる。
func TestRequest_Stop(t *testing.T) {
	usr1 := sleeper(1)
	usr1.Stopsignal = config.Stopsignal(syscall.SIGUSR1)
	usr1.Stoptime = 10

	for _, tc := range []struct {
		name    string
		setup   []step
		targets []string
		want    []string
	}{
		{
			name:    "STARTING のプロセスに停止シグナルを送る",
			setup:   []step{{CmdStart, []string{"healthy"}}},
			targets: []string{"healthy"},
			want: []string{
				"healthy:0: stopping (SIGTERM, SIGKILL in 1.0s)",
				"healthy:1: stopping (SIGTERM, SIGKILL in 1.0s)",
			},
		},
		{
			name:    "停止シグナルと stoptime は設定の値を出す",
			setup:   []step{{CmdStart, []string{"usr1"}}},
			targets: []string{"usr1:0"},
			want:    []string{"usr1:0: stopping (SIGUSR1, SIGKILL in 10.0s)"},
		},
		{
			name:    "BACKOFF のプロセスは stopped を出し、まとめ指定でも nothing to stop を出さない",
			setup:   []step{{CmdStart, []string{"nocmd"}}},
			targets: []string{"nocmd"},
			want:    []string{"nocmd:0: stopped"},
		},
		{
			name:    "STOPPED のプロセスを個体指定したら already を出す",
			targets: []string{"healthy:0"},
			want:    []string{"healthy:0: ERROR (already STOPPED)"},
		},
		{
			name:    "FATAL のプロセスを個体指定したら already を出す",
			setup:   []step{{CmdStart, []string{"nocmd0"}}},
			targets: []string{"nocmd0:0"},
			want:    []string{"nocmd0:0: ERROR (already FATAL)"},
		},
		{
			name: "STOPPING のプロセスを個体指定したら already を出す",
			setup: []step{
				{CmdStart, []string{"stubborn"}},
				{CmdStop, []string{"stubborn"}},
			},
			targets: []string{"stubborn:0"},
			want:    []string{"stubborn:0: ERROR (already STOPPING)"},
		},
		{
			name: "STOPPING のプロセスはまとめ指定では行を出さない",
			setup: []step{
				{CmdStart, []string{"stubborn"}},
				{CmdStop, []string{"stubborn"}},
			},
			targets: []string{"stubborn"},
			want:    []string{"stubborn: nothing to stop"},
		},
		{
			name:    "まとめ指定で 1 プロセスも対象にならなければ nothing to stop",
			targets: []string{"healthy"},
			want:    []string{"healthy: nothing to stop"},
		},
		{
			name:    "まとめ指定で何もしなかったプロセスには行を出さない",
			setup:   []step{{CmdStart, []string{"healthy:1"}}},
			targets: []string{"healthy"},
			want:    []string{"healthy:1: stopping (SIGTERM, SIGKILL in 1.0s)"},
		},
		{
			name:    "前の引数で扱ったプロセスがあれば、まとめ指定は nothing to stop を出さない",
			targets: []string{"healthy:0", "healthy"},
			want:    []string{"healthy:0: ERROR (already STOPPED)"},
		},
		{
			name:    "まとめ指定が先なら、後ろの個体指定は捨てる",
			setup:   []step{{CmdStart, []string{"healthy:0"}}},
			targets: []string{"healthy", "healthy:0"},
			want:    []string{"healthy:0: stopping (SIGTERM, SIGKILL in 1.0s)"},
		},
		{
			name:    "all は program 名の昇順に、止めたプロセスだけ行を出す",
			setup:   []step{{CmdStart, []string{"nocmd", "healthy:1"}}},
			targets: []string{"all"},
			want: []string{
				"healthy:1: stopping (SIGTERM, SIGKILL in 1.0s)",
				"nocmd:0: stopped",
			},
		},
		{
			name:    "エラーの引数には nothing to stop を出さない",
			targets: []string{"nosuch", "healthy:5"},
			want: []string{
				"nosuch: ERROR (no such program)",
				"healthy:5: ERROR (no such process, numprocs is 2)",
			},
		},
		{
			name:    "エラーの引数があっても、ほかの引数は止める",
			setup:   []step{{CmdStart, []string{"healthy:1"}}},
			targets: []string{"nosuch", "healthy:1"},
			want: []string{
				"nosuch: ERROR (no such program)",
				"healthy:1: stopping (SIGTERM, SIGKILL in 1.0s)",
			},
		},
		{
			name:    "対象が無ければ requires a target",
			targets: nil,
			want:    []string{"stop: requires a target"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newLoop(t, map[string]config.Program{
				"healthy":  sleeper(2),
				"usr1":     usr1,
				"stubborn": stubborn(),
				"nocmd":    missing(3),
				"nocmd0":   missing(0),
			})
			for _, s := range tc.setup {
				request(t, c, s.cmd, s.targets...)
			}
			got := request(t, c, CmdStop, tc.targets...)
			if !slices.Equal(got, tc.want) {
				t.Errorf("stop %q = %q, want %q", tc.targets, got, tc.want)
			}
			quitAndWait(t, c, 3*time.Second)
		})
	}
}

// TestRequest_StopRunning は、RUNNING のプロセスを stop すると、
// 停止シグナルで終わったあと EXITED ではなく STOPPED に入り、autorestart: always でも起動し直さないことを見る。
// シグナル死は想定外の終了なので、RUNNING のまま死んだ扱いになれば EXITED を経て STARTING に戻る。
//
// starttime を 0 にして、起動の直後に RUNNING へ進める。
func TestRequest_StopRunning(t *testing.T) {
	p := sleeper(1)
	p.Starttime = 0
	p.Autorestart = config.Always
	c := newLoop(t, map[string]config.Program{"quick": p})

	request(t, c, CmdStart, "quick")
	awaitState(t, c, "quick:0", process.Running)

	got := request(t, c, CmdStop, "quick")
	want := []string{"quick:0: stopping (SIGTERM, SIGKILL in 1.0s)"}
	if !slices.Equal(got, want) {
		t.Errorf("stop quick = %q, want %q", got, want)
	}
	awaitState(t, c, "quick:0", process.Stopped)
}

// TestRequest_StopBackoff は、BACKOFF のプロセスを stop すると、その場で STOPPED に入り、
// 再試行の予約が後から届いても起動し直さないことを見る。
// 1 回目の失敗の再試行は backoffUnit 後に届くので、その 2 倍待ってから状態を見る。
func TestRequest_StopBackoff(t *testing.T) {
	c := newLoop(t, map[string]config.Program{"nocmd": missing(3)})

	request(t, c, CmdStart, "nocmd")
	request(t, c, CmdStop, "nocmd")
	if got := stateOf(t, c, "nocmd:0"); got != process.Stopped.String() {
		t.Fatalf("stop の直後の状態 = %s, want STOPPED", got)
	}
	time.Sleep(2 * backoffUnit)
	if got := stateOf(t, c, "nocmd:0"); got != process.Stopped.String() {
		t.Errorf("再試行の予約が届いたあとの状態 = %s, want STOPPED のまま", got)
	}
}

// TestRequest_StopLogsByShell は、シェルからの stop が、イベントログの遷移の行に by=shell を付けることを見る。
// stop() の 2 つの分岐（STARTING → STOPPING と BACKOFF → STOPPED）を両方通す。
// ログを書くのは Run の goroutine だけなので、Run が返ってから読む。
func TestRequest_StopLogsByShell(t *testing.T) {
	var buf bytes.Buffer
	c := newLoopWithLogger(t, map[string]config.Program{
		"healthy": sleeper(1),
		"nocmd":   missing(3),
	}, slog.New(slog.NewTextHandler(&buf, nil)))

	request(t, c, CmdStart, "healthy", "nocmd")
	request(t, c, CmdStop, "healthy", "nocmd")
	quitAndWait(t, c, 3*time.Second)

	lines := strings.Split(buf.String(), "\n")
	for _, transition := range []string{
		"id=healthy:0 from=STARTING to=STOPPING",
		"id=nocmd:0 from=BACKOFF to=STOPPED",
	} {
		i := slices.IndexFunc(lines, func(l string) bool {
			return strings.Contains(l, transition)
		})
		if i < 0 {
			t.Errorf("イベントログに %q の行が無い\n%s", transition, buf.String())
			continue
		}
		line := lines[i]
		if !slices.Contains(strings.Fields(line), "by=shell") {
			t.Errorf("%q の行に by=shell が無い: %s", transition, line)
		}
	}
}

// TestRequest_StopWhileStarting は、STARTING で stop したプロセスが、starttime が過ぎても RUNNING に進まず、
// STOPPING に留まることを見る。starttime のタイマーは stop の前に設定されたもので、stop では取り消されない。
//
// 停止シグナルを SIGWINCH にして、stoptime（3 秒）の SIGKILL まで STOPPING に留める。
// starttime（1 秒）の 0.5 秒後に状態を見る。
func TestRequest_StopWhileStarting(t *testing.T) {
	p := sleeper(1)
	p.Starttime = 1
	p.Stoptime = 3
	p.Stopsignal = config.Stopsignal(syscall.SIGWINCH)
	c := newLoop(t, map[string]config.Program{"slow": p})

	request(t, c, CmdStart, "slow")
	request(t, c, CmdStop, "slow")
	time.Sleep(time.Duration(p.Starttime)*time.Second + 500*time.Millisecond)
	if got := stateOf(t, c, "slow:0"); got != process.Stopping.String() {
		t.Errorf("starttime が過ぎたあとの状態 = %s, want STOPPING のまま", got)
	}
	quitAndWait(t, c, 4*time.Second)
}

// TestRequest_StopBackoffThenStart は、BACKOFF で stop してから start し直したとき、
// stop の前の世代の再試行タイマーを、新しい世代の再試行として扱わないことを見る。
//
// 1 回目の start の再試行タイマーは backoffUnit 後に発火する。その半分の時点で start し直すと、
// 新しい世代の再試行は 2 回目の start から backoffUnit 後に届く。startretries が 1 なので、その再試行が失敗すると FATAL に入る。
// タイマーは予定より早く発火しないので、正しければ FATAL に入るのは 2 回目の start から backoffUnit 以上あと。
// 前の世代のタイマーを通してしまうと、その半分ほどで FATAL に入る。
func TestRequest_StopBackoffThenStart(t *testing.T) {
	c := newLoop(t, map[string]config.Program{"nocmd": missing(1)})

	request(t, c, CmdStart, "nocmd")
	request(t, c, CmdStop, "nocmd")
	time.Sleep(backoffUnit / 2)
	restarted := time.Now()
	request(t, c, CmdStart, "nocmd")
	awaitState(t, c, "nocmd:0", process.Fatal)
	if d := time.Since(restarted); d < backoffUnit {
		t.Errorf("2 回目の start から %v で FATAL に入った。再試行は %v 後に届くはず", d, backoffUnit)
	}
}

// TestRequest_StopWhileShuttingDown は、shutdown が始まったあとの stop を、shutting down で弾かずに普通に処理することを見る。
// shutdown がすべてのプロセスに停止シグナルを送るので、起動していたプロセスは STOPPING、起動していなかったプロセスは STOPPED になっている。
//
// 停止シグナルを SIGWINCH にして、stoptime（3 秒）の SIGKILL まで STOPPING に留める。
func TestRequest_StopWhileShuttingDown(t *testing.T) {
	p := stubborn()
	p.Stoptime = 3
	c := newLoop(t, map[string]config.Program{"slow": p, "never": idle(1)})

	request(t, c, CmdStart, "slow")
	request(t, c, CmdQuit)

	got := request(t, c, CmdStop, "slow", "never:0", "nosuch")
	want := []string{
		"slow: nothing to stop",
		"never:0: ERROR (already STOPPED)",
		"nosuch: ERROR (no such program)",
	}
	if !slices.Equal(got, want) {
		t.Errorf("shutdown 中の stop slow never:0 nosuch = %q, want %q", got, want)
	}

	got = request(t, c, CmdStop, "slow:0")
	want = []string{"slow:0: ERROR (already STOPPING)"}
	if !slices.Equal(got, want) {
		t.Errorf("shutdown 中の stop slow:0 = %q, want %q", got, want)
	}

	quitAndWait(t, c, 4*time.Second)
}
