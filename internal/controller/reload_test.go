package controller

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"taskmaster/internal/config"
	"taskmaster/internal/process"
)

// reload のテストは、設定ファイルを一時ディレクトリに書き、Controller にそのパスを渡して始める。
// 設定を書き換えてから、シェルの reload（CmdReload の要求）か SIGHUP（evSignal）を送る。
// イベントログを書くのは Run の goroutine だけなので、ログは Run が返ってから読む。

// 設定の YAML を組み立てる部品。program のフィールドは 1 行ずつ渡す。
const (
	sleep30 = `cmd: ["sleep", "30"]`
	sleep31 = `cmd: ["sleep", "31"]`
	// TERM を無視するので、stoptime の 1 秒後に SIGKILL で終わる。
	// 無視の設定は exec をまたいで子の sleep にも引き継がれる。
	stubborn30 = `cmd: ["/bin/sh", "-c", "trap '' TERM; sleep 30"]`
	stubborn31 = `cmd: ["/bin/sh", "-c", "trap '' TERM; sleep 31"]`
	noop       = `cmd: ["true"]`
	// starttime: 0 で、起動するとすぐ RUNNING に進む。stoptime: 1 で、後始末の quit が 1 秒で終わる。
	quick    = "starttime: 0"
	stopFast = "stoptime: 1"
	manual   = "autostart: false"
)

func yamlConfig(programs ...string) string {
	return "programs:\n" + strings.Join(programs, "")
}

func yamlProgram(name string, fields ...string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "  %s:\n", name)
	for _, f := range fields {
		fmt.Fprintf(&b, "    %s\n", f)
	}
	return b.String()
}

type reloadLoop struct {
	c    *Controller
	path string
	log  *bytes.Buffer
}

// newReloadLoop は、src を設定ファイルに書いて Load し、そのパスで Controller を作って Run を始める。
// イベントログは log に書く。
func newReloadLoop(t *testing.T, src string) *reloadLoop {
	t.Helper()
	path := filepath.Join(t.TempDir(), "taskmasterd.yaml")
	writeConfig(t, path, src)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v\n%s", err, src)
	}
	c, err := New(path, cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var buf bytes.Buffer
	runLoop(t, c, slog.New(slog.NewTextHandler(&buf, nil)))
	return &reloadLoop{c: c, path: path, log: &buf}
}

func writeConfig(t *testing.T, path, src string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
}

// reload は設定ファイルを src に書き換え、シェルの reload を送って応答を返す。
func (l *reloadLoop) reload(t *testing.T, src string) []string {
	t.Helper()
	writeConfig(t, l.path, src)
	return request(t, l.c, CmdReload)
}

// finish は quit を送って Run が返るのを待ち、イベントログを行に分けて返す。
// stubborn のプロセスが SIGKILL で終わるのを待つので、3 秒まで待つ。
func (l *reloadLoop) finish(t *testing.T) []string {
	t.Helper()
	quitAndWait(t, l.c, 3*time.Second)
	return logLines(l.log)
}

// send は、イベントを 1 つループに送る。SIGHUP をこのテストのプロセスに送らずに、ループが受けたときの動きを見る。
func send(t *testing.T, c *Controller, ev event) {
	t.Helper()
	select {
	case c.events <- ev:
	case <-time.After(wait):
		t.Fatalf("%T: 送信が %v 以内に終わらない", ev, wait)
	}
}

// pidOf は、status で id の PID の欄を読む。
func pidOf(t *testing.T, c *Controller, id string) int {
	t.Helper()
	got := request(t, c, CmdStatus, id)
	if len(got) != 2 {
		t.Fatalf("status %s = %q, want 見出しと 1 行", id, got)
	}
	var pid int
	if _, err := fmt.Sscan(strings.Fields(got[1])[2], &pid); err != nil {
		t.Fatalf("status %s の PID の欄が数でない: %q", id, got[1])
	}
	return pid
}

// cmdlineOf は、/proc/<pid>/cmdline を空白区切りの 1 行にして返す。新しい spec の cmd で起動したかを見る。
func cmdlineOf(t *testing.T, pid int) string {
	t.Helper()
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		t.Fatalf("pid %d の cmdline を読めない: %v", pid, err)
	}
	return strings.Join(strings.Split(strings.TrimRight(string(b), "\x00"), "\x00"), " ")
}

// mustIndexAfter は indexAfter で、見つからなければテストを止める。
func mustIndexAfter(t *testing.T, lines []string, from int, substr string) int {
	t.Helper()
	i := indexAfter(lines, from, substr)
	if i < 0 {
		t.Fatalf("イベントログの %d 行目より後ろに %q の行が無い\n%s", from+1, substr, strings.Join(lines, "\n"))
	}
	return i
}

func hasAttr(line, attr string) bool {
	return slices.Contains(strings.Fields(line), attr)
}

// TestReload_Lines は、reload の応答の行を見る。
// 変わった program を名前の昇順に 1 行ずつ出し、最後に変わらなかった program の数を出す。
// 1 つも変わらなければ no changes だけを出す。resized の行にだけ前後の numprocs を出す。
// どの program も autostart: false にして、子を起動しない。
func TestReload_Lines(t *testing.T) {
	base := yamlConfig(
		yamlProgram("batch", noop, manual, "numprocs: 2"),
		yamlProgram("keep", noop, manual),
		yamlProgram("oldprog", noop, manual),
		yamlProgram("workers", noop, manual),
	)
	tests := []struct {
		name string
		new  string
		want []string
	}{
		{
			name: "同じ設定なら no changes だけ",
			new:  base,
			want: []string{"no changes"},
		},
		{
			name: "4 種類が混ざると名前の昇順に並び、最後に unchanged の数",
			new: yamlConfig(
				yamlProgram("batch", noop, manual, "numprocs: 4"),
				yamlProgram("keep", noop, manual),
				yamlProgram("newprog", noop, manual),
				yamlProgram("workers", `cmd: ["false"]`, manual),
			),
			want: []string{
				"batch: resized (2 -> 4)",
				"newprog: added",
				"oldprog: removed",
				"workers: changed",
				"1 unchanged",
			},
		},
		{
			name: "numprocs とほかの項目が両方変われば changed で、numprocs は出さない",
			new: yamlConfig(
				yamlProgram("batch", `cmd: ["false"]`, manual, "numprocs: 4"),
				yamlProgram("keep", noop, manual),
				yamlProgram("oldprog", noop, manual),
				yamlProgram("workers", noop, manual),
			),
			want: []string{"batch: changed", "3 unchanged"},
		},
		{
			name: "書き方が違うだけで意味が同じなら no changes",
			new: yamlConfig(
				yamlProgram("batch", noop, manual, "numprocs: 2", "env: {}"),
				yamlProgram("keep", noop, manual, "exitcodes:"),
				yamlProgram("oldprog", noop, manual, "exitcodes: [0]"),
				yamlProgram("workers", `cmd: true`, manual),
			),
			want: []string{"no changes"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := newReloadLoop(t, base)
			if got := l.reload(t, tt.new); !slices.Equal(got, tt.want) {
				t.Errorf("reload\n got = %q\nwant = %q", got, tt.want)
			}
		})
	}
}

// TestReload_LogLine は、msg=reload の行を見る。
// 種類ごとの program 名を昇順のカンマ区切りで出し、無い種類は属性ごと出さない。unchanged は 0 でも出す。
func TestReload_LogLine(t *testing.T) {
	l := newReloadLoop(t, yamlConfig(
		yamlProgram("batch", noop, manual, "numprocs: 2"),
		yamlProgram("keep", noop, manual),
		yamlProgram("oldprog", noop, manual),
		yamlProgram("workers", noop, manual),
	))
	next := yamlConfig(
		yamlProgram("batch", noop, manual, "numprocs: 4"),
		yamlProgram("keep", noop, manual),
		yamlProgram("new2", noop, manual),
		yamlProgram("new1", noop, manual),
		yamlProgram("workers", `cmd: ["false"]`, manual),
	)
	l.reload(t, next)
	l.reload(t, next)
	l.reload(t, yamlConfig(yamlProgram("only", noop, manual)))
	lines := l.finish(t)

	first := mustIndexAfter(t, lines, -1, "msg=reload ")
	want := "msg=reload by=shell added=new1,new2 changed=workers removed=oldprog resized=batch unchanged=1"
	if !strings.Contains(lines[first], want) {
		t.Errorf("1 回目の reload の行\n got = %s\nwant に %q を含む", lines[first], want)
	}
	if !strings.Contains(lines[first], "level=INFO") {
		t.Errorf("1 回目の reload の行が INFO でない: %s", lines[first])
	}

	second := mustIndexAfter(t, lines, first, "msg=reload ")
	want = "msg=reload by=shell unchanged=5"
	if !strings.HasSuffix(lines[second], want) {
		t.Errorf("変わらなかった reload の行\n got = %s\nwant が %q で終わる", lines[second], want)
	}

	third := mustIndexAfter(t, lines, second, "msg=reload ")
	want = "msg=reload by=shell added=only removed=batch,keep,new1,new2,workers unchanged=0"
	if !strings.HasSuffix(lines[third], want) {
		t.Errorf("全部入れ替えた reload の行\n got = %s\nwant が %q で終わる", lines[third], want)
	}
}

// TestReload_Names は、reload のあと Names が新しい設定の program 名を昇順で返すことを見る。補完の候補になる。
func TestReload_Names(t *testing.T) {
	l := newReloadLoop(t, yamlConfig(
		yamlProgram("b", noop, manual),
		yamlProgram("gone", noop, manual),
	))
	l.reload(t, yamlConfig(
		yamlProgram("b", noop, manual),
		yamlProgram("c", noop, manual),
		yamlProgram("a", noop, manual),
	))
	if got, want := l.c.Names(), []string{"a", "b", "c"}; !slices.Equal(got, want) {
		t.Errorf("Names() = %q, want %q", got, want)
	}
}

// TestReload_SIGHUP は、SIGHUP で reload が走り、ログに action=reload と by=signal が出ることを見る。
// SIGHUP には応答が無いので、続けて送った status の要求で、差分が適用されたことを確かめる。
// イベントは 1 本の channel を順に処理するので、status は reload のあとに処理される。
func TestReload_SIGHUP(t *testing.T) {
	l := newReloadLoop(t, yamlConfig(yamlProgram("a", noop, manual)))
	writeConfig(t, l.path, yamlConfig(
		yamlProgram("a", noop, manual),
		yamlProgram("b", noop, manual),
	))
	send(t, l.c, evSignal{sig: syscall.SIGHUP})
	if got := stateOf(t, l.c, "b:0"); got != process.Stopped.String() {
		t.Errorf("SIGHUP のあとの b:0 = %s, want STOPPED", got)
	}
	lines := l.finish(t)

	sig := mustIndexAfter(t, lines, -1, "msg=signal signal=1")
	if !hasAttr(lines[sig], "action=reload") {
		t.Errorf("SIGHUP の行に action=reload が無い: %s", lines[sig])
	}
	re := mustIndexAfter(t, lines, sig, "msg=reload ")
	if !strings.Contains(lines[re], "msg=reload by=signal added=b unchanged=1") {
		t.Errorf("SIGHUP による reload の行 = %s, want msg=reload by=signal added=b unchanged=1", lines[re])
	}
}

// TestReload_WhileShuttingDown は、shutdown 中の reload が何もしないことを見る。
// シェルの reload は reload: ERROR (shutting down) を返し、SIGHUP は action=ignore reason=already_shutting_down を書く。
// stubborn が STOPPING に留まるあいだ、ループは shutdown 中のまま要求を受ける。
func TestReload_WhileShuttingDown(t *testing.T) {
	l := newReloadLoop(t, yamlConfig(yamlProgram("s", stubborn30, quick, stopFast)))
	awaitState(t, l.c, "s:0", process.Running)
	added := yamlConfig(
		yamlProgram("s", stubborn30, quick, stopFast),
		yamlProgram("late", noop, manual),
	)
	writeConfig(t, l.path, added)

	request(t, l.c, CmdQuit)
	if got, want := request(t, l.c, CmdReload), []string{"reload: ERROR (shutting down)"}; !slices.Equal(got, want) {
		t.Errorf("shutdown 中の reload = %q, want %q", got, want)
	}
	send(t, l.c, evSignal{sig: syscall.SIGHUP})
	select {
	case <-l.c.done:
	case <-time.After(3 * time.Second):
		t.Fatal("quit から 3 秒待っても Run が返らない")
	}
	lines := logLines(l.log)

	sig := mustIndexAfter(t, lines, -1, "msg=signal signal=1")
	for _, attr := range []string{"action=ignore", "reason=already_shutting_down"} {
		if !hasAttr(lines[sig], attr) {
			t.Errorf("shutdown 中の SIGHUP の行に %s が無い: %s", attr, lines[sig])
		}
	}
	if i := indexAfter(lines, -1, "msg=reload "); i >= 0 {
		t.Errorf("shutdown 中に reload の行が出た: %s", lines[i])
	}
}

// TestReload_ChangedRestartsRunning は、changed の program の動いていたプロセスが、
// 古い spec で止まってから、新しい spec で起動し直すことを見る。
// autostart: false でも、シェルの start で動いていたなら起動し直す。
// 停止と起動の遷移の行には by=reload が付き、Process を使い回すので gen は 2 に進む。
// unchanged の program のプロセスには触らない。pid は変わらず、reload のあとに遷移の行が出ない。
func TestReload_ChangedRestartsRunning(t *testing.T) {
	l := newReloadLoop(t, yamlConfig(
		yamlProgram("keep", sleep30, quick, stopFast),
		yamlProgram("manual", sleep30, quick, stopFast, manual),
		yamlProgram("workers", sleep30, quick, stopFast),
	))
	request(t, l.c, CmdStart, "manual")
	before := map[string]int{}
	for _, id := range []string{"keep:0", "manual:0", "workers:0"} {
		awaitState(t, l.c, id, process.Running)
		before[id] = pidOf(t, l.c, id)
	}

	got := l.reload(t, yamlConfig(
		yamlProgram("keep", sleep30, quick, stopFast),
		yamlProgram("manual", sleep31, quick, stopFast, manual),
		yamlProgram("workers", sleep31, quick, stopFast),
	))
	if want := []string{"manual: changed", "workers: changed", "1 unchanged"}; !slices.Equal(got, want) {
		t.Errorf("reload = %q, want %q", got, want)
	}
	for _, id := range []string{"manual:0", "workers:0"} {
		awaitState(t, l.c, id, process.Running)
		pid := pidOf(t, l.c, id)
		if pid == before[id] {
			t.Errorf("%s の pid が変わっていない（%d）。起動し直していない", id, pid)
		}
		if cmd := cmdlineOf(t, pid); cmd != "sleep 31" {
			t.Errorf("%s の cmdline = %q, want %q（新しい spec で起動していない）", id, cmd, "sleep 31")
		}
	}
	if pid := pidOf(t, l.c, "keep:0"); pid != before["keep:0"] {
		t.Errorf("unchanged の keep:0 の pid が %d から %d に変わった", before["keep:0"], pid)
	}
	lines := l.finish(t)

	re := mustIndexAfter(t, lines, -1, "msg=reload ")
	for _, id := range []string{"manual:0", "workers:0"} {
		stopping := mustIndexAfter(t, lines, re, "msg=transition id="+id+" from=RUNNING to=STOPPING")
		if !hasAttr(lines[stopping], "by=reload") {
			t.Errorf("%s の停止の行に by=reload が無い: %s", id, lines[stopping])
		}
		if !hasAttr(lines[stopping], "signal=15") && !hasAttr(lines[stopping], "stopsignal=15") {
			t.Errorf("%s の停止の行が古い spec の stopsignal で送っていない: %s", id, lines[stopping])
		}
		stopped := mustIndexAfter(t, lines, stopping, "msg=transition id="+id+" from=STOPPING to=STOPPED")
		started := mustIndexAfter(t, lines, stopped, "msg=transition id="+id+" from=STOPPED to=STARTING")
		if !hasAttr(lines[started], "by=reload") {
			t.Errorf("%s の起動し直しの行に by=reload が無い: %s", id, lines[started])
		}
		if !hasAttr(lines[started], "gen=2") {
			t.Errorf("%s の起動し直しが gen=2 でない（Process を作り直した？）: %s", id, lines[started])
		}
	}
	if i := indexAfter(lines, re, "msg=transition id=keep:0 "); i >= 0 && !hasAttr(lines[i], "by=shutdown") {
		t.Errorf("unchanged の keep:0 に reload が触った: %s", lines[i])
	}
}

// TestReload_ChangedStoppedFollowsAutostart は、changed の program の止まっていたプロセスが、
// 新しい autostart に従うことを見る。true なら by=reload で起動し、false なら STOPPED のまま。
func TestReload_ChangedStoppedFollowsAutostart(t *testing.T) {
	l := newReloadLoop(t, yamlConfig(
		yamlProgram("on", sleep30, quick, stopFast, manual),
		yamlProgram("off", sleep30, quick, stopFast, manual),
	))
	l.reload(t, yamlConfig(
		yamlProgram("on", sleep31, quick, stopFast),
		yamlProgram("off", sleep31, quick, stopFast, manual),
	))
	awaitState(t, l.c, "on:0", process.Running)
	if got := stateOf(t, l.c, "off:0"); got != process.Stopped.String() {
		t.Errorf("autostart: false の off:0 = %s, want STOPPED のまま", got)
	}
	lines := l.finish(t)

	re := mustIndexAfter(t, lines, -1, "msg=reload ")
	started := mustIndexAfter(t, lines, re, "msg=transition id=on:0 from=STOPPED to=STARTING")
	if !hasAttr(lines[started], "by=reload") {
		t.Errorf("on:0 の起動の行に by=reload が無い: %s", lines[started])
	}
	if i := indexAfter(lines, re, "msg=transition id=off:0 "); i >= 0 {
		t.Errorf("autostart: false の off:0 が遷移した: %s", lines[i])
	}
}

// TestReload_ChangedBackoff は、BACKOFF の changed のプロセスが、STOPPED を通って新しい spec で起動することを見る。
// 古い世代の再試行のタイマーが届いても、gen が違うので無視される。
func TestReload_ChangedBackoff(t *testing.T) {
	l := newReloadLoop(t, yamlConfig(
		yamlProgram("x", `cmd: ["/nonexistent"]`, quick, stopFast, "startretries: 100"),
	))
	awaitState(t, l.c, "x:0", process.Backoff)

	l.reload(t, yamlConfig(yamlProgram("x", sleep30, quick, stopFast, "startretries: 100")))
	awaitState(t, l.c, "x:0", process.Running)
	if cmd := cmdlineOf(t, pidOf(t, l.c, "x:0")); cmd != "sleep 30" {
		t.Errorf("x:0 の cmdline = %q, want %q", cmd, "sleep 30")
	}
	lines := l.finish(t)

	re := mustIndexAfter(t, lines, -1, "msg=reload ")
	stopped := mustIndexAfter(t, lines, re, "msg=transition id=x:0 from=BACKOFF to=STOPPED")
	if !hasAttr(lines[stopped], "by=reload") {
		t.Errorf("BACKOFF → STOPPED の行に by=reload が無い: %s", lines[stopped])
	}
	started := mustIndexAfter(t, lines, stopped, "msg=transition id=x:0 from=STOPPED to=STARTING")
	if !hasAttr(lines[started], "by=reload") {
		t.Errorf("STOPPED → STARTING の行に by=reload が無い: %s", lines[started])
	}
}

// TestReload_ChangedKeepsShellReservation は、restart で予約が立った STOPPING のプロセスを reload が changed にしたとき、
// 予約を上書きせず、起動の行が by=shell のまま、新しい spec で起動することを見る。
func TestReload_ChangedKeepsShellReservation(t *testing.T) {
	l := newReloadLoop(t, yamlConfig(yamlProgram("s", stubborn30, quick, stopFast)))
	awaitState(t, l.c, "s:0", process.Running)
	request(t, l.c, CmdRestart, "s")

	l.reload(t, yamlConfig(yamlProgram("s", stubborn31, quick, stopFast)))
	awaitState(t, l.c, "s:0", process.Running)
	if cmd := cmdlineOf(t, pidOf(t, l.c, "s:0")); !strings.Contains(cmd, "sleep 31") {
		t.Errorf("s:0 の cmdline = %q, want sleep 31 を含む（新しい spec で起動していない）", cmd)
	}
	lines := l.finish(t)

	re := mustIndexAfter(t, lines, -1, "msg=reload ")
	stopped := mustIndexAfter(t, lines, re, "msg=transition id=s:0 from=STOPPING to=STOPPED")
	started := mustIndexAfter(t, lines, stopped, "msg=transition id=s:0 from=STOPPED to=STARTING")
	if !hasAttr(lines[started], "by=shell") {
		t.Errorf("restart の予約による起動の行が by=shell でない: %s", lines[started])
	}
	if i := indexAfter(lines, started, "msg=transition id=s:0 from=STOPPED to=STARTING"); i >= 0 {
		t.Errorf("予約による起動が 2 回あった: %s", lines[i])
	}
}

// TestReload_ChangedShrinks は、changed で numprocs も減ったとき、新しい numprocs 以上の index を止めて外すことを見る。
func TestReload_ChangedShrinks(t *testing.T) {
	l := newReloadLoop(t, yamlConfig(yamlProgram("w", sleep30, quick, stopFast, "numprocs: 2")))
	awaitState(t, l.c, "w:1", process.Running)

	l.reload(t, yamlConfig(yamlProgram("w", sleep31, quick, stopFast)))
	if got, want := request(t, l.c, CmdStatus, "w:1"), []string{"w:1: ERROR (no such process, numprocs is 1)"}; !slices.Equal(got, want) {
		t.Errorf("status w:1 = %q, want %q", got, want)
	}
	awaitState(t, l.c, "w:0", process.Running)
	lines := l.finish(t)

	re := mustIndexAfter(t, lines, -1, "msg=reload ")
	stopping := mustIndexAfter(t, lines, re, "msg=transition id=w:1 from=RUNNING to=STOPPING")
	if !hasAttr(lines[stopping], "by=reload") {
		t.Errorf("w:1 の停止の行に by=reload が無い: %s", lines[stopping])
	}
	if i := indexAfter(lines, re, "msg=transition id=w:1 from=STOPPED to=STARTING"); i >= 0 {
		t.Errorf("外した w:1 を起動し直した: %s", lines[i])
	}
}

// TestReload_Resized は、numprocs だけを変えたとき、増えた index だけを起動し、減った index だけを止めることを見る。
// 残る index のプロセスは pid が変わらず、reload のあとに遷移の行が出ない。
func TestReload_Resized(t *testing.T) {
	l := newReloadLoop(t, yamlConfig(yamlProgram("r", sleep30, quick, stopFast, "numprocs: 2")))
	before := map[string]int{}
	for _, id := range []string{"r:0", "r:1"} {
		awaitState(t, l.c, id, process.Running)
		before[id] = pidOf(t, l.c, id)
	}

	if got, want := l.reload(t, yamlConfig(yamlProgram("r", sleep30, quick, stopFast, "numprocs: 4"))),
		[]string{"r: resized (2 -> 4)", "0 unchanged"}; !slices.Equal(got, want) {
		t.Errorf("reload = %q, want %q", got, want)
	}
	for _, id := range []string{"r:2", "r:3"} {
		awaitState(t, l.c, id, process.Running)
	}
	if got, want := l.reload(t, yamlConfig(yamlProgram("r", sleep30, quick, stopFast, "numprocs: 2"))),
		[]string{"r: resized (4 -> 2)", "0 unchanged"}; !slices.Equal(got, want) {
		t.Errorf("reload = %q, want %q", got, want)
	}
	if got, want := request(t, l.c, CmdStatus, "r:2"), []string{"r:2: ERROR (no such process, numprocs is 2)"}; !slices.Equal(got, want) {
		t.Errorf("status r:2 = %q, want %q", got, want)
	}
	for _, id := range []string{"r:0", "r:1"} {
		if pid := pidOf(t, l.c, id); pid != before[id] {
			t.Errorf("残る %s の pid が %d から %d に変わった", id, before[id], pid)
		}
	}
	lines := l.finish(t)

	grow := mustIndexAfter(t, lines, -1, "msg=reload ")
	shrink := mustIndexAfter(t, lines, grow, "msg=reload ")
	for _, id := range []string{"r:2", "r:3"} {
		started := mustIndexAfter(t, lines, grow, "msg=transition id="+id+" from=STOPPED to=STARTING")
		if started > shrink {
			t.Errorf("%s が増やした reload のあとに起動していない", id)
		}
		if !hasAttr(lines[started], "by=reload") || !hasAttr(lines[started], "gen=1") {
			t.Errorf("増えた %s の起動の行が by=reload gen=1 でない: %s", id, lines[started])
		}
		stopping := mustIndexAfter(t, lines, shrink, "msg=transition id="+id+" from=RUNNING to=STOPPING")
		if !hasAttr(lines[stopping], "by=reload") {
			t.Errorf("減った %s の停止の行に by=reload が無い: %s", id, lines[stopping])
		}
	}
	for _, id := range []string{"r:0", "r:1"} {
		if i := indexAfter(lines, grow, "msg=transition id="+id+" "); i >= 0 && !hasAttr(lines[i], "by=shutdown") {
			t.Errorf("残る %s に reload が触った: %s", id, lines[i])
		}
	}
}

// TestReload_AddedFollowsAutostart は、added の program のプロセスが STOPPED から始まり、autostart に従うことを見る。
// autostart: true なら by=reload で起動する。
func TestReload_AddedFollowsAutostart(t *testing.T) {
	l := newReloadLoop(t, yamlConfig(yamlProgram("keep", noop, manual)))
	l.reload(t, yamlConfig(
		yamlProgram("keep", noop, manual),
		yamlProgram("auto", sleep30, quick, stopFast, "numprocs: 2"),
		yamlProgram("idle", sleep30, quick, stopFast, manual),
	))
	awaitState(t, l.c, "auto:0", process.Running)
	awaitState(t, l.c, "auto:1", process.Running)
	if got := stateOf(t, l.c, "idle:0"); got != process.Stopped.String() {
		t.Errorf("autostart: false の idle:0 = %s, want STOPPED", got)
	}
	lines := l.finish(t)

	re := mustIndexAfter(t, lines, -1, "msg=reload ")
	for _, id := range []string{"auto:0", "auto:1"} {
		started := mustIndexAfter(t, lines, re, "msg=transition id="+id+" from=STOPPED to=STARTING")
		if !hasAttr(lines[started], "by=reload") || !hasAttr(lines[started], "gen=1") {
			t.Errorf("added の %s の起動の行が by=reload gen=1 でない: %s", id, lines[started])
		}
	}
}

// TestReload_RemovedStopsBeforeForgetting は、removed の program が status からすぐ消え、
// プロセスは STOPPED に入るまで追われ続けることを見る。
// s は TERM を無視するので、stoptime の 1 秒後の SIGKILL で終わる。
// プロセスを先に忘れると、SIGKILL を送らず、quit で Run が先に返り、子が残る。
func TestReload_RemovedStopsBeforeForgetting(t *testing.T) {
	l := newReloadLoop(t, yamlConfig(
		yamlProgram("keep", noop, manual),
		yamlProgram("s", stubborn30, quick, stopFast),
	))
	awaitState(t, l.c, "s:0", process.Running)
	pid := pidOf(t, l.c, "s:0")
	t.Cleanup(func() { syscall.Kill(-pid, syscall.SIGKILL) }) // 失敗したときに子を残さない

	if got, want := l.reload(t, yamlConfig(yamlProgram("keep", noop, manual))),
		[]string{"s: removed", "1 unchanged"}; !slices.Equal(got, want) {
		t.Errorf("reload = %q, want %q", got, want)
	}
	if got, want := request(t, l.c, CmdStatus, "s"), []string{"s: ERROR (no such program)"}; !slices.Equal(got, want) {
		t.Errorf("status s = %q, want %q", got, want)
	}
	if got := request(t, l.c, CmdStatus); slices.ContainsFunc(got, func(line string) bool { return strings.HasPrefix(line, "s:0 ") }) {
		t.Errorf("removed の s:0 が status に出た: %q", got)
	}
	// STOPPING のまま quit する。Run は s:0 が STOPPED に入るまで返らない。
	lines := l.finish(t)

	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Errorf("Run が返ったあとも removed の子（pid %d）が残っている: kill(pid, 0) = %v", pid, err)
	}
	re := mustIndexAfter(t, lines, -1, "msg=reload ")
	stopping := mustIndexAfter(t, lines, re, "msg=transition id=s:0 from=RUNNING to=STOPPING")
	if !hasAttr(lines[stopping], "by=reload") {
		t.Errorf("removed の停止の行に by=reload が無い: %s", lines[stopping])
	}
	kill := mustIndexAfter(t, lines, stopping, "msg=kill id=s:0")
	mustIndexAfter(t, lines, kill, "msg=transition id=s:0 from=STOPPING to=STOPPED")
}

// TestReload_ReaddWhileStopping は、removed で止めている途中の program を次の reload で足し直すと、
// 同じ Process を使い回すことを見る。STOPPED に入ったあと、autostart: true なので by=reload で起動し、gen は 2 に進む。
// Process を作り直すと、新しいほうも gen=1 から始まり、古い子の終了を新しい子の終了と取り違える。
func TestReload_ReaddWhileStopping(t *testing.T) {
	src := yamlConfig(yamlProgram("s", stubborn30, quick, stopFast))
	l := newReloadLoop(t, src)
	awaitState(t, l.c, "s:0", process.Running)
	old := pidOf(t, l.c, "s:0")

	l.reload(t, yamlConfig())
	if got, want := l.reload(t, src), []string{"s: added", "0 unchanged"}; !slices.Equal(got, want) {
		t.Errorf("足し直した reload = %q, want %q", got, want)
	}
	if got := stateOf(t, l.c, "s:0"); got != process.Stopping.String() {
		t.Errorf("足し直した直後の s:0 = %s, want STOPPING（止めている途中のプロセスを使い回す）", got)
	}
	awaitState(t, l.c, "s:0", process.Running)
	if pid := pidOf(t, l.c, "s:0"); pid == old {
		t.Errorf("s:0 の pid が %d のまま。起動し直していない", pid)
	}
	lines := l.finish(t)

	stopped := mustIndexAfter(t, lines, -1, "msg=transition id=s:0 from=STOPPING to=STOPPED")
	started := mustIndexAfter(t, lines, stopped, "msg=transition id=s:0 from=STOPPED to=STARTING")
	if !hasAttr(lines[started], "by=reload") || !hasAttr(lines[started], "gen=2") {
		t.Errorf("足し直した s:0 の起動の行が by=reload gen=2 でない: %s", lines[started])
	}
}

// TestReload_BadConfigAppliesNothing は、新しい設定を使えないとき、何も適用せずに今の設定のまま続けることを見る。
// 応答は reload: ERROR (<エラー文>) の 1 行で、ログは ERROR の msg=reload に by と err を書く。
// 新しい設定には changed と added も入れておき、どちらも適用されないことを確かめる。
// そのあと正しい設定で reload すると、今の設定との差分が適用される。
func TestReload_BadConfigAppliesNothing(t *testing.T) {
	base := yamlConfig(yamlProgram("w", sleep30, quick, stopFast))
	tests := []struct {
		name  string
		bad   func(dir string) string
		check func(t *testing.T, path, line string)
	}{
		{
			name: "設定ファイルの検証に通らない",
			bad: func(string) string {
				return yamlConfig(
					yamlProgram("w", sleep31, quick, stopFast),
					yamlProgram("newprog", noop, "cmdd: x"),
				)
			},
			check: func(t *testing.T, path, line string) {
				// エラー文は起動時の設定エラーと同じ文面で、ファイル名と行番号を含む
				if !strings.HasPrefix(line, "reload: ERROR ("+path+": line ") || !strings.Contains(line, `unknown field "cmdd"`) {
					t.Errorf("reload = %q, want reload: ERROR (%s: line N: unknown field \"cmdd\")", line, path)
				}
			},
		},
		{
			name: "YAML として読めない",
			bad: func(string) string {
				return "programs:\n  w:\n    cmd: [\n"
			},
			check: func(t *testing.T, path, line string) {
				if !strings.HasPrefix(line, "reload: ERROR ("+path+": ") {
					t.Errorf("reload = %q, want reload: ERROR (%s: ...)", line, path)
				}
			},
		},
		{
			name: "出力先を開けない",
			bad: func(dir string) string {
				return yamlConfig(
					yamlProgram("w", sleep31, quick, stopFast),
					yamlProgram("newprog", noop, manual, "stdout: "+filepath.Join(dir, "missing", "out.log")),
				)
			},
			check: func(t *testing.T, path, line string) {
				if !strings.HasPrefix(line, "reload: ERROR (") || !strings.Contains(line, filepath.Join("missing", "out.log")) {
					t.Errorf("reload = %q, want reload: ERROR (...missing/out.log...)", line)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := newReloadLoop(t, base)
			awaitState(t, l.c, "w:0", process.Running)
			before := pidOf(t, l.c, "w:0")

			got := l.reload(t, tt.bad(filepath.Dir(l.path)))
			if len(got) != 1 || !strings.HasSuffix(got[0], ")") {
				t.Fatalf("reload = %q, want reload: ERROR (...) の 1 行", got)
			}
			t.Logf("実際の応答: %s", got[0])
			tt.check(t, l.path, got[0])

			if pid := pidOf(t, l.c, "w:0"); pid != before {
				t.Errorf("失敗した reload のあと w:0 の pid が %d から %d に変わった", before, pid)
			}
			if got, want := request(t, l.c, CmdStatus, "newprog"), []string{"newprog: ERROR (no such program)"}; !slices.Equal(got, want) {
				t.Errorf("失敗した reload のあとの status newprog = %q, want %q", got, want)
			}
			if got, want := l.c.Names(), []string{"w"}; !slices.Equal(got, want) {
				t.Errorf("失敗した reload のあとの Names() = %q, want %q", got, want)
			}

			// 今の設定は base のままなので、差分は base との比較になる
			if got, want := l.reload(t, yamlConfig(yamlProgram("w", sleep31, quick, stopFast))),
				[]string{"w: changed", "0 unchanged"}; !slices.Equal(got, want) {
				t.Errorf("直した設定の reload = %q, want %q", got, want)
			}
			lines := l.finish(t)

			failed := mustIndexAfter(t, lines, -1, "msg=reload ")
			for _, attr := range []string{"level=ERROR", "by=shell"} {
				if !hasAttr(lines[failed], attr) {
					t.Errorf("失敗した reload の行に %s が無い: %s", attr, lines[failed])
				}
			}
			if !strings.Contains(lines[failed], " err=") {
				t.Errorf("失敗した reload の行に err が無い: %s", lines[failed])
			}
			if i := indexAfter(lines, -1, "msg=transition id=w:0 from=RUNNING to=STOPPING"); i >= 0 && i < failed {
				t.Errorf("失敗した reload の前に w:0 を止めた: %s", lines[i])
			}
			ok := mustIndexAfter(t, lines, failed, "msg=reload ")
			if !strings.Contains(lines[ok], "msg=reload by=shell changed=w unchanged=0") {
				t.Errorf("直した設定の reload の行 = %s", lines[ok])
			}
		})
	}
}
