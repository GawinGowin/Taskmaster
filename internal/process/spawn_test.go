package process

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"taskmaster/internal/config"
	"testing"
)

// childGuard は踏み台を起動するテストが子に渡す印。
// 子として起動されたテストバイナリが踏み台の判定に外れたとき、テストを走らせずに止まるために使う。
const childGuard = "TM_PROCESS_TEST_CHILD"

// TestMain はテストバイナリ自身を踏み台として使えるようにする。
//
// buildCmd は /proc/self/exe を exec するので、go test の中ではテストバイナリが踏み台になる。
// main() と同じ分岐をここに置けば、本番と同じ IsSpawned / RunSpawn を通せる。
// 分岐に外れると、子がテストをもう一度走らせ、その中でまた踏み台を起動する
// （際限なく自分を起動し続ける）。childGuard が付いているのに外れたら、そこで止める。
func TestMain(m *testing.M) {
	if IsSpawned(os.Args) {
		code, err := RunSpawn(os.Args)
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(code)
	}
	if os.Getenv(childGuard) != "" {
		fmt.Fprintf(os.Stderr, "踏み台として起動されたのに IsSpawned が false: %q\n", os.Args)
		os.Exit(3)
	}
	os.Exit(m.Run())
}

// runEnv は prog を Start() し、子の `env -0` の出力を並べ替えて返す。
func runEnv(t *testing.T, prog *config.Program) []string {
	t.Helper()
	out, err := os.CreateTemp(t.TempDir(), "env")
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()

	p, err := New(prog, "p", 0, out, devNull(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	wait, err := p.Start()
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	ps, err := wait()
	if err != nil || !ps.Success() {
		t.Fatalf("子が失敗した: %v, %v", ps, err)
	}

	b, err := os.ReadFile(out.Name())
	if err != nil {
		t.Fatal(err)
	}
	env := strings.Split(strings.TrimSuffix(string(b), "\x00"), "\x00")
	slices.Sort(env)
	return env
}

// TestStart_EnvSameThroughSpawn は踏み台を経由しても Cmd.Env がそのまま届くことを見る。
//
// 踏み台は os.Environ() を syscall.Exec に横流しするだけで、env に印も入れない。
// 直接経路と踏み台経路で同じ program を起動し、子が見た環境変数の集合が一致することで固定する。
// 値に改行が入っても行がずれないように `env -0` で NUL 区切りにする。
// cmd は名前だけで書き、親の LookPath で解決したパスが踏み台に渡る経路も通す。
func TestStart_EnvSameThroughSpawn(t *testing.T) {
	t.Setenv(childGuard, "1")
	t.Setenv("TM_TEST_INHERITED", "from-parent")
	t.Setenv("TM_TEST_OVERRIDE", "from-parent")

	prog := func(umask *int) *config.Program {
		return &config.Program{
			Cmd:   config.Command{"env", "-0"},
			Env:   map[string]string{"TM_TEST_OVERRIDE": "from-config", "TM_TEST_NEW": "added\nwith newline"},
			Umask: umask,
		}
	}
	mask := 0o077
	direct := runEnv(t, prog(nil))
	spawned := runEnv(t, prog(&mask))

	// 全体は出さない（実行環境のトークン類がログに残る）。食い違った要素だけを出す。
	if !slices.Equal(direct, spawned) {
		t.Errorf("踏み台経由で環境変数が変わった\n踏み台にだけある: %q\n直接にだけある:   %q",
			missing(spawned, direct), missing(direct, spawned))
	}
	for _, kv := range []string{
		"TM_TEST_INHERITED=from-parent",
		"TM_TEST_OVERRIDE=from-config",
		"TM_TEST_NEW=added\nwith newline",
	} {
		if n := countPrefix(spawned, kv[:strings.Index(kv, "=")+1]); n != 1 || !slices.Contains(spawned, kv) {
			t.Errorf("踏み台経由の子に %q が 1 つだけ届いていない（同名キーの数 %d）", kv, n)
		}
	}
}

// missing は a にあって b に無い要素を返す。
func missing(a, b []string) []string {
	var out []string
	for _, e := range a {
		if !slices.Contains(b, e) {
			out = append(out, e)
		}
	}
	return out
}

func countPrefix(env []string, prefix string) int {
	n := 0
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			n++
		}
	}
	return n
}
