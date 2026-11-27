package process

import (
	"bytes"
	"strconv"
	"strings"
	"testing"
)

// TestFormatLine は、ビュー層の 1 行の形と、プレフィックスの幅の決まり方を見る。
//
// 入力の line は pump が ReadSlice から受け取った 1 塊で、改行で終わるか、
// 16 KiB で切れたか EOF で終わった塊（改行で終わらない）のどちらか。
// 改行で終わらない塊には、行が続いている印として " ↵" を付ける。
//
// 幅は、それまでに出した最長のプレフィックス（name:index/pid）の長さ。
// 短いプレフィックスは空白で埋めて区切りの列をそろえ、長いものが来たら幅を広げる。縮めない。
func TestFormatLine(t *testing.T) {
	web := Source{Name: "web", Index: 0, Pid: 4101}          // "web:0/4101" は 10 文字
	workers := Source{Name: "workers", Index: 3, Pid: 41050} // "workers:3/41050" は 15 文字

	full := strings.Repeat("x", maxLine) // 16 KiB で切れた塊（ErrBufferFull）
	fits := strings.Repeat("y", maxLine-1) + "\n"

	for _, tc := range []struct {
		name      string
		src       Source
		line      string
		width     int
		want      string
		wantWidth int
	}{
		{
			name: "stdout の普通の行", src: web, line: "ready\n", width: 0,
			want: "web:0/4101 | ready\n", wantWidth: 10,
		},
		{
			name: "stderr は ! で区切る", src: Source{Name: "web", Index: 0, Pid: 4101, Stderr: true},
			line: "retrying\n", width: 0,
			want: "web:0/4101 ! retrying\n", wantWidth: 10,
		},
		{
			name: "幅より短いプレフィックスは空白で埋める", src: web, line: "tick=1\n", width: 15,
			want: "web:0/4101      | tick=1\n", wantWidth: 15,
		},
		{
			name: "幅より長いプレフィックスで幅が広がる", src: workers, line: "start\n", width: 10,
			want: "workers:3/41050 | start\n", wantWidth: 15,
		},
		{
			name: "pid の桁が増えても幅が広がる", src: Source{Name: "web", Index: 0, Pid: 10000}, line: "a\n", width: 10,
			want: "web:0/10000 | a\n", wantWidth: 11,
		},
		{
			name: "空行", src: web, line: "\n", width: 0,
			want: "web:0/4101 | \n", wantWidth: 10,
		},
		{
			name: "改行で終わらない最後の断片に ↵ を付ける", src: web, line: "no newline", width: 0,
			want: "web:0/4101 | no newline ↵\n", wantWidth: 10,
		},
		{
			name: "16 KiB で切れた塊に ↵ を付ける", src: web, line: full, width: 0,
			want: "web:0/4101 | " + full + " ↵\n", wantWidth: 10,
		},
		{
			name: "改行込みでちょうど 16 KiB の行には付けない", src: web, line: fits, width: 0,
			want: "web:0/4101 | " + fits, wantWidth: 10,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, gotWidth := formatLine(tc.src, []byte(tc.line), tc.width)
			if string(got) != tc.want {
				t.Errorf("got %q, want %q", abbrev(string(got)), abbrev(tc.want))
			}
			if gotWidth != tc.wantWidth {
				t.Errorf("width = %d, want %d", gotWidth, tc.wantWidth)
			}
		})
	}
}

// TestFormatLine_DoesNotTouchInput は、formatLine が受け取った line を書き換えないことを見る。
// line は呼び出し側（channel から受け取ったコピー）の持ち物で、末尾の改行を落とすときに
// line[:len(line)-1] に append すると、元の配列の改行の位置を上書きする。
func TestFormatLine_DoesNotTouchInput(t *testing.T) {
	line := []byte("ready\nnext\n")
	head := line[:6] // "ready\n" の後ろに容量が残っているスライス
	formatLine(Source{Name: "a", Index: 0, Pid: 1}, head, 0)
	if !bytes.Equal(line, []byte("ready\nnext\n")) {
		t.Errorf("入力が書き換わった: %q", line)
	}
}

// abbrev は、16 KiB の行を失敗メッセージに出せる長さに縮める。
func abbrev(s string) string {
	if len(s) <= 80 {
		return s
	}
	return s[:40] + "…(" + strconv.Itoa(len(s)) + " B)…" + s[len(s)-30:]
}
