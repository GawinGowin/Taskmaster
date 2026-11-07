package controller

import (
	"io"
	"strings"
	"fmt"

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
		return append([]string{"all"}, names()...)
	}

	args := readline.PcItemDynamic(programNames)
	args.Children = []readline.PrefixCompleterInterface{args}

	return readline.NewPrefixCompleter(
		readline.PcItem("start", args),
		readline.PcItem("stop", args),
		readline.PcItem("restart", args),
		readline.PcItem("status", args),
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
			s.events <- evRequest{
				cmd:     CmdQuit,
				targets: nil,
				reply:   nil,
			}
			return nil // todo
		case err != nil:
			return err
		}
		argv := strings.Fields(line)
		if len(argv) == 0 {
			continue
		}
		cmd, targets, err := parseLine(argv)
		if err != nil {
			fmt.Fprintln(s.rl.Stderr(), err)
			continue
		}

		var reply chan []string
		reply = make(chan []string, 1)

		s.events <- evRequest{
			cmd, targets, reply,
		}
		// TODO: reply を受信して表示してから次の Readline() に進む。
		// コントローラが返事を送らずに終わると戻れなくなるので、s.done と select で待つ。
	}
}

func parseLine(argv []string) (Command, []string, error) {
	c := argv[0]
	var targets []string
	if len(argv) > 1 {
		targets = argv[1:]
	}
	cmd, ok := availableCommand[c]
	if !ok {
		return CmdHelp, nil, fmt.Errorf("%s: unknown command", c)
	}
	return cmd, targets, nil
}
