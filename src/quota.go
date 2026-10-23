package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/flock"
)

type quotaError struct{ Until time.Time }

func (e *quotaError) Error() string {
	return fmt.Sprintf("GitHub rate limit; x-ratelimit-reset: %d", e.Until.Unix())
}

type quotaRecord struct {
	Until    time.Time `json:"until"`
	Attempts int       `json:"attempts"`
}

// Only deadlines and attempt counts are persisted. The path hashes host,
// resource and effective credential context, never repository-specific data.
// A CLI process owns one command. Cache expensive credential-store reads once;
// quotaPath still includes per-invocation credentials and environment overrides.
var quotaAuthContextFunc = sync.OnceValues(statusAuthContext)
var quotaRawFunc = runGHCmdRaw
var quotaNowFunc = time.Now
var quotaDirectoryFunc = func() (string, error) {
	directory, err := os.UserCacheDir()
	return filepath.Join(directory, "gh-x", "api-quota-v1"), err
}

func quotaPath(inv ghInvocation, resource string) string {
	if resource == "" {
		return ""
	}
	auth, err := quotaAuthContextFunc()
	if err != nil {
		return ""
	}
	directory, err := quotaDirectoryFunc()
	if err != nil {
		return ""
	}
	identity := statusRemoteFingerprint(strings.Join([]string{auth, strings.Join(inv.ExtraEnv, "\x00"),
		os.Getenv("GH_TOKEN"), os.Getenv("GITHUB_TOKEN"),
		os.Getenv("GH_ENTERPRISE_TOKEN"), os.Getenv("GITHUB_ENTERPRISE_TOKEN"),
	}, "\x00"))
	host := targetHostContext(inv.Context, inv.Args)
	return filepath.Join(directory, statusRemoteFingerprint(host+"\x00"+resource+"\x00"+identity)+".json")
}

func quotaResource(args []string) string {
	if len(args) < 2 {
		return ""
	}
	if quotaBrowserList(args) {
		return ""
	}
	switch args[0] {
	case "pr", "issue", "repo":
		return "graphql"
	case "run", "workflow":
		return "core"
	case "api":
		return quotaAPIResource(quotaEndpoint(args))
	default:
		return ""
	}
}

// Skip flag values as well as flags: a search string equal to --web must
// never turn an API acquisition into a browser-only operation.
func quotaBrowserList(args []string) bool {
	if args[1] != "list" || (args[0] != "pr" && args[0] != "issue") {
		return false
	}
	for i := 2; i < len(args); i++ {
		if args[i] == "--web" || args[i] == "-w" || args[i] == "--web=true" {
			return true
		}
		if quotaValueFlag(args[i]) {
			i++
		}
	}
	return false
}

func quotaValueFlag(arg string) bool {
	return slices.Contains([]string{"--hostname", "--repo", "-R", "--search", "-S", "--author", "-A", "--assignee", "-a", "--state", "-s", "--limit", "-L", "--json", "--jq", "-q", "--template", "-t", "--base", "-B", "--head", "-H", "--label", "-l", "--app", "--milestone", "-m", "--field", "-F", "--raw-field", "-f", "--header", "--input", "--method", "-X", "--cache", "--preview", "-p"}, arg)
}

func quotaEndpoint(args []string) string {
	for i := 1; i < len(args); i++ {
		if !strings.HasPrefix(args[i], "-") {
			return args[i]
		}
		if quotaValueFlag(args[i]) {
			i++
		}
	}
	return ""
}

func quotaAPIResource(endpoint string) string {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "core"
	}
	path := strings.TrimPrefix(parsed.Path, "/")
	path = strings.TrimPrefix(path, "api/v3/")
	switch {
	case path == "graphql":
		return "graphql"
	case path == "search/code":
		return "code_search"
	case strings.HasPrefix(path, "search/"):
		return "search"
	default:
		return "core"
	}
}

func quotaCapture(args []string) bool {
	if len(args) == 0 || args[0] != "api" {
		return false
	}
	for _, arg := range args {
		if arg == "--paginate" || arg == "--jq" || arg == "-q" || arg == "--template" || arg == "--include" || arg == "-i" {
			return false
		}
	}
	return true
}

func readQuota(path string) quotaRecord {
	var record quotaRecord
	raw, err := os.ReadFile(path)
	if err == nil {
		_ = json.Unmarshal(raw, &record)
	}
	return record
}

// All publishers and clearers lock the same persistent sidecar. Never remove
// the lock file: unlinking it would allow two processes to lock different inodes.
func withQuotaLock(path string, update func()) {
	if path == "" || os.MkdirAll(filepath.Dir(path), 0o700) != nil {
		return
	}
	lock := flock.New(path+".lock", flock.SetPermissions(0o600))
	defer func() { _ = lock.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	acquired, err := lock.TryLockContext(ctx, 10*time.Millisecond)
	if err == nil && acquired {
		update()
	}
}

func saveQuota(path string, record quotaRecord) {
	withQuotaLock(path, func() {
		current := readQuota(path)
		if current.Until.After(record.Until) {
			record.Until = current.Until
		}
		record.Attempts = max(record.Attempts, current.Attempts)
		publishQuota(path, record)
	})
}

func clearQuota(path string, observed quotaRecord) {
	withQuotaLock(path, func() {
		current := readQuota(path)
		if current.Until.Equal(observed.Until) && current.Attempts == observed.Attempts && !quotaNowFunc().Before(current.Until) {
			_ = os.Remove(path)
		}
	})
}

func publishQuota(path string, record quotaRecord) {
	raw, err := json.Marshal(record)
	if err != nil {
		return
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".quota-*")
	if err != nil {
		return
	}
	defer func() { _ = os.Remove(temp.Name()) }()
	_, writeErr := temp.Write(raw)
	closeErr := temp.Close()
	if writeErr == nil && closeErr == nil {
		_ = os.Rename(temp.Name(), path)
	}
}

type quotaHeaders map[string][]string

func (h quotaHeaders) Get(name string) string {
	for key, values := range h {
		if strings.EqualFold(key, name) && len(values) != 0 {
			return values[0]
		}
	}
	return ""
}

// Parse gh's --include output, not an HTTP socket. Only quota metadata is
// retained; the JSON payload is returned byte-for-byte, including large bodies.
func splitQuotaResponse(raw []byte) ([]byte, quotaHeaders) {
	if !bytes.HasPrefix(raw, []byte("HTTP/")) {
		return raw, quotaHeaders{}
	}
	reader := bufio.NewReader(bytes.NewReader(raw))
	if _, err := reader.ReadString('\n'); err != nil {
		return raw, quotaHeaders{}
	}
	headers := quotaHeaders{}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return raw, quotaHeaders{}
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		key, value, found := strings.Cut(line, ":")
		if !found {
			return raw, quotaHeaders{}
		}
		key = strings.ToLower(key)
		switch key {
		case "x-ratelimit-remaining", "x-ratelimit-reset", "x-ratelimit-resource", "retry-after":
			headers[key] = append(headers[key], strings.TrimSpace(value))
		}
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		return raw, quotaHeaders{}
	}
	return body, headers
}

func quotaDeadline(headers quotaHeaders, body []byte, message string, now time.Time, attempts int) (time.Time, bool) {
	limited := quotaLimited(body, message) || headers.Get("Retry-After") != ""
	until := time.Time{}
	if headers.Get("X-Ratelimit-Remaining") == "0" {
		limited = true
		seconds, err := strconv.ParseInt(headers.Get("X-Ratelimit-Reset"), 10, 64)
		if err == nil {
			until = time.Unix(seconds, 0)
		}
	}
	if !limited {
		return time.Time{}, false
	}
	delay := min(time.Minute*time.Duration(1<<min(max(attempts, 0), 4)), 15*time.Minute)
	if value := headers.Get("Retry-After"); value != "" {
		delay = quotaRetryAfter(value, now, delay)
	}
	if until.After(now) {
		if headers.Get("Retry-After") != "" && now.Add(delay).After(until) {
			return now.Add(delay), true
		}
		return until, true
	}
	return now.Add(delay), true
}

func quotaRetryAfter(value string, now time.Time, fallback time.Duration) time.Duration {
	seconds, err := strconv.ParseInt(value, 10, 32)
	if err == nil && seconds >= 0 {
		return max(time.Second, time.Duration(seconds)*time.Second)
	}
	if deadline, err := time.Parse(time.RFC1123, value); err == nil {
		return max(time.Second, deadline.Sub(now))
	}
	return fallback
}

func quotaLimited(body []byte, message string) bool {
	var envelope struct {
		Errors []struct {
			Message string
			Type    string
		}
	}
	_ = json.Unmarshal(body, &envelope)
	for _, entry := range envelope.Errors {
		message += " " + entry.Message + " " + entry.Type
	}
	message = strings.ToLower(message)
	return strings.Contains(message, "rate limit") || strings.Contains(message, "rate_limited") || strings.Contains(message, "http 429")
}

func runGHQuota(inv ghInvocation) (bytes.Buffer, bytes.Buffer, error) {
	path := quotaPath(inv, quotaResource(inv.Args))
	record := readQuota(path)
	if quotaNowFunc().Before(record.Until) {
		err := &quotaError{Until: record.Until}
		return bytes.Buffer{}, *bytes.NewBufferString(err.Error()), err
	}
	capture := quotaCapture(inv.Args)
	if capture {
		inv.Args = append(append([]string(nil), inv.Args...), "--include")
	}
	stdout, stderr, err := quotaRawFunc(inv)
	headers := quotaHeaders{}
	if capture {
		body, parsed := splitQuotaResponse(stdout.Bytes())
		stdout, headers = *bytes.NewBuffer(body), parsed
	}
	path, record = quotaResponseScope(inv, headers, path, record)
	until, limited := quotaDeadline(headers, stdout.Bytes(), stderr.String(), quotaNowFunc(), record.Attempts)
	if limited {
		saveQuota(path, quotaRecord{Until: until, Attempts: min(record.Attempts+1, 4)})
		// A successful last request may consume the final point. Keep its data;
		// only explicit rate-limit failures replace the original error.
		if quotaLimited(stdout.Bytes(), stderr.String()) {
			err = &quotaError{Until: until}
			stderr = *bytes.NewBufferString(err.Error())
		}
	} else if err == nil && path != "" {
		clearQuota(path, record)
	}
	return stdout, stderr, err
}

func quotaResponseScope(inv ghInvocation, headers quotaHeaders, path string, record quotaRecord) (string, quotaRecord) {
	resource := headers.Get("X-Ratelimit-Resource")
	if resource == "" || resource == quotaResource(inv.Args) || !slices.Contains([]string{"core", "graphql", "search", "code_search"}, resource) {
		return path, record
	}
	path = quotaPath(inv, resource)
	return path, readQuota(path)
}
