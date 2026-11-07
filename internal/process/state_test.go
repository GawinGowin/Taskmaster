package process

import (
	"slices"
	"testing"
	"time"
)

func TestTable(t *testing.T) {
	now := time.Date(2026, 11, 7, 15, 4, 5, 0, time.Local)
	sts := []Status{
		{ID: "fails:0", State: Fatal, SpawnErr: "exited too quickly (exit status 3)", Started: now.Add(-time.Minute), Stopped: now.Add(-time.Minute)},
		{ID: "hello:0", State: Running, Pid: 2116593, Started: now.Add(-4 * time.Second)},
		{ID: "manual:0", State: Stopped},
	}
	want := []string{
		"NAME       STATE     PID       UPTIME   INFO",
		"fails:0    FATAL     -         -        exited too quickly (exit status 3)",
		"hello:0    RUNNING   2116593   4s",
		"manual:0   STOPPED   -         -        not started",
	}
	got := Table(sts, now)
	if !slices.Equal(got, want) {
		t.Errorf("Table() =\n%q\nwant\n%q", got, want)
	}
}

func TestTable_Empty(t *testing.T) {
	got := Table(nil, time.Now())
	want := []string{"NAME   STATE   PID   UPTIME   INFO"}
	if !slices.Equal(got, want) {
		t.Errorf("Table(nil) = %q, want %q", got, want)
	}
}

func TestStatus_cells(t *testing.T) {
	now := time.Date(2026, 11, 7, 15, 4, 5, 0, time.Local)
	tests := []struct {
		name string
		st   Status
		want []string
	}{
		{
			name: "RUNNING は pid と uptime",
			st:   Status{ID: "app:0", State: Running, Pid: 4064, Started: now.Add(-(62*time.Second + 500*time.Millisecond))},
			want: []string{"app:0", "RUNNING", "4064", "62s", ""},
		},
		{
			name: "STARTING でも子は生きているので pid と uptime を出す",
			st:   Status{ID: "app:0", State: Starting, Pid: 10, Started: now.Add(-time.Second)},
			want: []string{"app:0", "STARTING", "10", "1s", ""},
		},
		{
			name: "時計が戻されて Started が未来",
			st:   Status{ID: "app:0", State: Running, Pid: 1, Started: now.Add(time.Minute)},
			want: []string{"app:0", "RUNNING", "1", "0s", ""},
		},
		{
			name: "EXITED は終了してからの時間。最後の pid は出さない",
			st:   Status{ID: "app:1", State: Exited, Pid: 4064, Started: now.Add(-time.Hour), Stopped: now.Add(-5 * time.Minute)},
			want: []string{"app:1", "EXITED", "-", "-", "exited 5m ago"},
		},
		{
			name: "STOPPED は止まってからの時間",
			st:   Status{ID: "app:0", State: Stopped, Started: now.Add(-time.Hour), Stopped: now.Add(-30 * time.Second)},
			want: []string{"app:0", "STOPPED", "-", "-", "stopped 30s ago"},
		},
		{
			name: "一度も起動していない STOPPED",
			st:   Status{ID: "app:0", State: Stopped},
			want: []string{"app:0", "STOPPED", "-", "-", "not started"},
		},
		{
			name: "起動に失敗し続けたまま STOPPED になり、終了した時刻が無い",
			st:   Status{ID: "app:0", State: Stopped, Started: now.Add(-time.Minute)},
			want: []string{"app:0", "STOPPED", "-", "-", ""},
		},
		{
			name: "BACKOFF は起動失敗の理由",
			st:   Status{ID: "app:0", State: Backoff, SpawnErr: "exited too quickly (exit status 1)"},
			want: []string{"app:0", "BACKOFF", "-", "-", "exited too quickly (exit status 1)"},
		},
		{
			name: "理由のない FATAL",
			st:   Status{ID: "app:0", State: Fatal},
			want: []string{"app:0", "FATAL", "-", "-", "unknown error"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.st.cells(now); !slices.Equal(got, tt.want) {
				t.Errorf("cells() = %q, want %q", got, tt.want)
			}
		})
	}
}

// 境界は k8s.io/apimachinery v0.36.0 の HumanDuration と同じ。
func TestHumanDuration(t *testing.T) {
	const (
		s   = time.Second
		m   = time.Minute
		h   = time.Hour
		day = 24 * h
		yr  = 365 * day
	)
	tests := []struct {
		d    time.Duration
		want string
	}{
		{-5 * s, "0s"},
		{0, "0s"},
		{999 * time.Millisecond, "0s"},
		{119 * s, "119s"},
		{120 * s, "2m"},
		{3*m + 20*s, "3m20s"},
		{9*m + 59*s, "9m59s"},
		{10 * m, "10m"},
		{179 * m, "179m"},
		{3 * h, "3h"},
		{5*h + 30*m, "5h30m"},
		{7*h + 59*m, "7h59m"},
		{8 * h, "8h"},
		{47 * h, "47h"},
		{48 * h, "2d"},
		{6*day + 15*h, "6d15h"},
		{7*day + 23*h, "7d23h"},
		{8 * day, "8d"},
		{729 * day, "729d"},
		{2 * yr, "2y"},
		{3*yr + 20*day, "3y20d"},
		{8 * yr, "8y"},
		{9*yr + 100*day, "9y"},
	}
	for _, tt := range tests {
		if got := humanDuration(tt.d); got != tt.want {
			t.Errorf("humanDuration(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}
