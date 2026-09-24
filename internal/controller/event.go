package controller

import "os"

type evExited struct {
	id  string
	gen uint64
	ps  *os.ProcessState
}
type evStartTimeElapsed struct {
	id  string
	gen uint64
}
type evStopTimeout struct{}
type evBackoffElapsed struct{}
