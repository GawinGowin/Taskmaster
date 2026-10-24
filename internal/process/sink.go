package process

import (
	"bufio"
	"errors"
	"os"
	"sync"
	"time"
)

type Sink struct {
	mu      sync.Mutex
	f       *os.File
	bw      *bufio.Writer
	timer   *time.Timer
	pending bool
}

func NewSink(f *os.File) (*Sink, error) {
	if f == nil {
		return nil,  errors.New("file must not be nil")
	}
	return &Sink{f: f, bw: bufio.NewWriterSize(f, 64*1024)}, nil
}

// Write は pump から呼ばれる。複数の pump が同じ Sink を叩くので mutex で守る。
// ここで守るのは Sink だけで、proc の状態には一切触らない。
func (s *Sink) Write(p []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f == nil {
		return
	}

	_, _ = s.bw.Write(p)

	// 書き出すバッファが到着してから 200ms で flush する
	if s.timer == nil {
		s.timer = time.AfterFunc(200*time.Millisecond, s.flush)
		s.pending = true
	}
	if !s.pending {
		s.timer.Reset(200 * time.Millisecond)
		s.pending = true
	}
}

func (s *Sink) flush() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f == nil {
		return
	}

	_ = s.bw.Flush()
	s.pending = false
}

func (s *Sink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f == nil {
		return os.ErrClosed
	}
	
	if s.timer != nil {
		s.timer.Stop()
	}
	_ = s.bw.Flush()
	err := s.f.Close()
	s.f = nil
	return err
}
