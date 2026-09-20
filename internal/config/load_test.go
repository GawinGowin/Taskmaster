package config_test

import (
	"os"
	"reflect"
	"sort"
	"strings"
	"taskmaster/internal/config"
	"testing"
)

// このファイルは設定の入口（Load / LoadFrom）の受け入れテスト。
// 狙いは「設定の読み方を知っているのは config パッケージだけ」という状態を固定すること。
// したがって、ここでは yaml パッケージを import しない。
// import した時点で「テストが本番と違う経路を検証している」状態に戻る。
//
// エラーの本文は t.Logf で全ケース出す（`go test -v` で見える）。
// 「不明な値は黙って通さずエラーにする」方針を採っている以上、
// 出たエラーが利用者に読めるかどうかは合否と同じくらい見る価値がある。
// 見るときは:  go test -v ./internal/config/

// logErr は実際のエラー本文をコンソールに出す。
// アサーションではなく観察用。何を読むべきかを添える。
func logErr(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Logf("err = nil")
		return
	}
	t.Logf("実際のエラー出力:\n%v", err)
}

// loadFile は testdata/<name>.yaml を LoadFrom に通す。
// name をそのままエラー表示用の名前としても渡す（本番の Load と同じ形）。
func loadFile(t *testing.T, name string) (*config.Config, error) {
	t.Helper()
	p := testdataPath(t, name)
	f, err := os.Open(p)
	if err != nil {
		t.Fatalf("testdata %s: %v", name, err)
	}
	defer f.Close()
	return config.LoadFrom(f, p)
}

// TestLoadFrom_TopLevelUnknownField は入口を 1 本にする動機そのもの。
// トップレベルの未知キーは Decoder.KnownFields(true) を立てたときだけエラーになる、
// つまり「呼び出し側が正しく組み立てたか」に依存する唯一の検査だった。
// LoadFrom の中に閉じた以降は、入口を通ったかどうかだけで決まる。
func TestLoadFrom_TopLevelUnknownField(t *testing.T) {
	_, err := loadFile(t, "invalid_unknown_field_toplevel")
	logErr(t, err)
	if err == nil {
		t.Fatal("トップレベルの未知キー programz がエラーにならなかった（KnownFields(true) が効いていない）")
	}
	if !strings.Contains(err.Error(), "programz") {
		t.Errorf("エラーに打ち間違えたキー名が出ていない: %v", err)
	}
}

// TestLoadFrom_ErrorMentionsName は LoadFrom が name 引数を取る理由そのもの。
// io.Reader はファイル名を知らないので、名前を出せるのは呼び出し側から受け取った場合だけ。
// 「どのファイルの何行目か」が出ないと、複数の設定を扱うようになった時点で追えなくなる。
func TestLoadFrom_ErrorMentionsName(t *testing.T) {
	const name = "taskmasterd.yaml"
	_, err := config.LoadFrom(strings.NewReader("programs:\n  p:\n    cmd: [\n"), name)
	logErr(t, err)
	if err == nil {
		t.Fatal("YAML 構文エラーがエラーにならなかった")
	}
	if !strings.Contains(err.Error(), name) {
		t.Errorf("エラーに設定ファイル名が含まれていない: %v", err)
	}
}

func TestLoadFrom(t *testing.T) {
	tests := []struct {
		name     string
		yamlFile string
		wantErr  bool
		check    func(t *testing.T, c *config.Config)
	}{
		{
			name:     "最小の設定が読める",
			yamlFile: "valid_minimal",
			check: func(t *testing.T, c *config.Config) {
				if len(c.Programs) != 1 {
					t.Fatalf("programs = %d 件, want 1", len(c.Programs))
				}
				if c.Programs["minimal"].Cmd[0] != "/bin/true" {
					t.Errorf("cmd = %q", c.Programs["minimal"].Cmd)
				}
			},
		},
		{
			// 狙い: 先頭の --- は単一ドキュメントのまま。
			// 複数ドキュメント検出を「--- の有無」で書くと、この正常なファイルを誤って弾く。
			name:     "先頭の --- は 2 つ目のドキュメントを作らない",
			yamlFile: "edge_leading_document_separator",
			check: func(t *testing.T, c *config.Config) {
				if len(c.Programs) != 1 {
					t.Fatalf("programs = %d 件, want 1", len(c.Programs))
				}
				if c.Programs["p"].Cmd[0] != "/bin/true" {
					t.Errorf("cmd = %q", c.Programs["p"].Cmd)
				}
			},
		},
		{
			// 狙い: 2 件目・3 件目にもデフォルトが効くこと。
			// Program.UnmarshalYAML は program ごとに呼ばれるので当然に見えるが、
			// 「1 件目だけ初期化して使い回す」実装に変えたときに落ちるのはここ。
			name:     "複数 program がそれぞれ独立にデフォルト適用される",
			yamlFile: "valid_multiple_programs",
			check: func(t *testing.T, c *config.Config) {
				if len(c.Programs) != 3 {
					t.Fatalf("programs = %d 件, want 3", len(c.Programs))
				}
				alpha, bravo, charlie := c.Programs["alpha"], c.Programs["bravo"], c.Programs["charlie"]

				// alpha は cmd だけ。全項目がデフォルト。
				if alpha.Numprocs != 1 || !alpha.Autostart || alpha.Autorestart != config.Unexpected ||
					alpha.Starttretries != 3 || alpha.Starttime != 1 || alpha.Stoptime != 10 {
					t.Errorf("alpha にデフォルトが入っていない: %#v", alpha)
				}
				// bravo は numprocs だけ上書き。残りはデフォルトのまま。
				if bravo.Numprocs != 2 {
					t.Errorf("bravo.Numprocs = %d, want 2", bravo.Numprocs)
				}
				if bravo.Starttretries != 3 || bravo.Stoptime != 10 {
					t.Errorf("bravo の未指定項目にデフォルトが入っていない: %#v", bravo)
				}
				// charlie は autorestart だけ上書き。
				if charlie.Autorestart != config.Never {
					t.Errorf("charlie.Autorestart = %v, want Never", charlie.Autorestart)
				}
				if charlie.Numprocs != 1 {
					t.Errorf("charlie.Numprocs = %d, want 1", charlie.Numprocs)
				}
			},
		},
		{
			// 狙い: supervisord の INI から移ってきた人がやりがちな書き方。
			// programs をリストで書くとマッピングとして読めない。
			name:     "programs がリストならエラー",
			yamlFile: "invalid_programs_not_mapping",
			wantErr:  true,
		},
		{
			// 狙い: program 内の未知キー検出（programFields）は型の中にあるので
			// 入口を変えても効き続けること。差し替えで壊していないことの確認。
			name:     "program 内の未知キーはエラー",
			yamlFile: "edge_unknown_field_in_program",
			wantErr:  true,
		},
		{
			name:     "YAML 構文エラー",
			yamlFile: "invalid_syntax",
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := loadFile(t, tt.yamlFile)
			logErr(t, err)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if got == nil {
				t.Fatal("err == nil なのに Config が nil")
			}
			tt.check(t, got)
		})
	}
}

// TestLoad_IsLoadFrom は Load が「open して LoadFrom に渡すだけ」であることを固定する。
// ここが崩れると（Load 側だけに検証や展開が足されると）入口を 1 本にした意味が消え、
// テストが通る経路と本番の経路が再び分かれる。
func TestLoad_IsLoadFrom(t *testing.T) {
	p := testdataPath(t, "valid_multiple_programs")

	viaLoad, err := config.Load(p)
	if err != nil {
		logErr(t, err)
		t.Fatalf("Load: %v", err)
	}

	f, err := os.Open(p)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	viaLoadFrom, err := config.LoadFrom(f, p)
	if err != nil {
		logErr(t, err)
		t.Fatalf("LoadFrom: %v", err)
	}

	if !reflect.DeepEqual(viaLoad, viaLoadFrom) {
		t.Errorf("Load と LoadFrom の結果が違う:\n Load     = %#v\n LoadFrom = %#v", viaLoad, viaLoadFrom)
	}
}

// TestLoad_MissingFile は open の失敗が呼び出し側に届くこと。
// 「設定ファイルが無い」は採点者が最初にやる操作のひとつ。
func TestLoad_MissingFile(t *testing.T) {
	const p = "testdata/no_such_file.yaml"
	_, err := config.Load(p)
	logErr(t, err)
	if err == nil {
		t.Fatal("存在しないパスがエラーにならなかった")
	}
	if !strings.Contains(err.Error(), p) {
		t.Errorf("エラーにパスが含まれていない: %v", err)
	}
}

// --- ドキュメントの個数 ------------------------------------------------------
//
// Decoder は「YAML ドキュメントが何個あるか」を 2 つの形でしか外に出さない。
//
//	0 個      -> 1 回目の Decode が io.EOF
//	2 個以上  -> 2 回目の Decode が io.EOF 以外を返す
//
// どちらも放っておくと利用者に伝わらない。前者は `error: EOF` という
// 実装都合の 1 行になり、後者は**黙って先頭だけ読まれる**
// （起動するのに設定が効かない、という最悪の壊れ方）。
//
// さらに「ドキュメントは 1 個あるが中身が null」（--- だけ / null）は
// どちらの網にも掛からない。利用者から見れば空ファイルと同じなので、同じエラーに落とす。

// TestLoadFrom_EmptyConfig は io.EOF の意訳。
// 文面は固定しない。契約は「io.EOF という Decoder の実装都合を利用者に見せないこと」だけ。
func TestLoadFrom_EmptyConfig(t *testing.T) {
	files := []string{
		"edge_empty_file",              // 0 バイト
		"edge_comment_only",            // コメントだけ
		"edge_only_document_separator", // --- だけ（root が null のドキュメント 1 個）
		"edge_null_document",           // null と書いたファイル（同上）
	}
	for _, name := range files {
		t.Run(name, func(t *testing.T) {
			_, err := loadFile(t, name)
			logErr(t, err)
			if err == nil {
				t.Fatal("空の設定がエラーにならなかった")
			}
			if strings.Contains(err.Error(), "EOF") {
				t.Errorf("EOF が利用者に漏れている（意訳されていない）: %v", err)
			}
			if !strings.Contains(err.Error(), name) {
				t.Errorf("エラーに設定ファイル名が含まれていない: %v", err)
			}
		})
	}
}

// TestLoadFrom_MultipleDocuments は「2 つ目以降を黙って捨てない」こと。
// 黙って捨てた場合にそれが分かるよう、成功してしまったときは
// どのドキュメントが勝ったかを出す。
func TestLoadFrom_MultipleDocuments(t *testing.T) {
	tests := []struct {
		name     string
		yamlFile string
		wantErr  bool
	}{
		{
			name:     "--- で区切られた 2 つ目を黙って捨てない",
			yamlFile: "invalid_multiple_documents",
			wantErr:  true,
		},
		{
			// 末尾の --- は「空の 2 つ目」になる。
			// 案 A（ドキュメントは 1 つだけ）ならエラー、案 B（空なら許す）なら成功。
			// いまは A を前提に書いている。B を採るならここを wantErr: false にし、
			// Programs に p が 1 件あることを確かめる形へ変える。
			name:     "末尾の --- も 2 つ目のドキュメントとして数える（案 A）",
			yamlFile: "edge_trailing_document_separator",
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := loadFile(t, tt.yamlFile)
			logErr(t, err)
			if (err != nil) != tt.wantErr {
				if err == nil {
					names := make([]string, 0, len(got.Programs))
					for k := range got.Programs {
						names = append(names, k)
					}
					sort.Strings(names)
					t.Fatalf("エラーにならず読めてしまった。採用された program = %v", names)
				}
				t.Fatalf("err = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}
