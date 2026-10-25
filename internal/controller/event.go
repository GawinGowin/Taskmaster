package controller

import (
	"os"
	"syscall"
)

type event interface{ isEvent() }

type evExited struct {
	id  string
	gen uint64
	ps  *os.ProcessState
	err error // wait 自体が失敗したとき
}
type evStartTimeElapsed struct {
	id  string
	gen uint64
}
type evBackoffElapsed struct {
	id  string
	gen uint64
}
type evStopTimeout struct {
	id  string
	gen uint64
}
type evSignal struct {
	sig syscall.Signal
}

type evRequest struct {
	cmd     Command
	targets []string
	reply   chan<- []string
}

type Command int

const (
	CmdStart Command = iota
	CmdStop
	CmdStopall
	CmdRestart
	CmdStatus
	CmdReload
	CmdQuit
	CmdHelp
)

func (evExited) isEvent()           {}
func (evStartTimeElapsed) isEvent() {}
func (evStopTimeout) isEvent()      {}
func (evBackoffElapsed) isEvent()   {}
func (evSignal) isEvent()           {}
func (evRequest) isEvent()          {}
