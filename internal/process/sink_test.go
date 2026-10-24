package process

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// newTestSink は temp ディレクトリのファイルを本番と同じフラグで開き、Sink で包む。
// 本番で開くのは controller なので、フラグはここに書き写している。
// Close はテスト側で呼ぶことがあるので、後始末では 2 回目の ErrClosed を許す。
func newTestSink(t *testing.T) (*Sink, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "out.log")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("出力先を開けない: %v", err)
	}
	s, err := NewSink(f)
	if err != nil {
		t.Fatalf("NewSink: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			t.Errorf("Close: %v", err)
		}
	})
	return s, path
}

func fileSize(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("読めない: %v", err)
	}
	return len(b)
}

// waitSize は path が want バイト以上になるまで待ち、かかった時間を返す。
// limit を過ぎたら Fatal。
func waitSize(t *testing.T, path string, want int, limit time.Duration) time.Duration {
	t.Helper()
	start := time.Now()
	for {
		if fileSize(t, path) >= want {
			return time.Since(start)
		}
		if time.Since(start) > limit {
			t.Fatalf("%v 待っても %d B に届かない（今 %d B）", limit, want, fileSize(t, path))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestNewSink_Nil は nil の *os.File を弾くことを見る。
// Sink は f == nil を「閉じた」の印に使うので、nil を通すと何も書かない Sink が黙ってできる。
func TestNewSink_Nil(t *testing.T) {
	s, err := NewSink(nil)
	if err == nil {
		t.Fatal("err = nil, want エラー")
	}
	if s != nil {
		t.Errorf("s = %v, want nil", s)
	}
}

// TestSink_BuffersThenFlushes は、書いた直後はファイルに出ず、タイマーで出ることを見る。
// 直後に出ていたら行ごとに write(2) している（9 倍遅い案に戻っている）。
func TestSink_BuffersThenFlushes(t *testing.T) {
	s, path := newTestSink(t)
	line := []byte("hello\n")

	s.Write(line)
	if got := fileSize(t, path); got != 0 {
		t.Fatalf("書いた直後に %d B 出ている。バッファに積まずに書いている", got)
	}

	took := waitSize(t, path, len(line), time.Second)
	t.Logf("ファイルに出るまで %v", took)
}

// TestSink_FlushesWhenBufferFull は、64 KiB を超えたらタイマーを待たずに出ることを見る。
func TestSink_FlushesWhenBufferFull(t *testing.T) {
	s, path := newTestSink(t)
	line := append(bytes.Repeat([]byte("x"), 1023), '\n')

	for range 65 { // 65 KiB
		s.Write(line)
	}
	if got := fileSize(t, path); got < 64*1024 {
		t.Fatalf("65 KiB 書いた直後に %d B しか出ていない。満杯でも流れていない", got)
	}
}

// TestSink_LatencyBoundWhileWriting は、200ms より短い間隔で書き続けても
// どの行も一定時間内にファイルに出ることを見る。
//
// 200ms を「最後の書き込みから」数える（書くたびにタイマーを延ばす）と、
// 100ms 間隔の書き込みでは 64 KiB 溜まるまで一度も流れず、ここで落ちる。
// 最初の 1 周期だけ見ると「1 回目の発火のあと印を立て忘れて延ばす側に戻る」実装を通してしまうので、
// 1.5 秒（7 周期ぶん）書き続けて、毎回すべての行を確かめる。
// 上限を 400ms（タイマーの 2 倍）にしているのは -race と負荷で揺れる分の余裕。
// 延ばす実装はそもそも流れないので、余裕を取っても見分けは付く。
func TestSink_LatencyBoundWhileWriting(t *testing.T) {
	const (
		interval = 100 * time.Millisecond
		bound    = 400 * time.Millisecond
		writes   = 15
	)
	s, path := newTestSink(t)
	line := []byte("2026-10-24T18:00:00+0900 [app:1234] tick\n")

	var written []time.Time
	for i := range writes {
		s.Write(line)
		written = append(written, time.Now())
		time.Sleep(interval)

		due := 0
		for _, at := range written {
			if time.Since(at) > bound {
				due++
			}
		}
		if got := fileSize(t, path); got < due*len(line) {
			t.Fatalf("%d 回目のあと: %v より前に書いた %d 行（%d B）が出ているはずが、ファイルは %d B",
				i+1, bound, due, due*len(line), got)
		}
	}
}

// TestSink_CloseFlushes は、Close がタイマーを待たずにバッファを流すことを見る。
// 終了時はここが最後の書き出しになる。
func TestSink_CloseFlushes(t *testing.T) {
	s, path := newTestSink(t)
	line := []byte("last words\n")

	s.Write(line)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := fileSize(t, path); got != len(line) {
		t.Fatalf("Close 直後のファイルが %d B, want %d B", got, len(line))
	}
}

// TestSink_WriteAfterClose は、Close の後に Write が来ても何も起きないことを見る。
// 孫が書き端を握っていると pump は Close の後も生きていて、Write を呼んでくる。
// タイマーが鳴る時間も待ち、閉じたあとに仕掛け直されていないことまで見る。
func TestSink_WriteAfterClose(t *testing.T) {
	s, path := newTestSink(t)
	before := []byte("before\n")

	s.Write(before)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s.Write([]byte("after\n"))
	time.Sleep(300 * time.Millisecond)

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("読めない: %v", err)
	}
	if !bytes.Equal(b, before) {
		t.Fatalf("ファイル = %q, want %q", b, before)
	}
	// ファイルだけ見ると、閉じた後に積んでも flush 側が止めるので差が出ない。
	// 積まれた分は二度と流れず、64 KiB を超えると閉じた fd に write(2) しに行くので、中も見る。
	if n := s.bw.Buffered(); n != 0 {
		t.Errorf("閉じた後の Write が %d B をバッファに積んでいる", n)
	}

	if err := s.Close(); !errors.Is(err, os.ErrClosed) {
		t.Errorf("2 回目の Close = %v, want os.ErrClosed", err)
	}
}

// TestSink_ConcurrentWritesKeepLines は、複数の pump が同じ Sink を叩いても行が混ざらないことを見る。
// numprocs 分の pump が 1 枚の Sink を共有するのが本番の形。-race で走らせる前提。
func TestSink_ConcurrentWritesKeepLines(t *testing.T) {
	const (
		writers = 8
		lines   = 2000
	)
	s, path := newTestSink(t)

	var wg sync.WaitGroup
	for w := range writers {
		wg.Go(func() {
			for i := range lines {
				s.Write(fmt.Appendf(nil, "w=%d i=%05d %s\n", w, i, bytes.Repeat([]byte("x"), 80)))
			}
		})
	}
	wg.Wait()
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("読めない: %v", err)
	}
	got := bytes.Split(bytes.TrimSuffix(b, []byte{'\n'}), []byte{'\n'})
	if len(got) != writers*lines {
		t.Fatalf("%d 行, want %d 行", len(got), writers*lines)
	}
	seen := make(map[string]bool, writers*lines)
	for n, ln := range got {
		var w, i int
		var pad string
		if _, err := fmt.Sscanf(string(ln), "w=%d i=%05d %s", &w, &i, &pad); err != nil || len(pad) != 80 {
			t.Fatalf("%d 行目が壊れている: %q", n+1, ln)
		}
		seen[string(ln)] = true
	}
	if len(seen) != writers*lines {
		t.Errorf("重複がある: 異なる行は %d, want %d", len(seen), writers*lines)
	}
}

// TestSink_CloseWhileWriting は、Write とタイマーの flush の最中に Close が割り込んでも
// panic せず、-race も報告しないことを見る。終了時に pump がまだ書いている状況。
func TestSink_CloseWhileWriting(t *testing.T) {
	s, _ := newTestSink(t)
	line := []byte("racing\n")

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 2000 {
				s.Write(line)
			}
		})
	}
	time.Sleep(5 * time.Millisecond)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	wg.Wait()
}
