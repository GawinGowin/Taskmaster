package config_test

import (
	"fmt"
	"slices"
	"strings"
	"taskmaster/internal/config"
	"testing"
)

// mustLoad は YAML の文字列を LoadFrom に通す。
// Program の値を直接組み立てず、パースを通すのは、UnmarshalYAML が書き方の違いをそろえる処理まで含めて Diff を確かめるため。
func mustLoad(t *testing.T, src string) *config.Config {
	t.Helper()
	cfg, err := config.LoadFrom(strings.NewReader(src), "test.yaml")
	if err != nil {
		t.Fatalf("LoadFrom: %v\n%s", err, src)
	}
	return cfg
}

func TestDiff(t *testing.T) {
	tests := []struct {
		name string
		old  string
		new  string
		want []config.Change
	}{
		{
			name: "すべて同じなら unchanged",
			old:  "programs:\n  a:\n    cmd: /bin/true\n    numprocs: 2\n",
			new:  "programs:\n  a:\n    cmd: /bin/true\n    numprocs: 2\n",
			want: []config.Change{{Name: "a", Kind: config.Unchanged, OldNumprocs: 2, NewNumprocs: 2}},
		},
		{
			name: "新しい設定にだけある program は added で、0 から増える",
			old:  "programs:\n  a:\n    cmd: /bin/true\n",
			new:  "programs:\n  a:\n    cmd: /bin/true\n  b:\n    cmd: /bin/true\n    numprocs: 3\n",
			want: []config.Change{
				{Name: "a", Kind: config.Unchanged, OldNumprocs: 1, NewNumprocs: 1},
				{Name: "b", Kind: config.Added, OldNumprocs: 0, NewNumprocs: 3},
			},
		},
		{
			name: "今の設定にだけある program は removed で、0 まで減る",
			old:  "programs:\n  a:\n    cmd: /bin/true\n  b:\n    cmd: /bin/true\n    numprocs: 3\n",
			new:  "programs:\n  a:\n    cmd: /bin/true\n",
			want: []config.Change{
				{Name: "a", Kind: config.Unchanged, OldNumprocs: 1, NewNumprocs: 1},
				{Name: "b", Kind: config.Removed, OldNumprocs: 3, NewNumprocs: 0},
			},
		},
		{
			name: "numprocs だけが違えば resized",
			old:  "programs:\n  a:\n    cmd: /bin/true\n    numprocs: 2\n",
			new:  "programs:\n  a:\n    cmd: /bin/true\n    numprocs: 4\n",
			want: []config.Change{{Name: "a", Kind: config.Resized, OldNumprocs: 2, NewNumprocs: 4}},
		},
		{
			name: "numprocs を減らしても resized",
			old:  "programs:\n  a:\n    cmd: /bin/true\n    numprocs: 4\n",
			new:  "programs:\n  a:\n    cmd: /bin/true\n    numprocs: 2\n",
			want: []config.Change{{Name: "a", Kind: config.Resized, OldNumprocs: 4, NewNumprocs: 2}},
		},
		{
			name: "numprocs 以外が違えば changed",
			old:  "programs:\n  a:\n    cmd: /bin/true\n",
			new:  "programs:\n  a:\n    cmd: /bin/false\n",
			want: []config.Change{{Name: "a", Kind: config.Changed, OldNumprocs: 1, NewNumprocs: 1}},
		},
		{
			// 適用する側は、減った index を止めるのに前後の numprocs を使う
			name: "numprocs とほかの項目が両方違えば changed で、前後の numprocs を持つ",
			old:  "programs:\n  a:\n    cmd: /bin/true\n    numprocs: 4\n",
			new:  "programs:\n  a:\n    cmd: /bin/false\n    numprocs: 2\n",
			want: []config.Change{{Name: "a", Kind: config.Changed, OldNumprocs: 4, NewNumprocs: 2}},
		},
		{
			name: "pointer の umask は指す値で比べる",
			old:  "programs:\n  a:\n    cmd: /bin/true\n    umask: 022\n",
			new:  "programs:\n  a:\n    cmd: /bin/true\n    umask: 022\n",
			want: []config.Change{{Name: "a", Kind: config.Unchanged, OldNumprocs: 1, NewNumprocs: 1}},
		},
		{
			name: "umask の値が違えば changed",
			old:  "programs:\n  a:\n    cmd: /bin/true\n    umask: 022\n",
			new:  "programs:\n  a:\n    cmd: /bin/true\n    umask: 077\n",
			want: []config.Change{{Name: "a", Kind: config.Changed, OldNumprocs: 1, NewNumprocs: 1}},
		},
		{
			name: "名前だけを変えると、旧名が removed、新名が added",
			old:  "programs:\n  old:\n    cmd: /bin/true\n",
			new:  "programs:\n  new:\n    cmd: /bin/true\n",
			want: []config.Change{
				{Name: "new", Kind: config.Added, OldNumprocs: 0, NewNumprocs: 1},
				{Name: "old", Kind: config.Removed, OldNumprocs: 1, NewNumprocs: 0},
			},
		},
		{
			name: "5 種類が混ざっても program 名の昇順に並ぶ",
			old: "programs:\n" +
				"  same:\n    cmd: /bin/true\n" +
				"  chg:\n    cmd: /bin/true\n" +
				"  rs:\n    cmd: /bin/true\n    numprocs: 2\n" +
				"  gone:\n    cmd: /bin/true\n",
			new: "programs:\n" +
				"  same:\n    cmd: /bin/true\n" +
				"  chg:\n    cmd: /bin/false\n" +
				"  rs:\n    cmd: /bin/true\n    numprocs: 4\n" +
				"  added:\n    cmd: /bin/true\n",
			want: []config.Change{
				{Name: "added", Kind: config.Added, OldNumprocs: 0, NewNumprocs: 1},
				{Name: "chg", Kind: config.Changed, OldNumprocs: 1, NewNumprocs: 1},
				{Name: "gone", Kind: config.Removed, OldNumprocs: 1, NewNumprocs: 0},
				{Name: "rs", Kind: config.Resized, OldNumprocs: 2, NewNumprocs: 4},
				{Name: "same", Kind: config.Unchanged, OldNumprocs: 1, NewNumprocs: 1},
			},
		},

		// 次の 3 つは DeepEqual が「違う」と判定しうるが意味は同じ書き方。
		// パースの時点でそろえているので、比較の側では特別扱いせずに unchanged になる。
		{
			name: "exitcodes の並びが違うだけなら unchanged",
			old:  "programs:\n  a:\n    cmd: /bin/true\n    exitcodes: [0, 2]\n",
			new:  "programs:\n  a:\n    cmd: /bin/true\n    exitcodes: [2, 0]\n",
			want: []config.Change{{Name: "a", Kind: config.Unchanged, OldNumprocs: 1, NewNumprocs: 1}},
		},
		{
			name: "env: {} と env を書かないときは unchanged",
			old:  "programs:\n  a:\n    cmd: /bin/true\n    env: {}\n",
			new:  "programs:\n  a:\n    cmd: /bin/true\n",
			want: []config.Change{{Name: "a", Kind: config.Unchanged, OldNumprocs: 1, NewNumprocs: 1}},
		},
		{
			name: "値なしの exitcodes: と exitcodes を書かないときは unchanged",
			old:  "programs:\n  a:\n    cmd: /bin/true\n    exitcodes:\n",
			new:  "programs:\n  a:\n    cmd: /bin/true\n",
			want: []config.Change{{Name: "a", Kind: config.Unchanged, OldNumprocs: 1, NewNumprocs: 1}},
		},
		{
			// [] は「どの終了も予期しない」で、既定の [0] とは意味が違う
			name: "exitcodes: [] と exitcodes を書かないときは changed",
			old:  "programs:\n  a:\n    cmd: /bin/true\n    exitcodes: []\n",
			new:  "programs:\n  a:\n    cmd: /bin/true\n",
			want: []config.Change{{Name: "a", Kind: config.Changed, OldNumprocs: 1, NewNumprocs: 1}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := config.Diff(mustLoad(t, tt.old), mustLoad(t, tt.new))
			if !slices.Equal(got, tt.want) {
				t.Errorf("Diff\n got = %+v\nwant = %+v", got, tt.want)
			}
		})
	}
}

// TestDiff_OrderIsStable は map を range で回す順序に結果が左右されないことを確かめる。
// 順序は呼ぶたびに変わりうるので、同じ組を何度も渡して毎回同じ列になるかを見る。
func TestDiff_OrderIsStable(t *testing.T) {
	var b strings.Builder
	b.WriteString("programs:\n")
	for _, n := range []string{"e", "c", "a", "d", "b", "h", "f", "g"} {
		fmt.Fprintf(&b, "  %s:\n    cmd: /bin/true\n", n)
	}
	old := mustLoad(t, b.String())
	b.WriteString("  i:\n    cmd: /bin/true\n")
	new := mustLoad(t, b.String())

	first := config.Diff(old, new)
	for i := range 50 {
		if got := config.Diff(old, new); !slices.Equal(got, first) {
			t.Fatalf("%d 回目で順序が変わった\n got = %+v\nfirst = %+v", i+2, got, first)
		}
	}
	if !slices.IsSortedFunc(first, func(a, b config.Change) int { return strings.Compare(a.Name, b.Name) }) {
		t.Errorf("program 名の昇順になっていない: %+v", first)
	}
}

// TestDiff_DoesNotModifyInput は、比較のために Numprocs をそろえたコピーが元の設定に書き戻されないことを確かめる。
// Diff のあとも、呼び出し側は今の設定と新しい設定をそのまま使う。
func TestDiff_DoesNotModifyInput(t *testing.T) {
	old := mustLoad(t, "programs:\n  a:\n    cmd: /bin/true\n    numprocs: 2\n    env: {K: v}\n")
	new := mustLoad(t, "programs:\n  a:\n    cmd: /bin/true\n    numprocs: 4\n    env: {K: v}\n")

	config.Diff(old, new)

	if n := old.Programs["a"].Numprocs; n != 2 {
		t.Errorf("old の numprocs = %d, want 2", n)
	}
	if n := new.Programs["a"].Numprocs; n != 4 {
		t.Errorf("new の numprocs = %d, want 4", n)
	}
	if v := old.Programs["a"].Env["K"]; v != "v" {
		t.Errorf("old の env[K] = %q, want %q", v, "v")
	}
}
