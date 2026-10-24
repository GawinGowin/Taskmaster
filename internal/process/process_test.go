package process

import (
	"os"
	"strings"
	"taskmaster/internal/config"
	"testing"
	"time"
)

// このファイルは内部テスト（package process）。exec.Cmd の組み立てが設計どおりか、
// 外から観測できない場所を直接見るため。API の使い勝手を見るテストは外部パッケージで書く。

// newProcess は New を panic を握って呼ぶ。設定由来の値を添字で触る箇所があるので、
// エラーを返すべき場面で落ちていないことを見分けられるようにする。出力は破棄（nil）にする。
func newProcess(t *testing.T, name string, index int, p *config.Program) (got *Process, err error, panicked any) {
	t.Helper()
	defer func() { panicked = recover() }()
	got, err = New(p, name, index, nil, nil)
	return got, err, nil
}

// minimal は起動できる最小の Program を返す。
func minimal() *config.Program {
	return &config.Program{Cmd: config.Command{"/bin/true"}}
}

// TestNew_EmptyCmd は空の argv で panic せずエラーを返すことを見る。
//
// config パッケージは cmd が空でないことを保証しているが、New は config.Program を
// 直接受け取るので、その保証が効かない経路（テストや将来の呼び出し元）が存在する。
// 保証を前提に p.Cmd[0] を触ると、そこで index out of range になる。
func TestNew_EmptyCmd(t *testing.T) {
	for _, tt := range []struct {
		name string
		prog *config.Program
	}{
		{name: "Cmd が nil", prog: &config.Program{}},
		{name: "Cmd が空スライス", prog: &config.Program{Cmd: config.Command{}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err, panicked := newProcess(t, "p", 0, tt.prog)
			if panicked != nil {
				t.Fatalf("panic: %v （添字の前に長さを見ていない）", panicked)
			}
			if err == nil {
				t.Fatal("err = nil, want エラー")
			}
			t.Logf("実際のエラー出力:\n%v", err)
			if !strings.Contains(err.Error(), "p:0") {
				t.Errorf("どの program のどのプロセスか分からない: %q", err)
			}
		})
	}
}

// TestBuildCmd_Wires は exec.Cmd の組み立てを見る。
// Setpgid は停止時の Kill(-pgid) の前提で、後から足せない（起動時にしか効かない）。
func TestBuildCmd_Wires(t *testing.T) {
	prog := minimal()
	prog.Cmd = config.Command{"/bin/echo", "hello world"}
	prog.Workingdir = "/tmp"

	p, err, panicked := newProcess(t, "healthy", 1, prog)
	if panicked != nil {
		t.Fatalf("panic: %v", panicked)
	}
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	cmd := p.buildCmd()

	if cmd.Path != "/bin/echo" {
		t.Errorf("Path = %q, want /bin/echo", cmd.Path)
	}
	// 空白を含む引数が 1 つのまま渡ること（config のリスト形式が活きる経路）。
	if len(cmd.Args) != 2 || cmd.Args[1] != "hello world" {
		t.Errorf("Args = %#v, want [/bin/echo, \"hello world\"]", cmd.Args)
	}
	if cmd.Dir != "/tmp" {
		t.Errorf("Dir = %q, want /tmp", cmd.Dir)
	}
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid {
		t.Errorf("Setpgid が立っていない: %#v （停止時の Kill(-pgid) が届かなくなる）", cmd.SysProcAttr)
	}
	if p.name != "healthy" || p.index != 1 {
		t.Errorf("name/index = %q/%d, want healthy/1", p.name, p.index)
	}
}

// TestNew_EnvInheritsAndOverrides は env の合成規則を固定する。
//
// 設定の env は**親の環境に足す**。置き換えにすると、env: を 1 行書いた
// 瞬間に子が PATH も HOME も失う。同名キーは設定が勝つ ——
// os/exec が「重複キーは最後の値を使う」と決めている（Cmd.Env のドキュメント）ので、
// 親の環境の後ろに足せばそうなる。
func TestBuildCmd_EnvInheritsAndOverrides(t *testing.T) {
	t.Setenv("TM_TEST_INHERITED", "from-parent")
	t.Setenv("TM_TEST_OVERRIDE", "from-parent")

	prog := minimal()
	prog.Env = map[string]string{"TM_TEST_OVERRIDE": "from-config", "TM_TEST_NEW": "added"}

	p, err, panicked := newProcess(t, "p", 0, prog)
	if panicked != nil {
		t.Fatalf("panic: %v", panicked)
	}
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	cmd := p.buildCmd()

	if !contains(cmd.Env, "TM_TEST_INHERITED=from-parent") {
		t.Error("親の環境が引き継がれていない（置き換えになっている）")
	}
	if !contains(cmd.Env, "TM_TEST_NEW=added") {
		t.Error("設定の env が足されていない")
	}
	// 重複は「最後が勝つ」ので、設定の値が親の値より後ろに無ければならない。
	if last(cmd.Env, "TM_TEST_OVERRIDE=") != "TM_TEST_OVERRIDE=from-config" {
		t.Errorf("同名キーで設定が勝っていない: %q", last(cmd.Env, "TM_TEST_OVERRIDE="))
	}
}

// TestBuildCmd_EnvNilInherits は env を書かなかったときに nil のままであることを見る。
// nil は os/exec にとって「親の環境をそのまま使う」の意味。
func TestBuildCmd_EnvNilInherits(t *testing.T) {
	p, err, panicked := newProcess(t, "p", 0, minimal())
	if panicked != nil {
		t.Fatalf("panic: %v", panicked)
	}
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if cmd := p.buildCmd(); cmd.Env != nil {
		t.Errorf("Env = %#v, want nil（未指定は継承）", cmd.Env)
	}
}

// TestNew_PinsSpec は起動時の spec を握ることを見る。
// reload で設定が差し替わっても、走行中のプロセスは起動時の値を見続ける。
func TestNew_PinsSpec(t *testing.T) {
	prog := minimal()
	p, err, panicked := newProcess(t, "p", 0, prog)
	if panicked != nil {
		t.Fatalf("panic: %v", panicked)
	}
	if err != nil {
		t.Fatalf("err = %v", err)
	}

	prog.Stoptime = 99 // 呼び出し元が持っている Program を後から書き換える
	if p.prog.Stoptime == 99 {
		t.Error("spec がピン留めされていない（参照を持っている）")
	}
}

// TestNew_DoesNotBuildCmd は New が exec.Cmd を作らないことを見る。
// Process は「hello:0 という枠」で、起動のたびに Start() が exec.Cmd を作り直す。
// New で作ると、exec.Cmd は 1 回しか Start() できないので再起動で詰まる。
func TestNew_DoesNotBuildCmd(t *testing.T) {
	p, err, panicked := newProcess(t, "p", 0, minimal())
	if panicked != nil {
		t.Fatalf("panic: %v", panicked)
	}
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if p.proc != nil {
		t.Errorf("proc = %#v, want nil（Start() 前に起動している）", p.proc)
	}
	if p.Pid() != 0 {
		t.Errorf("Pid() = %d, want 0（起動前）", p.Pid())
	}
}

// TestBuildCmd_Fresh は buildCmd が呼ぶたびに別の exec.Cmd を返すことを見る。
// 使い回すと 2 回目の Start() が "exec: already started" で失敗する。
func TestBuildCmd_Fresh(t *testing.T) {
	p, err, panicked := newProcess(t, "p", 0, minimal())
	if panicked != nil {
		t.Fatalf("panic: %v", panicked)
	}
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	first, second := p.buildCmd(), p.buildCmd()
	if first == second {
		t.Error("同じ *exec.Cmd を返している（再起動で Start() できない）")
	}
}

func contains(env []string, kv string) bool {
	for _, e := range env {
		if e == kv {
			return true
		}
	}
	return false
}

// last は prefix で始まる最後の要素を返す。os/exec の重複解決（後勝ち）を検査するため。
func last(env []string, prefix string) string {
	out := ""
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			out = e
		}
	}
	return out
}

// TestBuildCmd_LeavesOutputNil は、宛先の Sink があっても buildCmd が Stdout / Stderr に触らないことを見る。
// パイプは Start() が起動のたびに作る。ここで nil の *os.File を入れると「nil ではないインターフェース値」になり、
// os/exec は閉じた fd を子に渡す（子が書き込みに失敗して終了コード 1。autorestart: unexpected が誤って発動する）。
func TestBuildCmd_LeavesOutputNil(t *testing.T) {
	out, _ := newTestSink(t)
	errS, _ := newTestSink(t)
	p, err := New(minimal(), "p", 0, out, errS)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	cmd := p.buildCmd()
	if cmd.Stdout != nil {
		t.Errorf("cmd.Stdout = %#v, want nil", cmd.Stdout)
	}
	if cmd.Stderr != nil {
		t.Errorf("cmd.Stderr = %#v, want nil", cmd.Stderr)
	}
}

// startAndWait は p を起動して終わるまで待ち、子が成功したかを返す。
func startAndWait(t *testing.T, p *Process) bool {
	t.Helper()
	wait, err := p.Start()
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	ps, err := wait()
	if err != nil && ps == nil {
		t.Fatalf("wait: %v", err)
	}
	return ps.Success()
}

// shell は sh -c で script を走らせる Program を返す。
func shell(script string) *config.Program {
	return &config.Program{Cmd: config.Command{"/bin/sh", "-c", script}}
}

// TestStart_Discard は、宛先が nil（破棄）でも子が stdout / stderr に書けることを見る。
// 破棄のときは cmd.Stdout を nil のまま残し、os/exec に /dev/null を開かせる。
// nil の *os.File を入れてしまうと、子は閉じた fd に書いて失敗する。
func TestStart_Discard(t *testing.T) {
	p, err := New(shell("echo out && echo err >&2"), "p", 0, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !startAndWait(t, p) {
		t.Error("子が失敗した（破棄のはずの fd に書けていない）")
	}
}

// TestStart_ToSink は、子の stdout と stderr が別々のパイプを通ってそれぞれの Sink に届くことを見る。
func TestStart_ToSink(t *testing.T) {
	out, outPath := newTestSink(t)
	errS, errPath := newTestSink(t)
	p, err := New(shell("echo out && echo err >&2"), "p", 0, out, errS)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !startAndWait(t, p) {
		t.Fatal("子が失敗した")
	}
	if got := string(waitStable(t, outPath, 2*time.Second)); got != "out\n" {
		t.Errorf("stdout = %q, want %q", got, "out\n")
	}
	if got := string(waitStable(t, errPath, 2*time.Second)); got != "err\n" {
		t.Errorf("stderr = %q, want %q", got, "err\n")
	}
}

// openFds は今このプロセスが開いている fd の数を返す。
func openFds(t *testing.T) int {
	t.Helper()
	ents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatalf("/proc/self/fd を読めない: %v", err)
	}
	return len(ents)
}

// TestStart_ClosesPipes は、起動に成功しても失敗しても、パイプの fd が親に残らないことを見る。
//
// 成功: 親が書き端を閉じていれば、子が死ぬと pump に EOF が来て読み端も閉じる。
// 閉じ忘れると親も書き手として残り、EOF が来ないので再起動のたびに 2 本ずつ積もる。
// 失敗: pump を起こさないので、読み端と書き端の両方を Start() が閉じる。
// 存在しないコマンドは LookPath の失敗（cmd.Err）で fork の前に返るので、ここが抜けやすい。
func TestStart_ClosesPipes(t *testing.T) {
	out, _ := newTestSink(t)
	errS, _ := newTestSink(t)

	// 最初の os.Pipe() でランタイムが poller の fd を開くので、数える前に 1 回起動しておく。
	ok, err := New(shell("echo out && echo err >&2"), "p", 0, out, errS)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	startAndWait(t, ok)
	ng, err := New(&config.Program{Cmd: config.Command{"/nonexistent/taskmaster-test"}}, "p", 0, out, errS)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	t.Run("成功", func(t *testing.T) {
		before := openFds(t)
		for range 5 {
			startAndWait(t, ok)
		}
		// pump が EOF を読んで読み端を閉じるまでは待つ。
		deadline := time.Now().Add(2 * time.Second)
		for openFds(t) > before && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if got := openFds(t); got != before {
			t.Errorf("fd が %d 本から %d 本になった（書き端を閉じていない）", before, got)
		}
	})
	t.Run("失敗", func(t *testing.T) {
		before := openFds(t)
		for range 5 {
			if _, err := ng.Start(); err == nil {
				t.Fatal("存在しないコマンドで Start が成功した")
			}
		}
		if got := openFds(t); got != before {
			t.Errorf("fd が %d 本から %d 本になった（起動に失敗したときにパイプを閉じていない）", before, got)
		}
	})
}
