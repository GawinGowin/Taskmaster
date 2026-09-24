package controller

import "os"

type event interface{ isEvent() }

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

func (evExited) isEvent()           {}
func (evStartTimeElapsed) isEvent() {}
func (evStopTimeout) isEvent()      {}
func (evBackoffElapsed) isEvent()   {}
