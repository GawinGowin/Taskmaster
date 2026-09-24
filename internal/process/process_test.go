package process

import (
	"os"
	"strings"
	"taskmaster/internal/config"
	"testing"
)

// このファイルは内部テスト（package process）。exec.Cmd の組み立てが設計どおりか、
// 外から観測できない場所を直接見るため。API の使い勝手を見るテストは外部パッケージで書く。

// devNull は出力先として渡す *os.File を開く。New は渡されたものをそのまま
// cmd.Stdout / cmd.Stderr に入れるので、テストでも本番と同じ「有効な *os.File」を渡す。
func devNull(t *testing.T) *os.File {
	t.Helper()
	f, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("/dev/null を開けない: %v", err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

// newProcess は New を panic を握って呼ぶ。設定由来の値を添字で触る箇所があるので、
// エラーを返すべき場面で落ちていないことを見分けられるようにする。
func newProcess(t *testing.T, name string, index int, p *config.Program) (got *Process, err error, panicked any) {
	t.Helper()
	defer func() { panicked = recover() }()
	got, err = New(p, name, index, devNull(t), devNull(t))
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
// Setpgid は M-6 の Kill(-pgid) の前提で、後から足せない（起動時にしか効かない）。
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
		t.Errorf("Setpgid が立っていない: %#v （M-6 の Kill(-pgid) が届かなくなる）", cmd.SysProcAttr)
	}
	if p.name != "healthy" || p.index != 1 {
		t.Errorf("name/index = %q/%d, want healthy/1", p.name, p.index)
	}
}

// TestNew_EnvInheritsAndOverrides は env の合成規則を固定する。
//
// 設定の env は**親の環境に足す**（M-2 §1）。置き換えにすると、env: を 1 行書いた
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
// reload（M-7）で設定が差し替わっても、走行中のプロセスは起動時の値を見続ける。
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
// New で作ると、exec.Cmd は 1 回しか Start() できないので再起動（M-5）で詰まる。
func TestNew_DoesNotBuildCmd(t *testing.T) {
	p, err, panicked := newProcess(t, "p", 0, minimal())
	if panicked != nil {
		t.Fatalf("panic: %v", panicked)
	}
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if p.cmd != nil {
		t.Errorf("cmd = %#v, want nil（Start() 前に組み立てている）", p.cmd)
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
	if p.buildCmd() == p.buildCmd() {
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

// TestBuildCmd_WiresOutputFiles は渡した *os.File がそのまま cmd に入ることを見る。
// 開くのは呼び出し側の仕事（numprocs が 2 以上のとき、同じファイルを全プロセスで共有するため）。
func TestBuildCmd_WiresOutputFiles(t *testing.T) {
	out, errF := devNull(t), devNull(t)
	p, err := New(minimal(), "p", 0, out, errF)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	cmd := p.buildCmd()
	if cmd.Stdout != out {
		t.Errorf("cmd.Stdout に渡した *os.File が入っていない: %#v", cmd.Stdout)
	}
	if cmd.Stderr != errF {
		t.Errorf("cmd.Stderr に渡した *os.File が入っていない: %#v", cmd.Stderr)
	}
}

// TestNew_NilFile は nil の *os.File を弾くことを見る。
//
// cmd.Stdout は io.Writer なので、*os.File 型の nil を入れると
// 「nil ではないインターフェース値」になる。os/exec はそれを *os.File として扱い、
// Fd() が -1 を返すため、閉じた fd を渡したのと同じ状態になる
// （子が書き込みに失敗して終了コード 1。autorestart: unexpected が誤って発動する）。
// New が受け取った時点で弾けば、この状態は作れない。
func TestNew_NilFile(t *testing.T) {
	for _, tt := range []struct {
		name      string
		out, errF *os.File
	}{
		{name: "stdout が nil", out: nil, errF: devNull(t)},
		{name: "stderr が nil", out: devNull(t), errF: nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(minimal(), "p", 0, tt.out, tt.errF)
			if err == nil {
				t.Fatal("err = nil, want エラー（nil の *os.File は閉じた fd と同じ症状になる）")
			}
			t.Logf("実際のエラー出力:\n%v", err)
		})
	}
}
