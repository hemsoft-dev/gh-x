package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const (
	passStream = `{"Action":"start","Package":"example.com/fixture/src"}
{"Action":"run","Package":"example.com/fixture/src","Test":"TestClamp"}
{"Action":"pass","Package":"example.com/fixture/src","Test":"TestClamp"}
{"Action":"pass","Package":"example.com/fixture/src"}
`
	testFailStream = `{"Action":"start","Package":"example.com/fixture/src"}
{"Action":"run","Package":"example.com/fixture/src","Test":"TestClamp"}
{"Action":"fail","Package":"example.com/fixture/src","Test":"TestClamp"}
{"Action":"fail","Package":"example.com/fixture/src"}
`
	// Recorded from go1.26.10 for `go test -json -failfast example.com/fixture`
	// in a module whose only package is example.com/fixture/src.
	setupFailStream = `{"ImportPath":"example.com/fixture","Action":"build-output","Output":"no required module provides package example.com/fixture; to add it:\n"}
{"ImportPath":"example.com/fixture","Action":"build-fail"}
{"Action":"start","Package":"example.com/fixture"}
{"Action":"output","Package":"example.com/fixture","Output":"FAIL\texample.com/fixture [setup failed]\n"}
{"Action":"fail","Package":"example.com/fixture","Elapsed":0,"FailedBuild":"example.com/fixture"}
`
	buildFailStream = `{"ImportPath":"example.com/fixture/src [example.com/fixture/src.test]","Action":"build-output","Output":"./main.go:5:10: cannot use \"x\" (untyped string constant) as int value in return statement\n"}
` + buildFailTail
	// A full disk breaks the build without any compiler diagnostic, as the
	// first full run on a quota-limited /tmp showed.
	diskFullStream = `{"ImportPath":"example.com/fixture/src [example.com/fixture/src.test]","Action":"build-output","Output":"compile: writing output: write /tmp/go-build1/b001/_pkg_.a: disk quota exceeded\n"}
` + buildFailTail
	buildFailTail = `{"ImportPath":"example.com/fixture/src [example.com/fixture/src.test]","Action":"build-fail"}
{"Action":"start","Package":"example.com/fixture/src"}
{"Action":"output","Package":"example.com/fixture/src","Output":"FAIL\texample.com/fixture/src [build failed]\n"}
{"Action":"fail","Package":"example.com/fixture/src","FailedBuild":"example.com/fixture/src [example.com/fixture/src.test]"}
`
	noTestsStream = `{"Action":"start","Package":"example.com/fixture/src"}
{"Action":"output","Package":"example.com/fixture/src","Output":"?   \texample.com/fixture/src\t[no test files]\n"}
{"Action":"skip","Package":"example.com/fixture/src"}
`
	binaryFailStream = `{"Action":"start","Package":"example.com/fixture/src"}
{"Action":"run","Package":"example.com/fixture/src","Test":"TestClamp"}
{"Action":"output","Package":"example.com/fixture/src","Output":"panic: boom\n"}
{"Action":"fail","Package":"example.com/fixture/src"}
`
)

func TestClassify(t *testing.T) {
	tests := []struct {
		name   string
		stream string
		want   outcome
		tests  int
		failed []string
	}{
		{"passing tests live", passStream, outcomeLived, 1, nil},
		{"failing test kills", testFailStream, outcomeKilled, 1, []string{"TestClamp"}},
		{"failing test binary kills", binaryFailStream, outcomeKilled, 1, nil},
		{"setup failure is unproven", setupFailStream, outcomeUnproven, 0, nil},
		{"compile error is not viable", buildFailStream, outcomeNotViable, 0, nil},
		{"build failure without a compile error is unproven", diskFullStream, outcomeUnproven, 0, nil},
		{"package without tests is unproven", noTestsStream, outcomeUnproven, 0, nil},
		{"empty stream is unproven", "not json\n", outcomeUnproven, 0, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classify([]byte(tt.stream))
			if got.outcome != tt.want || got.testsRun != tt.tests || !slices.Equal(got.failedTests, tt.failed) {
				t.Fatalf("classify = %+v, want %s with %d tests and failures %v", got, tt.want, tt.tests, tt.failed)
			}
			if tt.want == outcomeUnproven && got.detail == "" {
				t.Fatal("an unproven run must say why")
			}
		})
	}
}

func TestClassifyListsStartedPackages(t *testing.T) {
	got := classify([]byte(passStream))
	if !slices.Equal(got.packages, []string{"example.com/fixture/src"}) {
		t.Fatalf("packages = %v", got.packages)
	}
}

func TestOutcomeExitCodesMatchGremlinsStatuses(t *testing.T) {
	for o, want := range map[outcome]int{outcomeKilled: 1, outcomeLived: 0, outcomeNotViable: 2, outcomeUnproven: 3, "other": 3} {
		if got := o.exitCode(); got != want {
			t.Errorf("%s exit = %d, want %d", o, got, want)
		}
	}
}

func TestContradicts(t *testing.T) {
	tests := []struct {
		o    outcome
		code int
		want bool
	}{
		{outcomeLived, 0, false},
		{outcomeLived, 1, true},
		{outcomeKilled, 1, false},
		{outcomeKilled, 0, true},
		{outcomeNotViable, 1, false},
		{outcomeNotViable, 0, true},
		{outcomeUnproven, 0, false},
		{outcomeUnproven, 1, false},
	}
	for _, tt := range tests {
		if got := contradicts(tt.o, tt.code); got != tt.want {
			t.Errorf("contradicts(%s, %d) = %v, want %v", tt.o, tt.code, got, tt.want)
		}
	}
}

func TestIsMutantTest(t *testing.T) {
	tests := []struct {
		args []string
		want bool
	}{
		{[]string{"test", "-timeout", "12s", "-failfast", "github.com/HemSoft/gh-x"}, true},
		{[]string{"test", "-tags", "x", "-timeout", "12s", "-failfast", "./..."}, true},
		{[]string{"test", "-cover", "-coverprofile", "c.out", "./src/...", "-failfast"}, false},
		{[]string{"test", "-cover", "-failfast", "./src/..."}, false},
		{[]string{"test", "-timeout", "12s", "github.com/HemSoft/gh-x"}, false},
		{[]string{"test", "-failfast"}, false},
		{[]string{"mod", "download"}, false},
		{[]string{"test"}, false},
		{nil, false},
	}
	for _, tt := range tests {
		if got := isMutantTest(tt.args); got != tt.want {
			t.Errorf("isMutantTest(%q) = %v, want %v", tt.args, got, tt.want)
		}
	}
}

func TestMutantTestArgsRunCallingDirectoryPackages(t *testing.T) {
	got := mutantTestArgs([]string{"test", "-tags", "x", "-timeout", "12s", "-failfast", "github.com/HemSoft/gh-x"})
	want := []string{"test", "-json", "-vet=off", "-tags", "x", "-timeout", "12s", "-failfast", "./..."}
	if !slices.Equal(got, want) {
		t.Fatalf("args = %q, want %q", got, want)
	}
}

func TestTail(t *testing.T) {
	if got := tail([]byte("  abcdef \n"), 3); got != "def" {
		t.Fatalf("tail = %q", got)
	}
	if got := tail([]byte("ab"), 3); got != "ab" {
		t.Fatalf("short tail = %q", got)
	}
}

func TestAppendRecord(t *testing.T) {
	if err := appendRecord("", record{}); err == nil {
		t.Fatal("a missing log path must fail")
	}
	path := filepath.Join(t.TempDir(), "runs.jsonl")
	for _, o := range []outcome{outcomeKilled, outcomeLived} {
		if err := appendRecord(path, record{Outcome: o, TestsRun: 2}); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `"outcome":"killed"`) || !strings.Contains(lines[1], `"outcome":"lived"`) {
		t.Fatalf("log = %q", data)
	}
	if err := appendRecord(filepath.Join(path, "missing", "x"), record{}); err == nil {
		t.Fatal("an unwritable log must fail")
	}
}

func TestPruneCacheKeepsOnlyWarmEntries(t *testing.T) {
	cache := t.TempDir()
	for _, name := range []string{"0a/keep-d", "0a/new-d", "ff/new-a", "trim.txt", "README"} {
		path := filepath.Join(cache, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, path, "x")
	}
	list := filepath.Join(t.TempDir(), "warm.txt")
	writeFile(t, list, "keep-d\ntrim.txt\n")
	if err := pruneCache(list, cache); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]bool{"0a/keep-d": true, "0a/new-d": false, "ff/new-a": false, "trim.txt": true, "README": true} {
		_, err := os.Stat(filepath.Join(cache, filepath.FromSlash(name)))
		if (err == nil) != want {
			t.Errorf("%s present = %v, want %v", name, err == nil, want)
		}
	}
}

func TestPruneCacheNeedsItsInputs(t *testing.T) {
	if err := pruneCache("", ""); err != nil {
		t.Fatalf("no warm list must skip pruning: %v", err)
	}
	if err := pruneCache("warm.txt", ""); err == nil {
		t.Fatal("a missing GOCACHE must fail")
	}
	if err := pruneCache(filepath.Join(t.TempDir(), "missing.txt"), t.TempDir()); err == nil {
		t.Fatal("a missing warm list must fail")
	}
}

func TestRunRequiresRealGo(t *testing.T) {
	var stderr bytes.Buffer
	if code := run([]string{"version"}, env(nil), &bytes.Buffer{}, &stderr); code != exitUnproven {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(stderr.String(), realGoEnv) {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRunPassesOtherCommandsThrough(t *testing.T) {
	realGo := findGo(t)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"env", "GOOS"}, env(map[string]string{realGoEnv: realGo}), &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, stderr %q", code, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) == "" {
		t.Fatal("passthrough lost stdout")
	}
	if code := run([]string{"no-such-command"}, env(map[string]string{realGoEnv: realGo}), &stdout, &stderr); code == 0 {
		t.Fatal("passthrough must keep a failing exit code")
	}
	missing := filepath.Join(t.TempDir(), "missing-go")
	if code := run([]string{"env"}, env(map[string]string{realGoEnv: missing}), &stdout, &stderr); code != exitUnproven {
		t.Fatalf("missing go exit = %d", code)
	}
}

// The defect: Gremlins asks for the module root of a package main living
// in src. The shim must run src's real tests and classify them.
func TestRunTestsTheCallingDirectoryForTheModuleRootTarget(t *testing.T) {
	realGo := findGo(t)
	srcDir := writeFixtureModule(t, "return 10")
	logPath := filepath.Join(t.TempDir(), "runs.jsonl")
	vars := map[string]string{realGoEnv: realGo, logEnv: logPath}

	if code := runIn(t, srcDir, vars); code != exitLived {
		t.Fatalf("clean tree exit = %d, log %s", code, readLog(t, logPath))
	}
	writeFile(t, filepath.Join(srcDir, "main.go"), fixtureSource("return 9"))
	if code := runIn(t, srcDir, vars); code != exitKilled {
		t.Fatalf("detected mutant exit = %d, log %s", code, readLog(t, logPath))
	}
	writeFile(t, filepath.Join(srcDir, "main.go"), fixtureSource(`return "x"`))
	if code := runIn(t, srcDir, vars); code != exitNotViable {
		t.Fatalf("uncompilable mutant exit = %d, log %s", code, readLog(t, logPath))
	}

	records := decodeLog(t, logPath)
	want := []outcome{outcomeLived, outcomeKilled, outcomeNotViable}
	if len(records) != len(want) {
		t.Fatalf("records = %+v", records)
	}
	for i, rec := range records {
		if rec.Outcome != want[i] || rec.Requested != "example.com/fixture" {
			t.Fatalf("record %d = %+v, want %s", i, rec, want[i])
		}
		if !slices.Equal(rec.Packages, []string{"example.com/fixture/src"}) {
			t.Fatalf("record %d tested %v", i, rec.Packages)
		}
	}
	if records[0].TestsRun != 1 || records[1].TestsRun != 1 || !slices.Equal(records[1].FailedTests, []string{"TestClamp"}) {
		t.Fatalf("records did not come from test runs: %+v", records)
	}
}

func TestRunMarksResolutionFailureUnproven(t *testing.T) {
	realGo := findGo(t)
	outside := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "runs.jsonl")
	vars := map[string]string{realGoEnv: realGo, logEnv: logPath}
	if code := runIn(t, outside, vars); code != exitUnproven {
		t.Fatalf("exit = %d, log %s", code, readLog(t, logPath))
	}
	records := decodeLog(t, logPath)
	if len(records) != 1 || records[0].Outcome != outcomeUnproven || records[0].Detail == "" {
		t.Fatalf("records = %+v", records)
	}
}

func TestRunFailsWithoutLog(t *testing.T) {
	realGo := findGo(t)
	srcDir := writeFixtureModule(t, "return 10")
	var stderr bytes.Buffer
	t.Chdir(srcDir)
	args := []string{"test", "-timeout", "60s", "-failfast", "example.com/fixture"}
	if code := run(args, env(map[string]string{realGoEnv: realGo}), &bytes.Buffer{}, &stderr); code != exitUnproven {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(stderr.String(), logEnv) {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestExecuteReportsStartFailure(t *testing.T) {
	rec := testMutant(filepath.Join(t.TempDir(), "missing-go"), []string{"test", "-failfast", "x"})
	if rec.Outcome != outcomeUnproven || !strings.Contains(rec.Detail, "cannot run go") {
		t.Fatalf("record = %+v", rec)
	}
}

func runIn(t *testing.T, dir string, vars map[string]string) int {
	t.Helper()
	t.Chdir(dir)
	args := []string{"test", "-timeout", "60s", "-failfast", "example.com/fixture"}
	return run(args, env(vars), &bytes.Buffer{}, &bytes.Buffer{})
}

func env(vars map[string]string) func(string) string {
	return func(name string) string { return vars[name] }
}

func findGo(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go is not on PATH")
	}
	return path
}

func fixtureSource(body string) string {
	return "package main\n\nfunc clamp(n int) int {\n\tif n > 10 {\n\t\t" + body + "\n\t}\n\treturn n\n}\n\nfunc main() { _ = clamp(1) }\n"
}

func writeFixtureModule(t *testing.T, body string) string {
	t.Helper()
	root := t.TempDir()
	srcDir := filepath.Join(root, "src")
	if err := os.Mkdir(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "go.mod"), "module example.com/fixture\n\ngo 1.22\n")
	writeFile(t, filepath.Join(srcDir, "main.go"), fixtureSource(body))
	writeFile(t, filepath.Join(srcDir, "main_test.go"), "package main\n\nimport \"testing\"\n\nfunc TestClamp(t *testing.T) {\n\tif clamp(11) != 10 || clamp(5) != 5 {\n\t\tt.Fatal(\"clamp\")\n\t}\n}\n")
	return srcDir
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readLog(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return err.Error()
	}
	return string(data)
}

func decodeLog(t *testing.T, path string) []record {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var records []record
	for line := range strings.Lines(string(data)) {
		var rec record
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("bad record %q: %v", line, err)
		}
		records = append(records, rec)
	}
	return records
}
