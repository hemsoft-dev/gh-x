package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
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
	auth, err := statusAuthContext()
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
	switch args[0] {
	case "pr", "issue", "repo":
		return "graphql"
	case "run":
		return "core"
	case "api":
		if containsArg(args, "graphql") {
			return "graphql"
		}
		return "core"
	default:
		return ""
	}
}

func containsArg(args []string, value string) bool {
	for _, arg := range args {
		if arg == value {
			return true
		}
	}
	return false
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

func saveQuota(path string, record quotaRecord) {
	if path == "" {
		return
	}
	if os.MkdirAll(filepath.Dir(path), 0o700) != nil {
		return
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return
	}
	// Atomic publication prevents another process from reading partial JSON.
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
		case "x-ratelimit-remaining", "x-ratelimit-reset", "retry-after":
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
	until, limited := quotaDeadline(headers, stdout.Bytes(), stderr.String(), quotaNowFunc(), record.Attempts)
	if limited {
		saveQuota(path, quotaRecord{Until: until, Attempts: min(record.Attempts+1, 4)})
		// A successful last request may consume the final point. Keep its data;
		// only an actual API failure becomes an error for this request.
		if err != nil || quotaLimited(stdout.Bytes(), stderr.String()) {
			err = &quotaError{Until: until}
			stderr = *bytes.NewBufferString(err.Error())
		}
	} else if err == nil && path != "" {
		_ = os.Remove(path)
	}
	return stdout, stderr, err
}
