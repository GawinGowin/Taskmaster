package controller

import (
	"io"
	"strings"

	"github.com/chzyer/readline"
)

type Shell struct {
	rl     *readline.Instance
	done   <-chan struct{}
	events chan<- event
	exited chan struct{}
}

func NewShell(events chan<- event, done <-chan struct{}, names func() []string) (*Shell, error) {
	var s Shell

	rl, err := readline.NewEx(&readline.Config{
		Prompt:          "taskmaster> ",
		AutoComplete:    newCompleter(names),
		InterruptPrompt: "^C",
		EOFPrompt:       "quit",
	})
	if err != nil {
		return nil, err
	}
	s.rl = rl

	s.done = done
	s.exited = make(chan struct{})
	s.events = events
	return &s, nil
}

func newCompleter(names func() []string) *readline.PrefixCompleter {
	programNames := func(line string) []string {
		return names()
	}

	return readline.NewPrefixCompleter(
		readline.PcItem("start", readline.PcItemDynamic(programNames)),
		readline.PcItem("stop", readline.PcItemDynamic(programNames)),
		readline.PcItem("stopall"),
		readline.PcItem("restart", readline.PcItemDynamic(programNames)),
		readline.PcItem("status", readline.PcItemDynamic(programNames)),
		readline.PcItem("reload"),
		readline.PcItem("quit"),
		readline.PcItem("help"),
	)
}

func (s *Shell) Exited() <-chan struct{} { return s.exited }

func (s *Shell) Run() error {
	defer close(s.exited)
	defer s.rl.Close()

	go func() { // controller.Run 側の終了監視
		<-s.done
		s.rl.Close()
	}()

	for {
		line, err := s.rl.Readline()
		switch {
		case err == readline.ErrInterrupt: // Ctrl-C
			continue
		case err == io.EOF: // Ctrl-D
			return nil // todo
		case err != nil:
			return err
		}
		argv := strings.Fields(line)
		cmd, target, err := parseLine(argv)
		if err != nil {
			// syntax err
			continue
		}

		var reply chan []string
		reply = make(chan []string, 1)

		s.events <- evRequest{
			cmd, target, reply,
		}
	}
}

func parseLine(argv []string) (Command, []string, error) {
	tmpTarget := argv[1:]
	return CmdHelp, tmpTarget, nil
}
