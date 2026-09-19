package config_test

import (
	"os"
	"strings"
	"taskmaster/internal/config"
	"testing"

	yaml "gopkg.in/yaml.v3"
)

// unsetEnv は「その変数が存在しない」状態をテストの中だけで作る。
// testing には Unsetenv が無いので、t.Setenv で元の値を覚えさせて（テスト後に復元される）
// から os.Unsetenv で消す。TM_ROOT は mise が与えているため、
// 未定義のケースは自分で作らないと再現できない。
func unsetEnv(t *testing.T, key string) {
	t.Helper()
	t.Setenv(key, "")
	os.Unsetenv(key)
}

// decodeEnvString は EnvString 単体をデコードする。
// UnmarshalYAML の中で panic するとテストバイナリごと落ちるので、ここで境界を作る
// （stopsignal_test.go の decodeStopsignal と同じ理由）。
func decodeEnvString(src string) (got config.EnvString, err error, panicked any) {
	defer func() { panicked = recover() }()
	err = yaml.Unmarshal([]byte(src), &got)
	return got, err, nil
}

func TestEnvString_Expand(t *testing.T) {
	t.Setenv("TM_ROOT", "/opt/tm")
	t.Setenv("TM_EMPTY", "") // 定義されているが空

	tests := []struct {
		name string
		yaml string
		want string
	}{
		{name: "波括弧つきは展開される", yaml: `"${TM_ROOT}/scripts/test.sh"`, want: "/opt/tm/scripts/test.sh"},
		{name: "1 行に 2 つ", yaml: `"${TM_ROOT}:${TM_ROOT}"`, want: "/opt/tm:/opt/tm"},
		// 裸の $VAR も展開される。波括弧必須にはしないと決めたので、仕様として固定する。
		// これが嫌になったら自前スキャナを書く話になり、そのときここが赤くなる。
		{name: "波括弧なしも展開される", yaml: `"$TM_ROOT/x"`, want: "/opt/tm/x"},
		{name: "$$ はリテラルの $", yaml: `"a$$b"`, want: "a$b"},
		{name: "$$ で展開から逃がせる", yaml: `"sh -c 'echo $$HOME'"`, want: "sh -c 'echo $HOME'"},
		{name: "変数が無ければそのまま", yaml: `"/bin/true"`, want: "/bin/true"},
		{name: "空文字はそのまま", yaml: `""`, want: ""},
		// 定義されていれば値が空でもエラーにしない。「未定義」と「空」は別物。
		{name: "定義済みで空の変数は空に展開される", yaml: `"[${TM_EMPTY}]"`, want: "[]"},
		{name: "末尾の $ は残る", yaml: `"100$"`, want: "100$"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err, panicked := decodeEnvString(tt.yaml)
			if panicked != nil {
				t.Fatalf("panic: %v", panicked)
			}
			if err != nil {
				t.Fatalf("err = %v, want nil", err)
			}
			if string(got) != tt.want {
				t.Errorf("got = %q, want = %q", string(got), tt.want)
			}
		})
	}
}

func TestEnvString_Errors(t *testing.T) {
	t.Setenv("TM_ROOT", "/opt/tm")
	unsetEnv(t, "TM_UNDEFINED_A")
	unsetEnv(t, "TM_UNDEFINED_B")

	tests := []struct {
		name        string
		yaml        string
		errContains string // 空なら照合しない
	}{
		{name: "未定義の変数はエラー", yaml: `"${TM_UNDEFINED_A}/x"`, errContains: "TM_UNDEFINED_A"},
		{name: "波括弧なしの未定義もエラー", yaml: `"$TM_UNDEFINED_A/x"`, errContains: "TM_UNDEFINED_A"},
		// 複数あるときは最初の 1 つが出る。全部並べる実装にしても最初の名前は含まれるので、
		// どちらの作りでも通る書き方にしてある。
		{name: "未定義が複数あっても最初の名前が出る", yaml: `"${TM_UNDEFINED_A}/${TM_UNDEFINED_B}"`, errContains: "TM_UNDEFINED_A"},
		// os.Expand の mapping はこの 2 つでは呼ばれない（実測）。
		// 放っておくと ${} は空文字へ潰れ、閉じ忘れは "${" だけ消えて通ってしまう。
		{name: "空の変数名はエラー", yaml: `"${}/x"`},
		{name: "閉じ忘れはエラー", yaml: `"${TM_ROOT/x"`},
		// スカラー以外。他のフィールド（stopsignal / exitcodes）と揃える。
		{name: "リストはエラー", yaml: `[a, b]`},
		{name: "マッピングはエラー", yaml: `{a: b}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err, panicked := decodeEnvString(tt.yaml)
			if panicked != nil {
				t.Fatalf("panic: %v", panicked)
			}
			logErr(t, err)
			if err == nil {
				t.Fatalf("エラーにならなかった。got = %q", string(got))
			}
			if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
				t.Errorf("エラーに %q が含まれていない: %v", tt.errContains, err)
			}
		})
	}
}

// TestEnvString_ErrorHasLine はエラーに行番号が出ること。
// 他のフィールドのエラーが "line N: ..." で揃っているので、ここだけ揃わないと
// 利用者が設定ファイルのどこを直せばよいか分からない。
func TestEnvString_ErrorHasLine(t *testing.T) {
	unsetEnv(t, "TM_UNDEFINED_A")

	// cmd は 3 行目
	src := "programs:\n  p:\n    cmd: \"${TM_UNDEFINED_A}\"\n"
	_, err := config.LoadFrom(strings.NewReader(src), "probe.yaml")
	logErr(t, err)
	if err == nil {
		t.Fatal("未定義の変数がエラーにならなかった")
	}
	if !strings.Contains(err.Error(), "line 3") {
		t.Errorf("エラーに行番号 line 3 が出ていない: %v", err)
	}
}

// TestProgram_EnvExpansion は展開するフィールドとしないフィールドの境界。
func TestProgram_EnvExpansion(t *testing.T) {
	t.Run("TM_ROOT が定義されていれば cmd と workingdir が展開される", func(t *testing.T) {
		t.Setenv("TM_ROOT", "/opt/tm")

		cfg, err := config.Load("testdata/valid_env_expansion.yaml")
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		p := cfg.Programs["p"]
		if string(p.Cmd) != "/opt/tm/scripts/test.sh" {
			t.Errorf("cmd = %q", string(p.Cmd))
		}
		if string(p.Workingdir) != "/opt/tm" {
			t.Errorf("workingdir = %q", string(p.Workingdir))
		}
		// env の値は展開対象外。ここを展開する実装に変えたらこのテストが赤くなる。
		if p.Env["LITERAL"] != "${TM_ROOT}" {
			t.Errorf("env の値が展開されている: %q", p.Env["LITERAL"])
		}
	})

	t.Run("TM_ROOT が未設定なら読み込みに失敗する", func(t *testing.T) {
		unsetEnv(t, "TM_ROOT")

		_, err := config.Load("testdata/valid_env_expansion.yaml")
		logErr(t, err)
		if err == nil {
			t.Fatal("TM_ROOT が未設定なのに読めてしまった")
		}
		if !strings.Contains(err.Error(), "TM_ROOT") {
			t.Errorf("エラーに変数名が出ていない: %v", err)
		}
	})
}
