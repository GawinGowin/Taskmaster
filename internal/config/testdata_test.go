package config_test

import (
	"path/filepath"
	"testing"
)

const testdataDir = "testdata"

// testdataPath は testdata/<name>.yaml のパスを返しつつ、絶対パスを出力に出す。
//
// VSCode の Test Results パネルはターミナル実体なので、そこに流れた
// 「絶対パス:行:桁」はターミナルのリンク検出で Ctrl+click できる。
// 相対パスにしないのは、このターミナルには cwd が無く、
// testdata/foo.yaml がワークスペース全体の検索にフォールバックしてしまうため。
//
// go test の cwd はパッケージディレクトリなので filepath.Abs がそのまま効く。
// 戻り値は相対パスのまま返す。LoadFrom の name 引数はエラー本文に出るので、
// ここを絶対パスにすると「どのファイルの何行目か」の表示が環境依存になる。
func testdataPath(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join(testdataDir, name+".yaml")
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = p
	}
	t.Logf("testdata: %s:1:1", abs)
	return p
}
