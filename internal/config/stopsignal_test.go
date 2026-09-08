package config_test

import (
	"os"
	"path"
	"strings"
	"syscall"
	"taskmaster/internal/config"
	"testing"

	yaml "gopkg.in/yaml.v3"
)

// untouched は「Stopsignal.UnmarshalYAML が呼ばれず、値が書き換えられなかった」ことを
// 見分けるための番兵。実在しないシグナル番号を先に入れておき、デコード後も残っていれば
// デコーダがこのフィールドに触れなかったと分かる（null ノードの短絡を観測するため）。
const untouched = config.Stopsignal(-1)

// decodeStopsignal は panic を握って返す。UnmarshalYAML の中で panic すると
// テストバイナリごと落ちて他のテスト結果が読めなくなるため、ここで境界を作る。
func decodeStopsignal(src string) (got config.Stopsignal, err error, panicked any) {
	defer func() { panicked = recover() }()
	got = untouched
	err = yaml.Unmarshal([]byte(src), &got)
	return got, err, nil
}

// decodeConfigFile も同じ理由で panic を握る。
func decodeConfigFile(t *testing.T, name string) (cfg config.Config, err error, panicked any) {
	t.Helper()
	f, ferr := os.Open(path.Join("testdata", name+".yaml"))
	if ferr != nil {
		t.Fatalf("testdata %s: %v", name, ferr)
	}
	defer f.Close()

	defer func() { panicked = recover() }()
	d := yaml.NewDecoder(f)
	d.KnownFields(true)
	err = d.Decode(&cfg)
	return cfg, err, nil
}

// TestStopsignal_ShortValue は 3 文字未満の値で落ちないことを見る回帰テスト。
//
// かつて spec.go が `strings.Compare("SIG", s[:3])` で接頭辞を剥がしており、
// 3 文字未満の入力で範囲外アクセスの panic になっていた（実測: "" / "X" / "IN" で落ちる）。
// 設定ファイルの中身はユーザー入力なので、`stopsignal: X` と書かれただけで
// taskmasterd がスタックトレースを吐いて死ぬ。エラーメッセージを出すべき場所で
// panic するのは設定パーサとして最悪の壊れ方なので、ここで固定しておく。
// strings.TrimPrefix(s, "SIG") なら「あれば剥がす / 無ければそのまま」を
// 境界チェックなしで満たすので、if 自体が要らない（ADR-016 追記2）。
func TestStopsignal_ShortValue(t *testing.T) {
	// 短いうえに無効な値。panic せずエラーを返すのが正。
	for _, src := range []string{`""`, "ab", "E", "S", "SI"} {
		t.Run(src, func(t *testing.T) {
			got, err, panicked := decodeStopsignal(src)
			if panicked != nil {
				t.Fatalf("panic: %v （接頭辞の剥がしが 3 文字未満で落ちている）", panicked)
			}
			if err == nil {
				t.Errorf("err = nil (got = %d), want error", int(got))
			}
		})
	}
}

func TestStopsignal_UnmarshalYAML(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want config.Stopsignal
		// wantErr が true なら want は見ない。errContains は空なら照合しない。
		wantErr     bool
		errContains string
	}{
		// 正常系: availableSignal のキーを一巡する
		{name: "TERM", yaml: "TERM", want: config.Stopsignal(syscall.SIGTERM)},
		{name: "HUP", yaml: "HUP", want: config.Stopsignal(syscall.SIGHUP)},
		{name: "INT", yaml: "INT", want: config.Stopsignal(syscall.SIGINT)},
		{name: "QUIT", yaml: "QUIT", want: config.Stopsignal(syscall.SIGQUIT)},
		{name: "KILL", yaml: "KILL", want: config.Stopsignal(syscall.SIGKILL)},
		{name: "USR1", yaml: "USR1", want: config.Stopsignal(syscall.SIGUSR1)},
		{name: "USR2", yaml: "USR2", want: config.Stopsignal(syscall.SIGUSR2)},
		{name: "引用符で囲んでも同じ", yaml: `"TERM"`, want: config.Stopsignal(syscall.SIGTERM)},

		// SIG 接頭辞は受ける / 大文字小文字は受けない（ADR-016 追記2 で決定2 を一部修正）。
		// 根拠が違うので分けている。SIGTERM は signal(7) に載っている OS 側の正式名称で、
		// TERM のほうが supervisord/taskmaster 側の略記。つまり接頭辞を受けるのは
		// 表記ゆれの吸収ではなく OS の語彙の受け入れ。小文字にはその正当化が無い。
		{name: "SIG 接頭辞つきも通る", yaml: "SIGTERM", want: config.Stopsignal(syscall.SIGTERM)},
		{name: "SIG 接頭辞つきの別シグナル", yaml: "SIGKILL", want: config.Stopsignal(syscall.SIGKILL)},
		{name: "小文字はエラー（本家は upper() するので通る）", yaml: "term", wantErr: true,
			errContains: `unknown stopsignal "term"`},
		{name: "小文字 + SIG 接頭辞もエラー", yaml: "sigterm", wantErr: true,
			errContains: `unknown stopsignal "sigterm"`},

		// null は Unmarshaler を呼ばずに素通りする（実測）。
		// Program 経由だと「既定値が残る」= 書き忘れが黙って TERM になる、という意味になる。
		{name: "~ は Unmarshaler が呼ばれず値が変わらない", yaml: "~", want: untouched},
		{name: "null は Unmarshaler が呼ばれず値が変わらない", yaml: "null", want: untouched},

		// 異常系: テーブル引き当ての失敗（自前のメッセージ）
		{name: "未知のシグナル名はエラー", yaml: "NOPE", wantErr: true,
			errContains: `unknown stopsignal "NOPE"`},
		{name: "実在するがサポート外のシグナルはエラー", yaml: "SEGV", wantErr: true,
			errContains: `unknown stopsignal "SEGV"`},
		{name: "前後の空白は落とさない", yaml: `"TERM "`, wantErr: true,
			errContains: `unknown stopsignal "TERM "`},
		{name: "真偽値に見えるスカラーはエラー", yaml: "true", wantErr: true,
			errContains: `unknown stopsignal "true"`},
		// エラーには剥がした後ではなく利用者が書いた元の文字列を出す（ADR-016 追記2）。
		// SIGNOPE と書いたのに NOPE を指摘されると、書いていないものを指摘されることになる。
		// ルックアップ用の変数とメッセージ用の元文字列を分ければよい。
		{name: "エラーには入力そのものが出る", yaml: "SIGNOPE", wantErr: true,
			errContains: `unknown stopsignal "SIGNOPE"`},

		// 数値表記（ADR-016 追記1 で決定1 を修正し、受けることにした）。
		// 番号は Linux の値。GOOS を跨ぐなら固定できない。
		{name: "15 は SIGTERM", yaml: "15", want: config.Stopsignal(syscall.SIGTERM)},
		{name: "1 は SIGHUP", yaml: "1", want: config.Stopsignal(syscall.SIGHUP)},
		{name: "9 は SIGKILL", yaml: "9", want: config.Stopsignal(syscall.SIGKILL)},
		// 決定3（ホワイトリスト）は数値パスにこそ効く。範囲チェックにすると
		// 11(SEGV) が通ってしまい availableSignal が飾りになる。
		{name: "ホワイトリスト外の番号はエラー", yaml: "11", wantErr: true,
			errContains: "unsupported stopsignal: 11"},
		// 0 は kill(2) では「シグナルを送らず存在確認だけ」。設定から書き込めてはいけない。
		{name: "0 はエラー", yaml: "0", wantErr: true,
			errContains: "unsupported stopsignal: 0"},
		{name: "負の番号はエラー", yaml: "-1", wantErr: true,
			errContains: "unsupported stopsignal: -1"},
		{name: "範囲外の番号はエラー", yaml: "99999", wantErr: true,
			errContains: "unsupported stopsignal: 99999"},
		// クォートすると !!str になるので名前として引かれる。Kind はどちらも ScalarNode なので、
		// 名前と数値の分岐は n.Tag（!!str / !!int）でしか書けない（実測）。
		{name: "引用符つきの数値は名前として扱われエラー", yaml: `"15"`, wantErr: true,
			errContains: `unknown stopsignal "15"`},
		// YAML 1.1 の「先頭ゼロ = 8 進」が残っているので 011 は 11 ではなく 9 になる。
		// SIGSEGV のつもりが SIGKILL として通る。ADR-016 では未解決なので、
		// 「いまはこう読んでいる」を固定しておく（仕様を変えたらここが赤くなる）。
		{name: "011 は 8 進で 9 = SIGKILL になる", yaml: "011", want: config.Stopsignal(syscall.SIGKILL)},

		// 異常系: スカラー以外
		{name: "リストはエラー", yaml: "[TERM]", wantErr: true,
			errContains: "stopsignal must be a signal name"},
		{name: "マッピングはエラー", yaml: "{a: TERM}", wantErr: true,
			errContains: "stopsignal must be a signal name"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err, panicked := decodeStopsignal(tt.yaml)
			if panicked != nil {
				t.Fatalf("panic: %v", panicked)
			}

			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tt.wantErr)
			}
			if tt.wantErr {
				if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("err = %q, want contains %q", err, tt.errContains)
				}
				return
			}
			if got != tt.want {
				t.Errorf("got = %d, want = %d", int(got), int(tt.want))
			}
		})
	}
}

// TestStopsignal_NullLeavesZeroValue は ADR-016 の未解決の問い
// 「Stopsignal のゼロ値が危険」の入口を固定する。
//
// 実測: yaml.v3 は null ノードを短絡して Unmarshaler を呼ばない。ADR-016 が書いている
// 「null は "" にデコードされて最終的に map 引きで落ちる」は起きず、値は触られないまま残る。
// つまりゼロ値の Stopsignal に null を入れると 0 のまま通る。syscall.Signal(0) は
// kill(2) では「シグナルを送らず存在確認だけ」なので、停止処理に流れるとエラーも出ずに
// プロセスが止まらない。Program 経由なら既定値 TERM が残るので実害は無い（下の表で確認）が、
// それは Program.UnmarshalYAML が守っているだけで、型自身は 0 を防いでいない。
func TestStopsignal_NullLeavesZeroValue(t *testing.T) {
	var got config.Stopsignal // ゼロ値 = syscall.Signal(0)
	if err := yaml.Unmarshal([]byte("null"), &got); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if got != 0 {
		t.Fatalf("got = %d, want = 0 (null は Unmarshaler を呼ばず値を触らない)", int(got))
	}
}

// TestStopsignal_ErrorLine は「何行目が悪いのか」がエラーに出ることを見る。
// wantErr bool だけの表では落ちない、行番号の付け間違いを拾うため。
func TestStopsignal_ErrorLine(t *testing.T) {
	// stopsignal は 8 行目（コメント 3 行 + programs + p + cmd + numprocs）。
	const src = `# 1
# 2
# 3
programs:
  p:
    cmd: /bin/true
    numprocs: 2
    stopsignal: NOPE
`
	var cfg config.Config
	err := yaml.Unmarshal([]byte(src), &cfg)
	if err == nil {
		t.Fatal("err = nil, want error")
	}
	if !strings.Contains(err.Error(), "line 8") {
		t.Errorf("err = %q, want contains %q", err, "line 8")
	}
}

func TestProgram_Stopsignal(t *testing.T) {
	tests := []struct {
		name     string
		yamlFile string // testdata/<yamlFile>.yaml
		program  string // Config.Programs から取り出すキー
		want     config.Stopsignal
		wantErr  bool
	}{
		// 正常系
		{name: "未指定なら既定の TERM", yamlFile: "valid_minimal", program: "minimal",
			want: config.Stopsignal(syscall.SIGTERM)},
		{name: "TERM", yamlFile: "valid_stopsignal_names", program: "term",
			want: config.Stopsignal(syscall.SIGTERM)},
		{name: "HUP", yamlFile: "valid_stopsignal_names", program: "hup",
			want: config.Stopsignal(syscall.SIGHUP)},
		{name: "INT", yamlFile: "valid_stopsignal_names", program: "int",
			want: config.Stopsignal(syscall.SIGINT)},
		{name: "QUIT", yamlFile: "valid_stopsignal_names", program: "quit",
			want: config.Stopsignal(syscall.SIGQUIT)},
		{name: "KILL", yamlFile: "valid_stopsignal_names", program: "kill",
			want: config.Stopsignal(syscall.SIGKILL)},
		{name: "USR1", yamlFile: "valid_stopsignal_names", program: "usr1",
			want: config.Stopsignal(syscall.SIGUSR1)},
		{name: "USR2", yamlFile: "valid_stopsignal_names", program: "usr2",
			want: config.Stopsignal(syscall.SIGUSR2)},

		// SIG 接頭辞は受ける（ADR-016 追記2。testdata のコメント参照）
		{name: "SIG 接頭辞つきも通る", yamlFile: "valid_stopsignal_sig_prefix", program: "p",
			want: config.Stopsignal(syscall.SIGTERM)},

		// 数値表記（ADR-016 追記1）
		{name: "数値の 15 は SIGTERM", yamlFile: "valid_stopsignal_numeric", program: "term",
			want: config.Stopsignal(syscall.SIGTERM)},
		{name: "数値の 1 は SIGHUP", yamlFile: "valid_stopsignal_numeric", program: "hup",
			want: config.Stopsignal(syscall.SIGHUP)},
		{name: "数値の 9 は SIGKILL", yamlFile: "valid_stopsignal_numeric", program: "kill",
			want: config.Stopsignal(syscall.SIGKILL)},

		// エッジ: 値なしはエラーにならず既定値が残る（yaml.v3 が null を短絡するため）。
		// エラーにする仕様に変えるならここが wantErr: true に反転する。
		{name: "stopsignal: （値なし）は既定の TERM のまま通る", yamlFile: "edge_stopsignal_null", program: "p",
			want: config.Stopsignal(syscall.SIGTERM)},

		// エッジ: 8 進の罠。011 は 11(SEGV) ではなく 9(KILL) になる（ADR-016 では未解決）
		{name: "011 は 8 進で 9 = SIGKILL になる", yamlFile: "edge_stopsignal_octal", program: "p",
			want: config.Stopsignal(syscall.SIGKILL)},

		// 異常系
		{name: "未知のシグナル名はエラー", yamlFile: "invalid_stopsignal_unknown", program: "p", wantErr: true},
		{name: "シグナル名は大文字小文字を区別する", yamlFile: "invalid_stopsignal_case", program: "p", wantErr: true},
		{name: "実在するがサポート外のシグナルはエラー", yamlFile: "invalid_stopsignal_unsupported", program: "p", wantErr: true},
		{name: "シグナル名が文字列でなければエラー", yamlFile: "invalid_stopsignal_not_string", program: "p", wantErr: true},
		{name: "ホワイトリスト外の番号はエラー", yamlFile: "invalid_stopsignal_numeric_unsupported", program: "p", wantErr: true},
		{name: "番号 0 はエラー", yamlFile: "invalid_stopsignal_zero", program: "p", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err, panicked := decodeConfigFile(t, tt.yamlFile)
			if panicked != nil {
				t.Fatalf("panic: %v", panicked)
			}
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			got, ok := cfg.Programs[tt.program]
			if !ok {
				t.Fatalf("program %q not found in %s.yaml", tt.program, tt.yamlFile)
			}
			if got.Stopsignal != tt.want {
				t.Errorf("Stopsignal = %d, want = %d", int(got.Stopsignal), int(tt.want))
			}
		})
	}
}
