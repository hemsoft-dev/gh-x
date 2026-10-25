package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

const consoleFixtureRole = "GH_X_TEST_CONSOLE_ROLE"

var consoleFixtureArgs = []string{"", "space value", `quote"value`, `trailing\`, "Unicode λ"}

func assertNoConsolePolicy(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.HideWindow || cmd.SysProcAttr.CreationFlags&0x08000000 == 0 {
		t.Fatal("captured command must explicitly request CREATE_NO_WINDOW and HideWindow")
	}
	if cmd.SysProcAttr.CreationFlags&(0x10|0x8) != 0 {
		t.Fatal("CREATE_NEW_CONSOLE/DETACHED_PROCESS would override CREATE_NO_WINDOW")
	}
}

func TestCapturedCommandWindowsPolicy(t *testing.T) {
	assertNoConsolePolicy(t, newCapturedCommand(context.Background(), os.Args[0]))
	assertNoConsolePolicy(t, newSSHConfigCommand(context.Background(), "fixture.invalid"))
}

// This re-executes the console-subsystem test binary as a parent with no console,
// then exercises the real status and gh transports. A hidden existing console
// would fail GetConsoleWindow()==0, so SW_HIDE alone cannot satisfy this test.
func TestCapturedCommandWindows(t *testing.T) {
	switch os.Getenv(consoleFixtureRole) {
	case "probe", "failure":
		runConsoleProbe(t)
		return
	case "parent":
		runConsoleFreeParent(t)
		return
	}
	// Fail before launching any probe if the source policy regresses.
	assertNoConsolePolicy(t, newCapturedCommand(context.Background(), os.Args[0]))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCapturedCommandWindows$")
	cmd.Env = append(os.Environ(), consoleFixtureRole+"=parent")
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000}
	cmd.WaitDelay = time.Second
	output, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "status and gh children remained console-free") {
		t.Fatalf("console-free parent qualification: %v\n%s", err, output)
	}
}

func requireNoConsole(t *testing.T) {
	t.Helper()
	window, _, _ := syscall.NewLazyDLL("kernel32.dll").NewProc("GetConsoleWindow").Call()
	if window != 0 {
		t.Fatalf("console-subsystem fixture acquired console window handle %#x", window)
	}
}

func runConsoleProbe(t *testing.T) {
	requireNoConsole(t)
	var args []string
	for i, arg := range os.Args {
		if arg == "--" {
			args = os.Args[i+1:]
			break
		}
	}
	if !reflect.DeepEqual(args, consoleFixtureArgs) {
		t.Fatalf("argument boundaries changed: %#v", args)
	}
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(os.Stdout, "console-free stdout input=%q\n", input)
	fmt.Fprintln(os.Stderr, "console-free stderr")
	if os.Getenv(consoleFixtureRole) == "failure" {
		os.Exit(23)
	}
}

func runConsoleFreeParent(t *testing.T) {
	requireNoConsole(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	saved := commandContext
	commandContext = ctx
	t.Cleanup(func() { commandContext = saved })
	t.Setenv(consoleFixtureRole, "probe")
	t.Setenv("GH_PATH", os.Args[0])
	args := append([]string{"-test.run=^TestCapturedCommandWindows$", "--"}, consoleFixtureArgs...)
	output, err := runStatusCommand(os.Args[0], args...)
	if err != nil || !strings.Contains(output, `console-free stdout input=""`) || !strings.Contains(output, "console-free stderr") {
		t.Fatalf("status captured child: %v\n%s", err, output)
	}
	stdout, stderr, err := runGHCmdRaw(ghInvocation{Context: ctx, Args: args, Stdin: []byte("fixture stdin")})
	if err != nil || !strings.Contains(stdout.String(), `input="fixture stdin"`) || !strings.Contains(stderr.String(), "console-free stderr") {
		t.Fatalf("gh captured child: %v\nstdout=%s\nstderr=%s", err, &stdout, &stderr)
	}
	t.Setenv(consoleFixtureRole, "failure")
	_, err = runStatusCommand(os.Args[0], args...)
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 23 || !strings.Contains(err.Error(), "console-free stderr") {
		t.Fatalf("status child exit/diagnostic changed: %v", err)
	}
	stdout, stderr, err = runGHCmdRaw(ghInvocation{Context: ctx, Args: args})
	if !errors.As(err, &exit) || exit.ExitCode() != 23 || !strings.Contains(stdout.String(), "console-free stdout") || !strings.Contains(stderr.String(), "console-free stderr") {
		t.Fatalf("gh child exit/streams changed: %v\nstdout=%s\nstderr=%s", err, &stdout, &stderr)
	}
	fmt.Println("status and gh children remained console-free")
}
