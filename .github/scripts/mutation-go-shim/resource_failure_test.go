package main

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestResourceFailureCannotProveMutationKill(t *testing.T) {
	start := `{"Action":"start","Package":"example.com/fixture/src"}` + "\n" +
		`{"Action":"run","Package":"example.com/fixture/src","Test":"TestClamp"}` + "\n"
	end := `{"Action":"fail","Package":"example.com/fixture/src"}` + "\n"
	for _, output := range []string{
		"signal: killed",
		"fatal error: runtime: out of memory",
		"fatal error: out of memory",
		"runtime: cannot allocate memory",
		"panic: test timed out after 1s",
	} {
		t.Run(output, func(t *testing.T) {
			stream := start + `{"Action":"output","Package":"example.com/fixture/src","Output":"` + output + `\n"}` + "\n" + end
			got := classify([]byte(stream))
			if got.outcome != outcomeUnproven || got.detail == "" {
				t.Fatalf("resource failure classified as %+v; want unproven with evidence", got)
			}
		})
	}
	t.Run("unknown package failure", func(t *testing.T) {
		got := classify([]byte(start + end))
		if got.outcome != outcomeUnproven || got.detail == "" {
			t.Fatalf("unexplained package failure classified as %+v", got)
		}
	})
	t.Run("successful test may log a resource error", func(t *testing.T) {
		stream := start + `{"Action":"output","Package":"example.com/fixture/src","Output":"signal: killed\n"}` + "\n" +
			`{"Action":"pass","Package":"example.com/fixture/src","Test":"TestClamp"}` + "\n" +
			`{"Action":"pass","Package":"example.com/fixture/src"}` + "\n"
		if got := classify([]byte(stream)); got.outcome != outcomeLived {
			t.Fatalf("successful execution classified as %+v", got)
		}
	})
	t.Run("resource failure after a named test failure", func(t *testing.T) {
		stream := start + `{"Action":"fail","Package":"example.com/fixture/src","Test":"TestClamp"}` + "\n" +
			`{"Action":"output","Package":"example.com/fixture/src","Output":"signal: killed\n"}` + "\n" + end
		if got := classify([]byte(stream)); got.outcome != outcomeUnproven || !strings.Contains(got.detail, "signal: killed") {
			t.Fatalf("resource failure must quarantine the whole execution: %+v", got)
		}
	})
	t.Run("bounded child failure is a named assertion", func(t *testing.T) {
		stream := start + `{"Action":"output","Package":"example.com/fixture/src","Test":"TestClamp","Output":"        fatal error: out of memory\n"}` + "\n" +
			`{"Action":"fail","Package":"example.com/fixture/src","Test":"TestClamp"}` + "\n" + end
		if got := classify([]byte(stream)); got.outcome != outcomeKilled || len(got.failedTests) != 1 {
			t.Fatalf("quoted child diagnostics must retain the parent assertion: %+v", got)
		}
	})
}

func TestSignaledGoCommandCannotProveKill(t *testing.T) {
	for _, outcome := range []outcome{outcomeKilled, outcomeNotViable} {
		if !contradicts(outcome, -1) {
			t.Fatalf("a signaled go command cannot prove %s", outcome)
		}
	}
}

func TestRunKilledTestProcessIsUnproven(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("replays the Linux SIGKILL path used by the bounded mutation run")
	}
	realGo := findGo(t)
	srcDir := writeFixtureModule(t, "return 10")
	writeFile(t, filepath.Join(srcDir, "main_test.go"), `package main
import ("os"; "testing")
func TestKilledProcess(t *testing.T) {
    p, err := os.FindProcess(os.Getpid())
    if err != nil { t.Fatal(err) }
    if err := p.Kill(); err != nil { t.Fatal(err) }
}
`)
	logPath := filepath.Join(t.TempDir(), "runs.jsonl")
	vars := map[string]string{realGoEnv: realGo, logEnv: logPath}
	if code := runIn(t, srcDir, vars); code != exitUnproven {
		t.Fatalf("SIGKILL exit = %d, log %s", code, readLog(t, logPath))
	}
	records := decodeLog(t, logPath)
	if len(records) != 1 || records[0].Outcome != outcomeUnproven || records[0].TestsRun == 0 || !strings.Contains(records[0].Detail, "signal: killed") {
		t.Fatalf("SIGKILL must be recorded without a mutation kill: %+v", records)
	}
}
