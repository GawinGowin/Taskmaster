package config_test

import (
	"reflect"
	"strings"
	"taskmaster/internal/config"
	"testing"

	yaml "gopkg.in/yaml.v3"
)

// decodeCommand は Command 単体をデコードする。
// UnmarshalYAML の中で panic するとテストバイナリごと落ちるので、ここで境界を作る
// （stopsignal_test.go の decodeStopsignal と同じ理由）。
func decodeCommand(src string) (got config.Command, err error, panicked any) {
	defer func() { panicked = recover() }()
	err = yaml.Unmarshal([]byte(src), &got)
	return got, err, nil
}

// TestCommand_Scalar は文字列形式の規則を固定する。
// 順序は「展開 → 空白分割」。シェルが `c$a` を `cat Makefile` にするのと同じ向きで、
// 本家 supervisord も同じ（options.py で展開 → process.py で shlex.split）。
func TestCommand_Scalar(t *testing.T) {
	t.Setenv("TM_ROOT", "/opt/tm")
	t.Setenv("TM_MSG", "hello world") // 値に空白を含む

	tests := []struct {
		name string
		yaml string
		want config.Command
	}{
		{
			name: "課題文 VII.1 の例がそのまま通る",
			yaml: `"/usr/local/bin/nginx -c /etc/nginx/test.conf"`,
			want: config.Command{"/usr/local/bin/nginx", "-c", "/etc/nginx/test.conf"},
		},
		{name: "引数なし", yaml: "/bin/true", want: config.Command{"/bin/true"}},
		{
			name: "連続する空白とタブはまとめて区切りになる",
			yaml: `"/bin/echo   a\tb"`,
			want: config.Command{"/bin/echo", "a", "b"},
		},
		{
			name: "展開してから分割する",
			yaml: `"${TM_ROOT}/scripts/test.sh -c conf"`,
			want: config.Command{"/opt/tm/scripts/test.sh", "-c", "conf"},
		},
		// 文字列形式の罠。展開結果に空白が入れば分割される（シェルの裸の $a と同じ）。
		// 1 引数として渡したいならリスト形式で書く。ここが変わったら仕様変更。
		{
			name: "展開結果の空白でも分割される",
			yaml: `"/bin/echo ${TM_MSG}"`,
			want: config.Command{"/bin/echo", "hello", "world"},
		},
		{
			name: "$$ はリテラルの $ になり、子に $HOME を渡せる",
			yaml: `"/bin/echo $$HOME"`,
			want: config.Command{"/bin/echo", "$HOME"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err, panicked := decodeCommand(tt.yaml)
			if panicked != nil {
				t.Fatalf("panic: %v", panicked)
			}
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got = %#v, want = %#v", got, tt.want)
			}
		})
	}
}

// TestCommand_Sequence はリスト形式の規則を固定する。
// **要素は分割しない。** 分割すると「空白を含む 1 引数」を書く手段が無くなり、
// とくに空白が変数の値から来ているケース（下の 2 本目）は代替手段が存在しなくなる。
func TestCommand_Sequence(t *testing.T) {
	t.Setenv("TM_MSG", "hello world")

	tests := []struct {
		name string
		yaml string
		want config.Command
	}{
		{
			name: "空白を含む要素は 1 引数のまま",
			yaml: `["/bin/echo", "hello world"]`,
			want: config.Command{"/bin/echo", "hello world"},
		},
		{
			name: "展開結果に空白が入っても分割しない（文字列形式の唯一の逃げ道）",
			yaml: `["/bin/echo", "${TM_MSG}"]`,
			want: config.Command{"/bin/echo", "hello world"},
		},
		{name: "1 要素", yaml: `["/bin/true"]`, want: config.Command{"/bin/true"}},
		{
			name: "ブロック形式でも同じ",
			yaml: "- /bin/echo\n- hello world",
			want: config.Command{"/bin/echo", "hello world"},
		},
		// いまは数値もスカラーとして受け、文字列になる。弾くと決めたらここが赤くなる。
		{name: "数値の要素は文字列として受ける", yaml: `["/bin/echo", 42]`, want: config.Command{"/bin/echo", "42"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err, panicked := decodeCommand(tt.yaml)
			if panicked != nil {
				t.Fatalf("panic: %v", panicked)
			}
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got = %#v, want = %#v", got, tt.want)
			}
		})
	}
}

// TestCommand_QuotesRejected は「引用符を解釈しない」を黙って壊さずエラーにすることを見る。
// 文面の検査までやるのは、逃げ道（リスト形式）の存在が設定ファイルからは分からず、
// エラー文でしか伝えられないため。これが無いと利用者は引用符を消す方向
// （＝2 引数に割れる）に直しに行く。
func TestCommand_QuotesRejected(t *testing.T) {
	tests := []struct {
		name string
		yaml string
	}{
		{name: "シングルクォート", yaml: `"/bin/echo 'hello world'"`},
		{name: "ダブルクォート", yaml: `'/bin/echo "hello world"'`},
		{name: "バックスラッシュ", yaml: `'/bin/echo hello\ world'`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err, panicked := decodeCommand(tt.yaml)
			if panicked != nil {
				t.Fatalf("panic: %v", panicked)
			}
			if err == nil {
				t.Fatal("err = nil, want エラー（引用符は解釈しないので黙って割ってはいけない）")
			}
			logErr(t, err)

			msg := err.Error()
			if !strings.Contains(msg, "list") {
				t.Errorf("逃げ道が文面に無い。リスト形式で書けることを書く: %q", msg)
			}
			if strings.Contains(msg, "\n") {
				t.Errorf("エラーは 1 行に収める（ファイル名の接頭辞が 2 行目に乗らない）: %q", msg)
			}
			if strings.Contains(msg, `\"`) {
				t.Errorf(`raw string literal の中では \" はエスケープされない。素の " を書く: %q`, msg)
			}
		})
	}
}

// TestCommand_InvalidShape はスカラーでもシーケンスでもない形と、要素が入れ子の場合。
func TestCommand_InvalidShape(t *testing.T) {
	tests := []struct {
		name string
		yaml string
	}{
		{name: "マッピング", yaml: `{a: 1}`},
		{name: "要素がリスト", yaml: `[["/bin/echo"], "x"]`},
		{name: "要素がマッピング", yaml: `[{a: 1}]`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err, panicked := decodeCommand(tt.yaml)
			if panicked != nil {
				t.Fatalf("panic: %v", panicked)
			}
			if err == nil {
				t.Fatal("err = nil, want エラー")
			}
			logErr(t, err)

			// かつて ExitCodes からのコピペで "exitcodes:" と名乗っていた。
			// 利用者は cmd を直しに行くので、名前が違うのは致命的。
			if strings.Contains(err.Error(), "exitcodes") {
				t.Errorf("cmd のエラーが exitcodes と名乗っている: %q", err.Error())
			}
		})
	}
}

// TestCommand_EmptyList は空リストが Command 単体ではエラーにならないことを固定する。
// 「コマンドが無い」の判定は Program 側の 1 箇所（len(p.Cmd) == 0）に集約してあり、
// cmd 未指定 / cmd: "" / cmd: [] が同じ文面に集まる。
func TestCommand_EmptyList(t *testing.T) {
	got, err, panicked := decodeCommand(`[]`)
	if panicked != nil {
		t.Fatalf("panic: %v", panicked)
	}
	if err != nil {
		t.Fatalf("err = %v, want nil（空の判定は Program 側）", err)
	}
	if len(got) != 0 {
		t.Errorf("got = %#v, want 空", got)
	}
}

// TestCommand_EmptyElement は空文字の要素を弾くことを見る。
//
// argv[0] が空だと exec.Command("") が "exec: no command" で失敗する。
// 判定に外の世界を見る必要がなく設定ファイルの中だけで閉じているので、
// [[ADR-017]] 決定2 の線引きでは config 側で落とす対象。
// null 要素（`- ~`）も同じ経路に落ちる。yaml.v3 は null ノードで UnmarshalYAML を
// 呼ばないため EnvString のゼロ値（空文字）が残り、書き手の意図と無関係に空引数になる。
func TestCommand_EmptyElement(t *testing.T) {
	tests := []struct {
		name string
		yaml string
	}{
		{name: "argv[0] が空文字", yaml: `["", "/bin/echo"]`},
		{name: "argv[0] が null", yaml: `[~, "/bin/echo"]`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err, panicked := decodeCommand(tt.yaml)
			if panicked != nil {
				t.Fatalf("panic: %v", panicked)
			}
			if err == nil {
				t.Fatal("err = nil, want エラー（argv[0] が空だと exec が失敗する）")
			}
			logErr(t, err)
		})
	}
}
