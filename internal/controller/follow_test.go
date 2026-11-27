package controller

import (
	"testing"

	"github.com/chzyer/readline"

	"taskmaster/internal/process"
)

// TestFollowKey は、logs の追従中に readline の FuncFilterInputRune に渡す関数が、
// 押されたキーをどう選り分けるかを見る。
//
// 追従をやめるキー（q、Ctrl-C、Esc）は Enter に置き換えて Readline() を戻らせる。
// それ以外は false を返して捨てる。Ctrl-D を通すと Readline() が io.EOF を返し、シェルが quit を送ってしまう。
// Enter を通すと、空の行で Readline() が戻り、追従をやめるキーが増える。
// 矢印キーは、readline の端末の層で CharPrev などに置き換わってからフィルタに届く。
func TestFollowKey(t *testing.T) {
	for _, tc := range []struct {
		name    string
		in      rune
		process bool
	}{
		{"q", 'q', true},
		{"Ctrl-C", readline.CharInterrupt, true},
		{"Esc", readline.CharEsc, true},
		{"Ctrl-D", readline.CharDelete, false},
		{"Enter", readline.CharEnter, false},
		{"大文字の Q", 'Q', false},
		{"ほかの文字", 'a', false},
		{"空白", ' ', false},
		{"上矢印（Ctrl-P と同じ）", readline.CharPrev, false},
		{"Tab", readline.CharTab, false},
		{"Alt-b", readline.MetaBackward, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, process := followKey(tc.in)
			if process != tc.process {
				t.Fatalf("followKey(%d) の process = %v, want %v", tc.in, process, tc.process)
			}
			if process && got != readline.CharEnter {
				t.Errorf("followKey(%d) = %d, want CharEnter(%d)", tc.in, got, readline.CharEnter)
			}
		})
	}
}

// TestMatchTargets は、logs の対象を、View に登録する照合の関数に変える規則を見る。
// 対象の書き方は status と同じで、ループが検証したあとの値だけが来る。
//
// 対象は ID の一覧ではなく、name と index の照合で持つ。
// web の追従中に reload で numprocs が増えても、増えた web:4 の行が出るようにするため。
func TestMatchTargets(t *testing.T) {
	src := func(name string, index int) process.Source {
		return process.Source{Name: name, Index: index, Pid: 100 + index}
	}

	for _, tc := range []struct {
		name    string
		targets []string
		src     process.Source
		want    bool
	}{
		{"対象が 0 個なら全部", nil, src("web", 0), true},
		{"all は全部", []string{"all"}, src("db", 3), true},
		{"name は全 index", []string{"web"}, src("web", 1), true},
		{"name は reload で増えた index も", []string{"web"}, src("web", 4), true},
		{"name は別の program に合わない", []string{"web"}, src("db", 0), false},
		{"name は前方一致しない", []string{"web"}, src("webx", 0), false},
		{"name は後ろが同じでも合わない", []string{"web"}, src("myweb", 0), false},
		{"name:index は合う index だけ", []string{"web:1"}, src("web", 1), true},
		{"name:index は別の index に合わない", []string{"web:1"}, src("web", 0), false},
		{"name:index は 10 と 1 を区別する", []string{"web:1"}, src("web", 10), false},
		{"複数の対象はどれかに合えばよい", []string{"db", "web:2"}, src("web", 2), true},
		{"複数の対象のどれにも合わない", []string{"db", "web:2"}, src("web", 0), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := matchTargets(tc.targets)(tc.src); got != tc.want {
				t.Errorf("matchTargets(%q)(%s:%d) = %v, want %v", tc.targets, tc.src.Name, tc.src.Index, got, tc.want)
			}
		})
	}
}
