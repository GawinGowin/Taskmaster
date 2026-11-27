package controller

import (
	"path/filepath"
	"slices"
	"testing"

	"taskmaster/internal/config"
)

// TestNew_View は、New が View を 1 つ作り、View() が毎回同じものを返すことを見る。
// Process は New で受け取った View のポインタを持ち続けるので、途中で作り直すと pump の書く先がずれる。
func TestNew_View(t *testing.T) {
	c, err := New("", &config.Config{Programs: map[string]config.Program{"a": idle(1)}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	v := c.View()
	if v == nil {
		t.Fatal("View() = nil")
	}
	if c.View() != v {
		t.Error("View() が呼ぶたびに別のものを返した")
	}
}

// TestRequest_Logs は、logs の要求に、ループが返す応答の行を見る。
//
// 対象の検証は status と同じ規則で、エラーがあればエラーの行を返す。
// 対象に出力を破棄する program が含まれていたら、program ごとに 1 行の知らせを返す。
// 出力を両方ファイルに書く program だけなら、何も返さない（シェルはそのまま追従を始める）。
// 行の順序は status と同じく、対象を打った順に展開した順（all と対象 0 個は program 名の昇順）。
func TestRequest_Logs(t *testing.T) {
	dir := t.TempDir()
	path := func(name string) string { return filepath.Join(dir, name) }
	onlyOut := program(path("o.out"), "")
	onlyOut.Numprocs = 1
	onlyErr := program("", path("e.err"))
	onlyErr.Numprocs = 1

	c := newLoop(t, map[string]config.Program{
		"web":     program(path("web.out"), path("web.err")), // numprocs 2
		"silent":  idle(1),                                   // 両方破棄
		"outonly": onlyOut,                                   // stderr だけ破棄
		"erronly": onlyErr,                                   // stdout だけ破棄
	})

	for _, tc := range []struct {
		name    string
		targets []string
		want    []string
	}{
		{"両方ファイルに書く program", []string{"web"}, nil},
		{"両方ファイルに書くプロセス", []string{"web:1"}, nil},
		{"両方破棄", []string{"silent"}, []string{"silent: stdout and stderr are discarded"}},
		{"stdout だけ破棄", []string{"erronly"}, []string{"erronly: stdout is discarded"}},
		{"stderr だけ破棄", []string{"outonly"}, []string{"outonly: stderr is discarded"}},
		{"破棄の知らせは program ごとに 1 行", []string{"silent", "silent:0"}, []string{
			"silent: stdout and stderr are discarded",
		}},
		{"知らせの順序は対象を展開した順", []string{"silent", "all"}, []string{
			"silent: stdout and stderr are discarded",
			"erronly: stdout is discarded",
			"outonly: stderr is discarded",
		}},
		{"対象が 0 個なら全部", nil, []string{
			"erronly: stdout is discarded",
			"outonly: stderr is discarded",
			"silent: stdout and stderr are discarded",
		}},
		{"無い program", []string{"web", "nosuch"}, []string{"nosuch: ERROR (no such program)"}},
		{"無い index", []string{"web:5"}, []string{"web:5: ERROR (no such process, numprocs is 2)"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := request(t, c, CmdLogs, tc.targets...)
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("logs %q = %q, want %q", tc.targets, got, tc.want)
			}
		})
	}
}

// TestRequest_LogsErrorWithDiscard は、エラーと破棄の program が混ざっているとき、エラーの行を返すことを見る。
// 破棄の知らせも一緒に返すかは決めていないので、エラーの行が含まれることだけを見る。
func TestRequest_LogsErrorWithDiscard(t *testing.T) {
	c := newLoop(t, map[string]config.Program{"silent": idle(1)})

	got := request(t, c, CmdLogs, "silent", "nosuch")
	if !slices.Contains(got, "nosuch: ERROR (no such program)") {
		t.Errorf("logs silent nosuch = %q, エラーの行が無い", got)
	}
}

// TestShell_LogsSendsRequest は、シェルが logs の行を CmdLogs の要求にしてループに送ることを見る。
// 対象の検証はループがするので、シェルは対象をそのまま渡す。
func TestShell_LogsSendsRequest(t *testing.T) {
	for _, tc := range []struct {
		line string
		want []string
	}{
		{"logs web healthy:0", []string{"web", "healthy:0"}},
		{"logs", nil},
	} {
		t.Run(tc.line, func(t *testing.T) {
			ts := newTestShell(t)
			ts.write(t, tc.line)
			req := ts.recvRequest(t)
			if req.cmd != CmdLogs {
				t.Errorf("cmd = %v, want CmdLogs(%v)", req.cmd, CmdLogs)
			}
			if !slices.Equal(req.targets, tc.want) {
				t.Errorf("targets = %q, want %q", req.targets, tc.want)
			}
			if req.reply == nil {
				t.Fatal("reply が nil。ループは応答を返せない")
			}
			// エラーの行を返すと、シェルは追従を始めずに次の行を読む。
			req.reply <- []string{"nosuch: ERROR (no such program)"}
			ts.write(t, "status")
			if req := ts.recvRequest(t); req.cmd != CmdStatus {
				t.Errorf("エラーの応答のあとの要求の cmd = %v, want CmdStatus(%v)", req.cmd, CmdStatus)
			}
		})
	}
}
