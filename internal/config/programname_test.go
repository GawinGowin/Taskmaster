package config_test

import (
	"fmt"
	"strings"
	"taskmaster/internal/config"
	"testing"
)

// program 名は表示用のラベルではなく識別子の構成要素で、ビュー行・イベント id・
// シェルの引数・イベントログに name:index/pid の形で出る。名前に : や / が入ると
// healthy:0:1/4064 になってパースが曖昧になり、空白が入れば stop my app が 2 引数に割れる。
//
// 検査はデコード後（設定全体が揃ってから）に行う前提で書いている。したがって
// エラーに行番号は求めず、**名前そのものが出ること**だけを見る。
// 名前は識別子なので、利用者はファイルを名前で検索できる。

// programWithName は名前だけを差し替えた最小の設定を作る。
// 不正な名前は 1 ファイル 1 ケースになり testdata が増えすぎるので、
// キーの文字種だけが論点のこのテストではインラインで組み立てる。
func programWithName(name string) string {
	return fmt.Sprintf("programs:\n  %s:\n    cmd: /bin/true\n", name)
}

func TestConfig_InvalidProgramName(t *testing.T) {
	tests := []struct {
		name     string
		yamlKey  string // YAML に書くキー（引用符も含めてそのまま）
		inMsg    string // エラーに出てほしい文字列
		whyBroke string
	}{
		{
			name:    "コロンはビュー書式の区切りと衝突する",
			yamlKey: `"a:0"`, inMsg: `"a:0"`,
			whyBroke: "a:0:1/4064 になって index との境目が読めない",
		},
		{
			name:    "スラッシュもビュー書式の区切り",
			yamlKey: `"a/b"`, inMsg: `"a/b"`,
			whyBroke: "a/b:0/4064 の pid との境目が読めない",
		},
		{
			name:    "空白はシェルの引数が割れる",
			yamlKey: `"my app"`, inMsg: `"my app"`,
			whyBroke: "stop my app が 2 引数になる",
		},
		{
			name:    "空文字",
			yamlKey: `""`, inMsg: `""`,
			whyBroke: ":0/4064 になって名前が消える",
		},
		{
			name:    "先頭のハイフンはフラグに見える",
			yamlKey: `"-app"`, inMsg: `"-app"`,
			whyBroke: "stop -app がフラグとして解釈されうる",
		},
		{
			name:    "先頭のドット",
			yamlKey: `".app"`, inMsg: `".app"`,
			whyBroke: "隠しファイル風で、シェル補完とも相性が悪い",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := programWithName(tt.yamlKey)
			got, err := config.LoadFrom(strings.NewReader(src), "probe.yaml")
			logErr(t, err)
			if err == nil {
				t.Fatalf("エラーにならなかった（%s）。読めた program = %v", tt.whyBroke, keysOf(got))
			}
			if !strings.Contains(err.Error(), tt.inMsg) {
				t.Errorf("エラーに名前 %s が出ていない: %v", tt.inMsg, err)
			}
		})
	}
}

func TestConfig_ValidProgramNames(t *testing.T) {
	cfg, err := loadFile(t, "valid_program_names")
	logErr(t, err)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	for _, name := range []string{"nginx", "vogsphere", "web.1", "my-app", "_internal", "App2"} {
		if _, ok := cfg.Programs[name]; !ok {
			t.Errorf("program %q が読めていない（読めたのは %v）", name, keysOf(cfg))
		}
	}
}

// TestConfig_RealConfigNamesPass は回帰の当て先。
// 検証を厳しくしすぎて、実際に使っている設定が読めなくなっていないかを見る。
func TestConfig_RealConfigNamesPass(t *testing.T) {
	t.Setenv("TM_ROOT", "/tmp/tm-root-for-test")

	cfg, err := config.Load("../../configs/taskmasterd.yaml")
	logErr(t, err)
	if err != nil {
		t.Fatalf("実際の設定が読めなくなっている: %v", err)
	}
	if len(cfg.Programs) != 12 {
		t.Errorf("program = %d 件, want 12（%v）", len(cfg.Programs), keysOf(cfg))
	}
}

// TestConfig_InvalidProgramNameIsDeterministic は、不正な名前が 2 つあるときに
// **毎回同じエラーが出る**こと。map の反復順は不定なので、キーをソートせずに
// 検査するとエラーに出る名前が実行ごとに変わる（テストが不安定になるだけでなく、
// 同じ設定を読んで違うエラーが出るという挙動自体がおかしい）。
func TestConfig_InvalidProgramNameIsDeterministic(t *testing.T) {
	src := "programs:\n  \"a:0\":\n    cmd: /bin/true\n  \"b/1\":\n    cmd: /bin/true\n"

	var first string
	for i := 0; i < 20; i++ {
		_, err := config.LoadFrom(strings.NewReader(src), "probe.yaml")
		if err == nil {
			t.Fatal("不正な名前がエラーにならなかった")
		}
		if i == 0 {
			first = err.Error()
			t.Logf("実際のエラー出力:\n%s", first)
			continue
		}
		if err.Error() != first {
			t.Fatalf("実行ごとにエラーが変わる（キーをソートしていない）:\n 1 回目: %s\n %d 回目: %s", first, i+1, err.Error())
		}
	}
}

// TestConfig_NullProgram は、program の値を書かなかったとき（null）にエラーになること。
// yaml.v3 は値が null だと Program.UnmarshalYAML を呼ばずに 0 値を入れるので、
// 既定値も cmd is required / numprocs >= 1 の検査も通らず、cmd が空で numprocs が 0 の program が読めてしまう。
// 検査はデコード後に program 名の検査と同じ場所で行うので、行番号は求めず、名前が引用符付きで出ることだけを見る。
func TestConfig_NullProgram(t *testing.T) {
	tests := []struct {
		name  string
		value string // a: のあとに書く値
	}{
		{name: "値なし", value: ""},
		{name: "チルダ", value: " ~"},
		{name: "null", value: " null"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := "programs:\n  a:" + tt.value + "\n  b:\n    cmd: /bin/true\n"
			got, err := config.LoadFrom(strings.NewReader(src), "probe.yaml")
			logErr(t, err)
			if err == nil {
				t.Fatalf("エラーにならなかった。a = %#v", got.Programs["a"])
			}
			if !strings.Contains(err.Error(), `"a"`) {
				t.Errorf("エラーに名前 \"a\" が出ていない: %v", err)
			}
		})
	}
}

// TestConfig_NullProgramIsDeterministic は、null の program が 2 つあるとき、
// 毎回名前の昇順で最初のもの（a）を報告すること。理由は TestConfig_InvalidProgramNameIsDeterministic と同じ。
func TestConfig_NullProgramIsDeterministic(t *testing.T) {
	src := "programs:\n  b:\n  a:\n"

	var first string
	for i := 0; i < 20; i++ {
		_, err := config.LoadFrom(strings.NewReader(src), "probe.yaml")
		if err == nil {
			t.Fatal("null の program がエラーにならなかった")
		}
		if i == 0 {
			first = err.Error()
			t.Logf("実際のエラー出力:\n%s", first)
			if !strings.Contains(first, `"a"`) {
				t.Errorf("名前の昇順で最初の \"a\" を報告していない: %s", first)
			}
			continue
		}
		if err.Error() != first {
			t.Fatalf("実行ごとにエラーが変わる（キーをソートしていない）:\n 1 回目: %s\n %d 回目: %s", first, i+1, err.Error())
		}
	}
}

func keysOf(c *config.Config) []string {
	if c == nil {
		return nil
	}
	ks := make([]string, 0, len(c.Programs))
	for k := range c.Programs {
		ks = append(ks, k)
	}
	return ks
}
