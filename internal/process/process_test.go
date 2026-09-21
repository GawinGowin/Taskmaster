package process

import (
	"strings"
	"taskmaster/internal/config"
	"testing"
)

// このファイルは内部テスト（package process）。exec.Cmd の組み立てが設計どおりか、
// 外から観測できない場所を直接見るため。API の使い勝手を見るテストは外部パッケージで書く。

// newProcess は New を panic を握って呼ぶ。設定由来の値を添字で触る箇所があるので、
// エラーを返すべき場面で落ちていないことを見分けられるようにする。
func newProcess(name string, index int, p *config.Program) (got *Process, err error, panicked any) {
	defer func() { panicked = recover() }()
	got, err = New(name, index, p)
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
			_, err, panicked := newProcess("p", 0, tt.prog)
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

// TestNew_WiresCmd は exec.Cmd の組み立てを見る。
// Setpgid は M-6 の Kill(-pgid) の前提で、後から足せない（起動時にしか効かない）。
func TestNew_WiresCmd(t *testing.T) {
	prog := minimal()
	prog.Cmd = config.Command{"/bin/echo", "hello world"}
	prog.Workingdir = "/tmp"

	p, err, panicked := newProcess("healthy", 1, prog)
	if panicked != nil {
		t.Fatalf("panic: %v", panicked)
	}
	if err != nil {
		t.Fatalf("err = %v", err)
	}

	if p.cmd.Path != "/bin/echo" {
		t.Errorf("Path = %q, want /bin/echo", p.cmd.Path)
	}
	// 空白を含む引数が 1 つのまま渡ること（config のリスト形式が活きる経路）。
	if len(p.cmd.Args) != 2 || p.cmd.Args[1] != "hello world" {
		t.Errorf("Args = %#v, want [/bin/echo, \"hello world\"]", p.cmd.Args)
	}
	if p.cmd.Dir != "/tmp" {
		t.Errorf("Dir = %q, want /tmp", p.cmd.Dir)
	}
	if p.cmd.SysProcAttr == nil || !p.cmd.SysProcAttr.Setpgid {
		t.Errorf("Setpgid が立っていない: %#v （M-6 の Kill(-pgid) が届かなくなる）", p.cmd.SysProcAttr)
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
func TestNew_EnvInheritsAndOverrides(t *testing.T) {
	t.Setenv("TM_TEST_INHERITED", "from-parent")
	t.Setenv("TM_TEST_OVERRIDE", "from-parent")

	prog := minimal()
	prog.Env = map[string]string{"TM_TEST_OVERRIDE": "from-config", "TM_TEST_NEW": "added"}

	p, err, panicked := newProcess("p", 0, prog)
	if panicked != nil {
		t.Fatalf("panic: %v", panicked)
	}
	if err != nil {
		t.Fatalf("err = %v", err)
	}

	if !contains(p.cmd.Env, "TM_TEST_INHERITED=from-parent") {
		t.Error("親の環境が引き継がれていない（置き換えになっている）")
	}
	if !contains(p.cmd.Env, "TM_TEST_NEW=added") {
		t.Error("設定の env が足されていない")
	}
	// 重複は「最後が勝つ」ので、設定の値が親の値より後ろに無ければならない。
	if last(p.cmd.Env, "TM_TEST_OVERRIDE=") != "TM_TEST_OVERRIDE=from-config" {
		t.Errorf("同名キーで設定が勝っていない: %q", last(p.cmd.Env, "TM_TEST_OVERRIDE="))
	}
}

// TestNew_EnvNilInherits は env を書かなかったときに nil のままであることを見る。
// nil は os/exec にとって「親の環境をそのまま使う」の意味。
func TestNew_EnvNilInherits(t *testing.T) {
	p, err, panicked := newProcess("p", 0, minimal())
	if panicked != nil {
		t.Fatalf("panic: %v", panicked)
	}
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if p.cmd.Env != nil {
		t.Errorf("Env = %#v, want nil（未指定は継承）", p.cmd.Env)
	}
}

// TestNew_PinsSpec は起動時の spec を握ることを見る。
// reload（M-7）で設定が差し替わっても、走行中のプロセスは起動時の値を見続ける。
func TestNew_PinsSpec(t *testing.T) {
	prog := minimal()
	p, err, panicked := newProcess("p", 0, prog)
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
