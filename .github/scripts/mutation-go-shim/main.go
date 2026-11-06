// Command mutation-go-shim stands in for the go command while Gremlins
// v0.6.0 tests mutants.
//
// Gremlins derives a mutant's test package from the Go package name. Every
// file in src declares package main, so it falls back to the module root,
// whose `go test` fails during setup, and it then counts exit code 1 as a
// killed mutant. The shim runs the real tests of the calling directory's
// packages (the scope Gremlins measured coverage for), classifies the
// result from `go test -json` events, and records one line per mutant so
// the gate can prove every killed and lived count came from a test run.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"
)

const (
	realGoEnv = "MUTATION_REAL_GO"
	logEnv    = "MUTATION_TEST_LOG"
	// warmEnv names a file listing the GOCACHE entries to keep. Each mutant
	// adds about 19 MB of unique build output, about 50 GB for ./src.
	warmEnv = "MUTATION_WARM_CACHE"
)

// Gremlins v0.6.0 maps exit code 1 to KILLED, 2 to NOT VIABLE and any other
// code to LIVED. An unproven run is reported as LIVED, so it can only lower
// efficacy, and the gate fails on any unproven record.
const (
	exitLived     = 0
	exitKilled    = 1
	exitNotViable = 2
	exitUnproven  = 3
)

type outcome string

const (
	outcomeKilled    outcome = "killed"
	outcomeLived     outcome = "lived"
	outcomeNotViable outcome = "not-viable"
	outcomeUnproven  outcome = "unproven"
)

func (o outcome) exitCode() int {
	switch o {
	case outcomeKilled:
		return exitKilled
	case outcomeLived:
		return exitLived
	case outcomeNotViable:
		return exitNotViable
	default:
		return exitUnproven
	}
}

type record struct {
	Outcome     outcome  `json:"outcome"`
	ElapsedMS   int64    `json:"elapsed_ms"`
	Requested   string   `json:"requested"`
	Packages    []string `json:"packages"`
	TestsRun    int      `json:"tests_run"`
	FailedTests []string `json:"failed_tests,omitempty"`
	Detail      string   `json:"detail,omitempty"`
}

type testEvent struct {
	Action      string
	Package     string
	Test        string
	Output      string
	FailedBuild string
}

type verdict struct {
	outcome     outcome
	packages    []string
	testsRun    int
	failedTests []string
	detail      string
}

func main() {
	os.Exit(run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

func run(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	realGo := getenv(realGoEnv)
	if realGo == "" {
		fmt.Fprintf(stderr, "mutation-go-shim: %s is not set\n", realGoEnv)
		return exitUnproven
	}
	if !isMutantTest(args) {
		return passthrough(realGo, args, stdout, stderr)
	}
	rec := testMutant(realGo, args)
	if err := pruneCache(getenv(warmEnv), getenv("GOCACHE")); err != nil {
		fmt.Fprintf(stderr, "mutation-go-shim: pruning GOCACHE: %v\n", err)
	}
	if err := appendRecord(getenv(logEnv), rec); err != nil {
		fmt.Fprintf(stderr, "mutation-go-shim: %v\n", err)
		return exitUnproven
	}
	return rec.Outcome.exitCode()
}

// isMutantTest recognizes the per-mutant command Gremlins v0.6.0 builds:
// go test [-tags T] -timeout D -failfast PACKAGE. Its coverage run uses
// -cover and goes to the real toolchain unchanged.
func isMutantTest(args []string) bool {
	if len(args) < 2 || args[0] != "test" || strings.HasPrefix(args[len(args)-1], "-") {
		return false
	}
	return slices.Contains(args, "-failfast") && !slices.Contains(args, "-cover")
}

// mutantTestArgs keeps Gremlins' flags, adds JSON events, turns off vet so
// only tests can kill a mutant, and replaces the package with the calling
// directory's packages.
func mutantTestArgs(args []string) []string {
	flags := args[1 : len(args)-1]
	out := make([]string, 0, len(flags)+4)
	out = append(out, "test", "-json", "-vet=off")
	out = append(out, flags...)
	return append(out, "./...")
}

func testMutant(realGo string, args []string) record {
	start := time.Now()
	stdout, stderr, exitCode, err := execute(realGo, mutantTestArgs(args))
	v := classify(stdout)
	if err != nil {
		v = verdict{outcome: outcomeUnproven, detail: "cannot run go: " + err.Error()}
	} else if contradicts(v.outcome, exitCode) {
		v.detail = fmt.Sprintf("go test exited %d after a %s classification", exitCode, v.outcome)
		v.outcome = outcomeUnproven
	}
	if v.outcome == outcomeUnproven {
		v.detail = strings.TrimSpace(v.detail + " " + tail(stderr, 400))
	}
	return record{
		Outcome:     v.outcome,
		ElapsedMS:   time.Since(start).Milliseconds(),
		Requested:   args[len(args)-1],
		Packages:    v.packages,
		TestsRun:    v.testsRun,
		FailedTests: v.failedTests,
		Detail:      v.detail,
	}
}

// contradicts reports an exit code that disagrees with the event stream.
func contradicts(o outcome, exitCode int) bool {
	switch o {
	case outcomeLived:
		return exitCode != 0
	case outcomeKilled, outcomeNotViable:
		return exitCode <= 0
	default:
		return false
	}
}

func execute(name string, args []string) (stdout, stderr []byte, exitCode int, err error) {
	var outBuf, errBuf bytes.Buffer
	cmd := exec.Command(name, args...)
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	cmd.SysProcAttr = childProcAttr()
	// Parent-death signals follow the starting OS thread, so keep it.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	runErr := cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		return outBuf.Bytes(), errBuf.Bytes(), exitErr.ExitCode(), nil
	}
	return outBuf.Bytes(), errBuf.Bytes(), 0, runErr
}

func passthrough(realGo string, args []string, stdout, stderr io.Writer) int {
	cmd := exec.Command(realGo, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	if err != nil {
		fmt.Fprintf(stderr, "mutation-go-shim: %v\n", err)
		return exitUnproven
	}
	return 0
}

// compileError matches a compiler diagnostic at a source position. A build
// that fails without one (a full disk, a killed compiler) proves nothing
// about the mutant.
var compileError = regexp.MustCompile(`\.go:\d+:\d+: `)

type eventSummary struct {
	packages       []string
	testsRun       int
	failedTests    []string
	failedPackage  bool
	runtimeFailure string
	termination    string
	buildFailed    bool
	compileError   bool
	setupFailed    bool
}

func summarize(stream []byte) eventSummary {
	var s eventSummary
	for line := range bytes.Lines(stream) {
		var e testEvent
		if json.Unmarshal(line, &e) != nil {
			continue
		}
		s.add(e)
	}
	return s
}

func (s *eventSummary) add(e testEvent) {
	switch e.Action {
	case "start":
		s.packages = append(s.packages, e.Package)
	case "run":
		s.testsRun++
	case "build-output":
		s.compileError = s.compileError || compileError.MatchString(e.Output)
	case "output":
		s.addOutput(e)
	case "fail":
		s.addFailure(e)
	}
}

func (s *eventSummary) addOutput(e testEvent) {
	s.setupFailed = s.setupFailed || (e.Test == "" && strings.Contains(e.Output, "[setup failed]"))
	// Keep indentation: testing indents quoted child-process diagnostics.
	// Those belong to a named assertion, not a death of this test process.
	output := strings.TrimRight(e.Output, "\r\n")
	for _, marker := range []string{"signal: killed", "fatal error: runtime: out of memory", "fatal error: out of memory", "runtime: out of memory", "runtime: cannot allocate memory", "panic: test timed out"} {
		if strings.HasPrefix(strings.ToLower(output), marker) {
			s.termination = tail([]byte(output), 400)
		}
	}
	if strings.HasPrefix(output, "panic:") || strings.HasPrefix(output, "fatal error:") {
		s.runtimeFailure = tail([]byte(output), 400)
	}
}

func (s *eventSummary) addFailure(e testEvent) {
	switch {
	case e.Test != "":
		s.failedTests = append(s.failedTests, e.Test)
	case e.FailedBuild != "":
		s.buildFailed = true
	default:
		s.failedPackage = true
	}
}

func (s eventSummary) terminationFailure() string {
	if s.termination == "" {
		return ""
	}
	if s.failedPackage || len(s.failedTests) > 0 {
		return "go test termination does not prove a mutation kill: " + s.termination
	}
	return ""
}

func (s eventSummary) provesKill() bool {
	return len(s.failedTests) > 0 || (s.failedPackage && s.runtimeFailure != "")
}

// classify proves a kill only from a failing test run, and never treats a
// setup or package-resolution failure as a result.
func classify(stream []byte) verdict {
	s := summarize(stream)
	v := verdict{packages: s.packages, testsRun: s.testsRun, failedTests: s.failedTests}
	termination := s.terminationFailure()
	switch {
	case termination != "":
		v.outcome, v.detail = outcomeUnproven, termination
	case s.setupFailed:
		v.outcome, v.detail = outcomeUnproven, "go test setup failed"
	case len(s.packages) == 0:
		v.outcome, v.detail = outcomeUnproven, "go test started no package"
	case s.buildFailed && s.compileError:
		v.outcome = outcomeNotViable
	case s.buildFailed:
		v.outcome, v.detail = outcomeUnproven, "go test build failed without a compile error"
	case s.testsRun == 0:
		v.outcome, v.detail = outcomeUnproven, "go test ran no test"
	case s.provesKill():
		v.outcome, v.detail = outcomeKilled, s.runtimeFailure
	case s.failedPackage:
		v.outcome, v.detail = outcomeUnproven, "go test package failed without a named test failure or runtime failure"
	default:
		v.outcome = outcomeLived
	}
	return v
}

func tail(b []byte, limit int) string {
	text := strings.TrimSpace(string(b))
	if len(text) > limit {
		text = text[len(text)-limit:]
	}
	return text
}

// pruneCache removes every GOCACHE entry missing from the warm list. Cache
// files are content-addressed, so a rebuilt dependency keeps its listed name.
// Go treats a removed entry as a miss, so concurrent workers stay correct.
func pruneCache(warmList, cacheDir string) error {
	if warmList == "" {
		return nil
	}
	if cacheDir == "" {
		return errors.New("GOCACHE is not set")
	}
	keep, err := readWarmList(warmList)
	if err != nil {
		return err
	}
	entries, err := filepath.Glob(filepath.Join(cacheDir, "[0-9a-f][0-9a-f]", "*"))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !keep[filepath.Base(entry)] {
			if err := os.Remove(entry); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
	}
	return nil
}

func readWarmList(path string) (map[string]bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	keep := make(map[string]bool)
	for line := range strings.Lines(string(data)) {
		keep[strings.TrimSpace(line)] = true
	}
	return keep, nil
}

func appendRecord(path string, rec record) error {
	if path == "" {
		return fmt.Errorf("%s is not set", logEnv)
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(append(line, '\n')); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}
