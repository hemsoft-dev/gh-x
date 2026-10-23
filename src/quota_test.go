package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestGraphQLQuotaHTTPEnvelope(t *testing.T) {
	now := time.Unix(1800000000, 0)
	raw := []byte("HTTP/2.0 200 OK\r\nX-Ratelimit-Remaining: 0\r\nX-Ratelimit-Reset: 1800001800\r\nX-Ratelimit-Resource: graphql\r\n\r\n" + `{"errors":[{"type":"RATE_LIMITED","message":"API rate limit exceeded"}]}`)
	body, headers := splitQuotaResponse(raw)
	if !bytes.HasPrefix(body, []byte(`{"errors"`)) {
		t.Fatalf("body = %q", body)
	}
	until, limited := quotaDeadline(headers, body, "", now, 0)
	if !limited || !until.Equal(now.Add(30*time.Minute)) {
		t.Fatalf("deadline = %v, limited=%v", until, limited)
	}
	if got := statusRetryDelay(&quotaError{Until: until}, now); got != 30*time.Minute {
		t.Fatalf("cache delay = %v", got)
	}
}

func TestQuotaSecondaryBackoff(t *testing.T) {
	now := time.Unix(1800000000, 0)
	for _, test := range []struct {
		headers quotaHeaders
		attempt int
		want    time.Duration
	}{
		{quotaHeaders{"Retry-After": {"120"}}, 0, 2 * time.Minute},
		{quotaHeaders{}, 0, time.Minute},
		{quotaHeaders{}, 1, 2 * time.Minute},
		{quotaHeaders{}, 20, 15 * time.Minute},
	} {
		until, limited := quotaDeadline(test.headers, nil, "secondary rate limit", now, test.attempt)
		if !limited || until.Sub(now) != test.want {
			t.Fatalf("%+v: %v %v", test, until, limited)
		}
	}
	if _, limited := quotaDeadline(quotaHeaders{}, []byte(`{"errors":[{"message":"unknown field"}]}`), "", now, 0); limited {
		t.Fatal("schema error classified as rate limit")
	}
	if !errors.As(&quotaError{Until: now}, new(*quotaError)) {
		t.Fatal("quota error lost")
	}
}

func TestQuotaTransportSuppressesRequestsAndIsolatesResources(t *testing.T) {
	resetQuotaAuth(t)
	directory := t.TempDir()
	savedDirectory, savedNow, savedRaw := quotaDirectoryFunc, quotaNowFunc, quotaRawFunc
	t.Cleanup(func() { quotaDirectoryFunc, quotaNowFunc, quotaRawFunc = savedDirectory, savedNow, savedRaw })
	quotaDirectoryFunc = func() (string, error) { return directory, nil }
	now := time.Unix(1800000000, 0)
	quotaNowFunc = func() time.Time { return now }
	t.Setenv("GH_CONFIG_DIR", t.TempDir())
	t.Setenv("GH_TOKEN", "synthetic-account-a")
	calls := 0
	quotaRawFunc = func(inv ghInvocation) (bytes.Buffer, bytes.Buffer, error) {
		calls++
		if calls == 1 {
			if !slices.Contains(inv.Args, "--include") {
				t.Fatal("headers not requested")
			}
			return *bytes.NewBufferString("HTTP/2.0 200 OK\r\nX-Ratelimit-Remaining: 0\r\nX-Ratelimit-Reset: 1800001800\r\n\r\n" + `{"errors":[{"type":"RATE_LIMITED","message":"API rate limit exceeded"}]}`), bytes.Buffer{}, nil
		}
		return *bytes.NewBufferString(`{"data":{"ok":true}}`), bytes.Buffer{}, nil
	}
	graphql := ghInvocation{Context: context.Background(), Args: []string{"api", "--hostname", "github.com", "graphql", "-f", "query=query { viewer { login } }"}}
	_, _, err := runGHCmd(graphql)
	var quota *quotaError
	if !errors.As(err, &quota) {
		t.Fatalf("HTTP 200 quota error = %v", err)
	}
	for range 3 {
		_, _, err = runGHCmd(graphql)
		if !errors.As(err, &quota) {
			t.Fatal("cooldown not visible")
		}
	}
	if calls != 1 {
		t.Fatalf("quota requests = %d", calls)
	}
	rest := graphql
	rest.Args = []string{"api", "--hostname", "github.com", "repos/other/repo/rulesets"}
	if _, _, err := runGHCmd(rest); err != nil {
		t.Fatal(err)
	}
	otherHost := graphql
	otherHost.Args = []string{"api", "--hostname", "enterprise.example", "graphql"}
	if _, _, err := runGHCmd(otherHost); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_TOKEN", "synthetic-account-b")
	if _, _, err := runGHCmd(graphql); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_TOKEN", "synthetic-account-a")
	t.Setenv("GH_REPO", "github.com/other/repository")
	list := graphql
	list.Args = []string{"pr", "list", "--repo", "github.com/other/repository"}
	if _, _, err := runGHCmd(list); !errors.As(err, &quota) {
		t.Fatal("cross-repository request not suppressed")
	}
	if calls != 4 {
		t.Fatalf("isolated requests = %d", calls)
	}
	now = now.Add(30 * time.Minute)
	if _, _, err := runGHCmd(graphql); err != nil {
		t.Fatal(err)
	}
	if calls != 5 {
		t.Fatalf("resumption calls = %d", calls)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".json") {
			t.Fatal("successful resumption retained cooldown")
		}
	}
}

func TestQuotaResponsePreservesLargePartialData(t *testing.T) {
	body := []byte(`{"data":{"value":"` + strings.Repeat("x", 10000) + `"},"errors":[{"message":"field failed"}]}`)
	raw := append([]byte("HTTP/2.0 200 OK\nContent-Type: application/json\n\n"), body...)
	got, _ := splitQuotaResponse(raw)
	if !bytes.Equal(got, body) {
		t.Fatal("large partial body truncated")
	}
}

func TestQuotaRecordedGHProducer(t *testing.T) {
	raw, err := os.ReadFile("testdata/quota/gh-2.101.0-http.txt")
	if err != nil {
		t.Fatal(err)
	}
	body, headers := splitQuotaResponse(raw)
	until, limited := quotaDeadline(headers, body, "", time.Unix(1800000000, 0), 0)
	if !limited || until.Unix() != 1800001800 {
		t.Fatalf("producer response = %v, %v", until, limited)
	}
}

func TestQuotaParsingEdges(t *testing.T) {
	now := time.Unix(1800000000, 0)
	for _, raw := range []string{`{"data":{}}`, "HTTP/2.0", "HTTP/2.0 200 OK\ninvalid header\n\nbody"} {
		body, _ := splitQuotaResponse([]byte(raw))
		if string(body) != raw {
			t.Fatalf("malformed response changed: %q", body)
		}
	}
	for _, test := range []struct {
		value string
		want  time.Duration
	}{
		{"0", time.Second}, {"invalid", time.Minute}, {"-1", time.Minute},
		{now.Add(3 * time.Minute).UTC().Format("Mon, 02 Jan 2006 15:04:05 GMT"), 3 * time.Minute},
		{now.Add(-time.Minute).UTC().Format("Mon, 02 Jan 2006 15:04:05 GMT"), time.Second},
	} {
		if got := quotaRetryAfter(test.value, now, time.Minute); got != test.want {
			t.Fatalf("retry %q = %v", test.value, got)
		}
	}
	for _, test := range []struct {
		args     []string
		resource string
		capture  bool
	}{
		{nil, "", false}, {[]string{"auth", "status"}, "", false},
		{[]string{"api", "graphql"}, "graphql", true},
		{[]string{"api", "repos/o/r"}, "core", true},
		{[]string{"run", "list"}, "core", false},
		{[]string{"repo", "view"}, "graphql", false},
		{[]string{"api", "user", "--jq", ".login"}, "core", false},
		{[]string{"api", "user", "--paginate"}, "core", false},
		{[]string{"api", "user", "--include"}, "core", false},
	} {
		if got := quotaResource(test.args); got != test.resource {
			t.Fatalf("resource %v = %s", test.args, got)
		}
		if got := quotaCapture(test.args); got != test.capture {
			t.Fatalf("capture %v = %v", test.args, got)
		}
	}
	until, limited := quotaDeadline(quotaHeaders{"X-Ratelimit-Remaining": {"0"}, "X-Ratelimit-Reset": {"bad"}}, nil, "", now, 0)
	if !limited || until.Sub(now) != time.Minute {
		t.Fatal("invalid reset did not back off")
	}
}

func TestQuotaCombinedDeadlines(t *testing.T) {
	now := time.Unix(1800000000, 0)
	until, limited := quotaDeadline(quotaHeaders{"X-Ratelimit-Remaining": {"0"}, "X-Ratelimit-Reset": {"1800000005"}, "Retry-After": {"120"}}, nil, "", now, 0)
	if !limited || until.Sub(now) != 2*time.Minute {
		t.Fatal("Retry-After shortened by primary reset")
	}
}

func TestQuotaPartialSuccessAndCachedDeadline(t *testing.T) {
	resetQuotaAuth(t)
	savedRaw, savedDirectory, savedNow := quotaRawFunc, quotaDirectoryFunc, quotaNowFunc
	t.Cleanup(func() { quotaRawFunc, quotaDirectoryFunc, quotaNowFunc = savedRaw, savedDirectory, savedNow })
	directory := t.TempDir()
	quotaDirectoryFunc = func() (string, error) { return directory, nil }
	now := time.Unix(1800000000, 0)
	quotaNowFunc = func() time.Time { return now }
	t.Setenv("GH_CONFIG_DIR", t.TempDir())
	t.Setenv("GH_TOKEN", "synthetic-partial-account")
	body := `{"data":{"viewer":{"login":"fixture"}}}`
	quotaRawFunc = func(inv ghInvocation) (bytes.Buffer, bytes.Buffer, error) {
		return *bytes.NewBufferString("HTTP/2.0 200 OK\r\nX-Ratelimit-Remaining: 0\r\nX-Ratelimit-Reset: 1800001800\r\n\r\n" + body), bytes.Buffer{}, nil
	}
	inv := ghInvocation{Context: context.Background(), Args: []string{"api", "--hostname", "github.com", "graphql"}}
	out, _, err := runGHCmd(inv)
	if err != nil || out.String() != body {
		t.Fatalf("last point data: %s, %v", out.String(), err)
	}
	_, _, err = runGHCmd(inv)
	if err == nil {
		t.Fatal("last point did not set cooldown")
	}
	dashboard := statusDashboard{IssuesErr: err}
	sections := cacheStatusSections(dashboard, now)
	if !sections[0].RetryAfter.Equal(now.Add(30 * time.Minute)) {
		t.Fatal("cache shortened deadline")
	}
	restored := statusDashboard{}
	restoreStatusFailures(&restored, cacheStatusFailures(dashboard))
	if delay := statusRetryDelay(restored.IssuesErr, now); delay != 30*time.Minute {
		t.Fatalf("restored deadline=%v", delay)
	}
}

func TestQuotaReviewRegressions(t *testing.T) {
	for _, test := range []struct {
		args []string
		want string
	}{
		{[]string{"api", "search/users", "--jq", ".items[].login"}, "search"},
		{[]string{"api", "--hostname", "example.test", "/search/code?q=x"}, "code_search"},
		{[]string{"workflow", "list"}, "core"},
		{[]string{"pr", "list", "--web"}, ""},
		{[]string{"pr", "list", "-w"}, ""},
		{[]string{"pr", "list", "--search", "--web"}, "graphql"},
	} {
		if got := quotaResource(test.args); got != test.want {
			t.Fatalf("%v: resource=%q want=%q", test.args, got, test.want)
		}
	}
}

func resetQuotaAuth(t *testing.T) {
	t.Helper()
	saved := quotaAuthContextFunc
	quotaAuthContextFunc = sync.OnceValues(statusAuthContext)
	t.Cleanup(func() { quotaAuthContextFunc = saved })
}

func TestQuotaConcurrentPublication(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quota.json")
	now := quotaNowFunc()
	observed := readQuota(path)
	longer := quotaRecord{Until: now.Add(30 * time.Minute), Attempts: 2}
	shorter := quotaRecord{Until: now.Add(time.Minute), Attempts: 1}
	saveQuota(path, longer)
	clearQuota(path, observed)
	if got := readQuota(path); !got.Until.Equal(longer.Until) {
		t.Fatal("concurrent success erased new deadline")
	}
	saveQuota(path, shorter)
	if got := readQuota(path); !got.Until.Equal(longer.Until) || got.Attempts != 2 {
		t.Fatal("concurrent publisher shortened cooldown")
	}
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() { saveQuota(path, shorter); clearQuota(path, observed) })
	}
	group.Wait()
	if got := readQuota(path); !got.Until.Equal(longer.Until) {
		t.Fatal("parallel publication lost reset")
	}
	expired := quotaRecord{Until: now.Add(-time.Minute), Attempts: 1}
	other := filepath.Join(t.TempDir(), "quota.json")
	saveQuota(other, expired)
	clearQuota(other, readQuota(other))
	if _, err := os.Stat(other); !os.IsNotExist(err) {
		t.Fatal("expired observation not cleared")
	}
}

func TestQuotaAuthFingerprintIsCached(t *testing.T) {
	resetQuotaAuth(t)
	directory := t.TempDir()
	t.Setenv("GH_CONFIG_DIR", directory)
	for _, name := range []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN"} {
		t.Setenv(name, "")
	}
	if err := os.WriteFile(filepath.Join(directory, "hosts.yml"), []byte("github.com:\n  user: fixture\n  users:\n    fixture: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	saved := statusKeyringGetFunc
	t.Cleanup(func() { statusKeyringGetFunc = saved })
	reads := 0
	statusKeyringGetFunc = func(service, user string) (string, error) { reads++; return "synthetic-keyring", nil }
	inv := ghInvocation{Context: context.Background(), Args: []string{"api", "--hostname", "github.com", "graphql"}}
	first := quotaPath(inv, "graphql")
	initialReads := reads
	if first == "" || initialReads == 0 {
		t.Fatal("secure storage not fingerprinted")
	}
	for range 5 {
		if got := quotaPath(inv, "graphql"); got != first {
			t.Fatal("fingerprint changed")
		}
	}
	inv.ExtraEnv = []string{"GH_TOKEN=synthetic-fallback"}
	if got := quotaPath(inv, "graphql"); got == first || got == "" {
		t.Fatal("credential override lost")
	}
	if reads != initialReads {
		t.Fatalf("keyring probed %d times, first acquisition=%d", reads, initialReads)
	}
}

func TestQuotaPreservesUnrelatedLastPointError(t *testing.T) {
	resetQuotaAuth(t)
	savedRaw, savedDirectory := quotaRawFunc, quotaDirectoryFunc
	t.Cleanup(func() { quotaRawFunc, quotaDirectoryFunc = savedRaw, savedDirectory })
	directory := t.TempDir()
	quotaDirectoryFunc = func() (string, error) { return directory, nil }
	t.Setenv("GH_CONFIG_DIR", t.TempDir())
	t.Setenv("GH_TOKEN", "synthetic-schema-error")
	original := errors.New("exit status 1")
	message := "Cannot query field parent on type Issue"
	reset := quotaNowFunc().Add(30 * time.Minute).Unix()
	quotaRawFunc = func(ghInvocation) (bytes.Buffer, bytes.Buffer, error) {
		return *bytes.NewBufferString(fmt.Sprintf("HTTP/2.0 200 OK\r\nX-Ratelimit-Remaining: 0\r\nX-Ratelimit-Reset: %d\r\n\r\n", reset) + `{"errors":[{"message":"Cannot query field parent on type Issue"}]}`), *bytes.NewBufferString(message), original
	}
	inv := ghInvocation{Context: context.Background(), Args: []string{"api", "--hostname", "github.com", "graphql"}}
	_, stderr, err := runGHCmd(inv)
	if !errors.Is(err, original) || stderr.String() != message {
		t.Fatalf("schema error replaced: %q, %v", stderr.String(), err)
	}
	var quota *quotaError
	if _, _, err := runGHCmd(inv); !errors.As(err, &quota) {
		t.Fatal("last point failure did not set future cooldown")
	}
}

func TestQuotaCoveredRoutes(t *testing.T) {
	resetQuotaAuth(t)
	savedRaw, savedDirectory := quotaRawFunc, quotaDirectoryFunc
	t.Cleanup(func() { quotaRawFunc, quotaDirectoryFunc = savedRaw, savedDirectory })
	directory := t.TempDir()
	quotaDirectoryFunc = func() (string, error) { return directory, nil }
	t.Setenv("GH_CONFIG_DIR", t.TempDir())
	t.Setenv("GH_TOKEN", "synthetic-route-fixture")
	calls := 0
	quotaRawFunc = func(ghInvocation) (bytes.Buffer, bytes.Buffer, error) {
		calls++
		return bytes.Buffer{}, bytes.Buffer{}, nil
	}
	invocation := func(args ...string) ghInvocation { return ghInvocation{Context: context.Background(), Args: args} }
	api := invocation("api", "--hostname", "github.com", "search/users", "--jq", ".items[].login")
	future := quotaRecord{Until: quotaNowFunc().Add(time.Hour)}
	saveQuota(quotaPath(api, "search"), future)
	var quota *quotaError
	if _, _, err := runGHCmd(api); !errors.As(err, &quota) {
		t.Fatal("search quota not enforced")
	}
	core := invocation("api", "--hostname", "github.com", "repos/o/r/rules/branches/main")
	if _, _, err := runGHCmd(core); err != nil {
		t.Fatal("search blocked core", err)
	}
	saveQuota(quotaPath(core, "core"), future)
	workflow := invocation("workflow", "list", "--repo", "github.com/o/r")
	if _, _, err := runGHCmd(workflow); !errors.As(err, &quota) {
		t.Fatal("workflow bypassed core")
	}
	web := invocation("pr", "list", "--repo", "github.com/o/r", "--web")
	saveQuota(quotaPath(web, "graphql"), future)
	if _, _, err := runGHCmd(web); err != nil {
		t.Fatal("browser blocked", err)
	}
	if calls != 2 {
		t.Fatalf("outbound requests=%d", calls)
	}
}

func TestQuotaAuthoritativeResource(t *testing.T) {
	resetQuotaAuth(t)
	t.Setenv("GH_CONFIG_DIR", t.TempDir())
	t.Setenv("GH_TOKEN", "synthetic-resource-header")
	inv := ghInvocation{Context: context.Background(), Args: []string{"api", "--hostname", "github.com", "user"}}
	original := quotaRecord{Attempts: 2}
	path, record := quotaResponseScope(inv, quotaHeaders{"X-Ratelimit-Resource": {"search"}}, "original", original)
	if path == "" || path == "original" || record.Attempts != 0 {
		t.Fatal("authoritative resource ignored")
	}
	for _, resource := range []string{"", "core", "invalid"} {
		path, record := quotaResponseScope(inv, quotaHeaders{"X-Ratelimit-Resource": {resource}}, "original", original)
		if path != "original" || record != original {
			t.Fatal("matching or unknown resource changed scope")
		}
	}
	for _, test := range []struct{ endpoint, want string }{
		{"http://example.test/api/v3/search/users?q=x", "search"},
		{"http://example.test/search/code?q=x", "code_search"},
		{"http://example.test/graphql", "graphql"},
		{"%invalid", "core"},
	} {
		if got := quotaAPIResource(test.endpoint); got != test.want {
			t.Fatalf("%s: %s", test.endpoint, got)
		}
	}
}
