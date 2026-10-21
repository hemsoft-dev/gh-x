package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/muesli/termenv"
	xterm "golang.org/x/term"
)

const terminalClearSequence = "\x1b[2J\x1b[H"

var terminalOutputFunc = func(w io.Writer) bool {
	file, ok := w.(*os.File)
	return ok && xterm.IsTerminal(int(file.Fd()))
}

type terminalOptions struct {
	clear bool
	json  bool
}

func (options terminalOptions) clearOutput(w io.Writer) error {
	if !options.clear || options.json || !terminalOutputFunc(w) {
		return nil
	}
	if err := clearTerminal(w); err != nil {
		return fmt.Errorf("clear terminal: %w", err)
	}
	return nil
}

type terminalFlagScan struct {
	options terminalOptions
	command []string
	flags   *flag.FlagSet
}

// extractTerminalOptions consumes only global flags. Command definitions tell
// it which following tokens are values, including values spelled "--clear".
func extractTerminalOptions(args []string) ([]string, terminalOptions, error) {
	var scan terminalFlagScan
	var firstErr error
	cleaned := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			cleaned = append(cleaned, args[i:]...)
			break
		}
		consumed, err := scan.option(arg)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			// Keep scanning so JSON error envelopes also work when a bad
			// global option occurs before the command or its --json flag.
			continue
		}
		if consumed {
			continue
		}
		cleaned = append(cleaned, arg)
		if scan.takesValue(arg) && i+1 < len(args) {
			i++
			cleaned = append(cleaned, args[i])
		}
	}
	return cleaned, scan.options, firstErr
}

func (scan *terminalFlagScan) option(arg string) (bool, error) {
	name, value, assigned := strings.Cut(arg, "=")
	if name == "--clear" {
		enabled, err := terminalBool(value, assigned)
		if err != nil {
			return false, fmt.Errorf("invalid value for --clear: %w", err)
		}
		scan.options.clear = enabled
		return true, nil
	}
	if !looksLikeFlag(arg) {
		scan.command = append(scan.command, arg)
		scan.flags = terminalCommandFlags(scan.command)
	} else if strings.TrimLeft(name, "-") == "json" && scan.flags != nil && scan.flags.Lookup("json") != nil {
		scan.options.json, _ = terminalBool(value, assigned)
	}
	return false, nil
}

func (scan *terminalFlagScan) takesValue(arg string) bool {
	name, _, assigned := strings.Cut(arg, "=")
	return !assigned && consumesFlagValue(scan.flags, name)
}

func terminalBool(value string, assigned bool) (bool, error) {
	if !assigned {
		return true, nil
	}
	return strconv.ParseBool(value)
}

func consumesFlagValue(flags *flag.FlagSet, arg string) bool {
	if flags == nil {
		return false
	}
	option := flags.Lookup(strings.TrimLeft(arg, "-"))
	if option == nil {
		return false
	}
	boolean, ok := option.Value.(interface{ IsBoolFlag() bool })
	return !ok || !boolean.IsBoolFlag()
}

func terminalCommandKey(command []string) string {
	if len(command) == 0 {
		return ""
	}
	switch command[0] {
	case "s":
		return "status"
	case "pr", "issue", "run", "workflow", "codespace":
		name := "list"
		if len(command) > 1 {
			name = command[1]
		}
		if command[0] == "pr" && looksLikeNumber(name) {
			name = "view"
		}
		return command[0] + " " + name
	default:
		return command[0]
	}
}

func terminalCommandFlags(command []string) *flag.FlagSet {
	factories := map[string]func() *flag.FlagSet{
		"status":         func() *flag.FlagSet { return statusFlags(&statusOptions{}, io.Discard) },
		"changelog":      func() *flag.FlagSet { return changelogFlags(&changelogOptions{}, io.Discard) },
		"pr changelog":   func() *flag.FlagSet { return changelogFlags(&changelogOptions{}, io.Discard) },
		"pr list":        func() *flag.FlagSet { return listFlags(&listOptions{}, io.Discard) },
		"pr me":          func() *flag.FlagSet { return meFlags(&meOptions{}, io.Discard) },
		"pr atm":         func() *flag.FlagSet { return atmFlags(&atmOptions{}, io.Discard) },
		"pr review":      func() *flag.FlagSet { return reviewFlags(&prReviewOptions{}, io.Discard) },
		"pr view":        viewFlags,
		"issue list":     func() *flag.FlagSet { return issueListFlags(&issueListOptions{}, io.Discard) },
		"run list":       func() *flag.FlagSet { return runListFlags(&runListOptions{}, io.Discard) },
		"workflow list":  func() *flag.FlagSet { return workflowListFlags(&workflowListOptions{}, io.Discard) },
		"codespace list": func() *flag.FlagSet { return codespaceListFlags(&codespaceListOptions{}, io.Discard) },
	}
	if factory := factories[terminalCommandKey(command)]; factory != nil {
		return factory()
	}
	return nil
}

// View has a positional parser, so share its repository-option names here.
var viewRepoFlagNames = []string{"--repo", "-R"}

func viewFlags() *flag.FlagSet {
	flags := flag.NewFlagSet("view", flag.ContinueOnError)
	for _, name := range viewRepoFlagNames {
		flags.String(strings.TrimLeft(name, "-"), "", "Repository")
	}
	return flags
}

func isViewRepoFlag(arg string) bool {
	for _, name := range viewRepoFlagNames {
		if arg == name {
			return true
		}
	}
	return false
}

func clearTerminal(w io.Writer) (err error) {
	restore, err := termenv.EnableVirtualTerminalProcessing(termenv.NewOutput(w))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, restore()) }()
	_, err = io.WriteString(w, terminalClearSequence)
	return err
}
