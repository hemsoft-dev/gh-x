package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	ghconfig "github.com/cli/go-gh/v2/pkg/config"
)

// defaultGitHubHost is the public GitHub API host; publicGitHubSSHHost is its
// documented SSH-over-443 transport endpoint.
const (
	defaultGitHubHost           = "github.com"
	publicGitHubSSHHost         = "ssh.github.com"
	githubCommandTimeoutEnv     = "GH_X_GITHUB_TIMEOUT"
	defaultGitHubCommandTimeout = 30 * time.Second
	githubCommandWaitDelay      = time.Second
)

// ghInvocation describes one gh subprocess execution.
type ghInvocation struct {
	Context  context.Context
	Args     []string
	Stdin    []byte
	ExtraEnv []string
}

// ghTransportFunc is the lowest-level seam for executing gh; tests replace it
// to simulate command output and failures.
var ghTransportFunc = runGHCmd

// accountWarningWriter receives multi-account fallback notices. stderr keeps
// --json stdout machine-readable.
var accountWarningWriter io.Writer = os.Stderr

// runGHCmd executes the gh binary with inherited environment plus any extra
// environment entries. GH_PATH takes precedence, matching gh's own resolution.
// Its context always owns the child process and cancels it when the caller ends.
func runGHCmd(inv ghInvocation) (bytes.Buffer, bytes.Buffer, error) {
	if inv.Context == nil {
		inv.Context = context.Background()
	}
	return runGHQuota(inv)
}

func runGHCmdRaw(inv ghInvocation) (bytes.Buffer, bytes.Buffer, error) {
	ctx := inv.Context
	if ctx == nil {
		ctx = context.Background()
	}
	path := os.Getenv("GH_PATH")
	if path == "" {
		var lookErr error
		if path, lookErr = exec.LookPath("gh"); lookErr != nil {
			return bytes.Buffer{}, bytes.Buffer{}, fmt.Errorf("gh CLI not found in PATH")
		}
	}
	cmd := exec.CommandContext(ctx, path, inv.Args...)
	cmd.WaitDelay = githubCommandWaitDelay
	if len(inv.Stdin) > 0 {
		cmd.Stdin = bytes.NewReader(inv.Stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if len(inv.ExtraEnv) > 0 {
		cmd.Env = append(os.Environ(), inv.ExtraEnv...)
	}
	if err := cmd.Run(); err != nil {
		if contextErr := githubContextError(ctx.Err()); contextErr != nil {
			return stdout, stderr, contextErr
		}
		return stdout, stderr, err
	}
	return stdout, stderr, nil
}

func githubContextError(err error) error {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("github request timed out: %w", context.DeadlineExceeded)
	case errors.Is(err, context.Canceled):
		return fmt.Errorf("github request canceled: %w", context.Canceled)
	default:
		return nil
	}
}

func configuredTimeout(name string, fallback time.Duration) (time.Duration, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback, nil
	}
	value := strings.TrimSpace(raw)
	if value == "" {
		return 0, fmt.Errorf("%s must not contain only whitespace", name)
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("%s must be a positive Go duration", name)
	}
	return duration, nil
}

var (
	notifiedMu      sync.Mutex
	notifiedLogins  = map[string]bool{}
	accountProgress *statusProgress
)

// noteFallback prints the alternate-account notice once per login per process
// so multi-call commands do not repeat it.
func noteFallback(login, host string) {
	notifiedMu.Lock()
	defer notifiedMu.Unlock()
	if notifiedLogins[login] {
		return
	}
	notifiedLogins[login] = true
	text := fmt.Sprintf("[gh-x] note: retried as %s (%s) after an access failure\n", login, host)
	if accountProgress != nil {
		accountProgress.notice(accountWarningWriter, text)
		return
	}
	fmt.Fprint(accountWarningWriter, text)
}

// execGH runs a gh command and retries with another logged-in account's token
// when the active account cannot access the target repository. The retry
// authenticates against the same host the original command targeted.
func execGH(args ...string) (bytes.Buffer, bytes.Buffer, error) {
	timeout, err := configuredTimeout(githubCommandTimeoutEnv, defaultGitHubCommandTimeout)
	if err != nil {
		return bytes.Buffer{}, bytes.Buffer{}, err
	}
	ctx, cancel := context.WithTimeout(commandContext, timeout)
	defer cancel()
	return execGHContext(ctx, args...)
}

func execGHContext(ctx context.Context, args ...string) (bytes.Buffer, bytes.Buffer, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	stdout, stderr, err := ghTransportFunc(ghInvocation{Context: ctx, Args: args})
	if err == nil {
		return retryEmptySearchWithAccounts(ctx, args, stdout, stderr)
	}
	if contextErr := githubContextError(ctx.Err()); contextErr != nil {
		return stdout, stderr, contextErr
	}
	host, eligible := fallbackHost(ctx, args, stderr.String())
	if contextErr := githubContextError(ctx.Err()); contextErr != nil {
		return stdout, stderr, contextErr
	}
	if !eligible {
		return stdout, stderr, err
	}
	return retryGHWithAccounts(ctx, host, args, stdout, stderr, err)
}

// retryEmptySearchWithAccounts distinguishes a legitimate empty search from
// GitHub's successful empty response when the active account cannot see a
// private repository. The repository probe does not use fallback itself, so
// an access failure can retry the original search under another account.
func retryEmptySearchWithAccounts(ctx context.Context, args []string, stdout, stderr bytes.Buffer) (bytes.Buffer, bytes.Buffer, error) {
	if !isEmptySearchList(args, stdout.Bytes()) {
		return stdout, stderr, nil
	}
	probeArgs := repositoryAccessProbeArgs(args)
	_, probeStderr, probeErr := ghTransportFunc(ghInvocation{Context: ctx, Args: probeArgs})
	if probeErr == nil {
		return stdout, stderr, nil
	}
	if contextErr := githubContextError(ctx.Err()); contextErr != nil {
		return stdout, probeStderr, contextErr
	}
	host, eligible := fallbackHost(ctx, args, probeStderr.String())
	if !eligible {
		return stdout, stderr, nil
	}
	return retryGHWithAccounts(ctx, host, args, stdout, probeStderr, probeErr)
}

func isEmptySearchList(args []string, output []byte) bool {
	if len(args) < 2 || (args[0] != "pr" && args[0] != "issue") || args[1] != "list" {
		return false
	}
	hasSearch := false
	for _, arg := range args[2:] {
		if arg == "--search" || strings.HasPrefix(arg, "--search=") {
			hasSearch = true
			break
		}
	}
	return hasSearch && bytes.Equal(bytes.TrimSpace(output), []byte("[]"))
}

func repositoryAccessProbeArgs(args []string) []string {
	probeArgs := []string{"repo", "view"}
	for i := 2; i < len(args); i++ {
		switch {
		case (args[i] == "--repo" || args[i] == "-R") && i+1 < len(args):
			probeArgs = append(probeArgs, args[i+1])
			i++
		case strings.HasPrefix(args[i], "--repo="):
			probeArgs = append(probeArgs, strings.TrimPrefix(args[i], "--repo="))
		case strings.HasPrefix(args[i], "-R="):
			probeArgs = append(probeArgs, strings.TrimPrefix(args[i], "-R="))
		}
	}
	return append(probeArgs, "--json", "nameWithOwner")
}

func retryGHWithAccounts(ctx context.Context, host string, args []string, originalOut, originalErrs bytes.Buffer, originalErr error) (bytes.Buffer, bytes.Buffer, error) {
	logins := fallbackAccountLoginsFor(ctx, host)
	if contextErr := githubContextError(ctx.Err()); contextErr != nil {
		return originalOut, originalErrs, contextErr
	}
	for _, login := range logins {
		token, ok := accountTokenFunc(ctx, login, host)
		if contextErr := githubContextError(ctx.Err()); contextErr != nil {
			return originalOut, originalErrs, contextErr
		}
		if !ok || token == "" {
			continue
		}
		retry := ghInvocation{Context: ctx, Args: args, ExtraEnv: credentialEnvFor(host, token)}
		retryOut, retryErrs, retryErr := ghTransportFunc(retry)
		if retryErr == nil && isEmptySearchList(args, retryOut.Bytes()) {
			probe := ghInvocation{Context: ctx, Args: repositoryAccessProbeArgs(args), ExtraEnv: retry.ExtraEnv}
			_, _, retryErr = ghTransportFunc(probe)
		}
		if retryErr == nil {
			noteFallback(login, host)
			return retryOut, retryErrs, nil
		}
		if contextErr := githubContextError(ctx.Err()); contextErr != nil {
			return retryOut, retryErrs, contextErr
		}
	}
	return originalOut, originalErrs, originalErr
}

// execGHActive runs a gh command as the active account with no fallback.
// Identity-scoped flows use it so a retry can never switch the account that a
// query's embedded login refers to, on any host.
func execGHActive(args ...string) (bytes.Buffer, bytes.Buffer, error) {
	return execGHActiveInvocation(ghInvocation{Args: args})
}

func execGHActiveInvocation(invocation ghInvocation) (bytes.Buffer, bytes.Buffer, error) {
	timeout, err := configuredTimeout(githubCommandTimeoutEnv, defaultGitHubCommandTimeout)
	if err != nil {
		return bytes.Buffer{}, bytes.Buffer{}, err
	}
	ctx, cancel := context.WithTimeout(commandContext, timeout)
	defer cancel()
	invocation.Context = ctx
	return ghTransportFunc(invocation)
}

func execGHActiveContext(ctx context.Context, args ...string) (bytes.Buffer, bytes.Buffer, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	return ghTransportFunc(ghInvocation{Context: ctx, Args: args})
}

// targetHost resolves which GitHub host a command targets, following gh's own
// precedence: an explicit --hostname, an explicit HOST/OWNER/REPO value on
// --repo/-R, the GH_REPO environment variable, the current repository's git
// remote after SSH alias resolution, GH_HOST, then github.com.
func targetHost(args []string) string {
	return targetHostContext(context.Background(), args)
}

func targetHostContext(ctx context.Context, args []string) string {
	if host := hostFromHostnameArgs(args); host != "" {
		return host
	}
	if host := hostFromRepoArgs(args); host != "" {
		return host
	}
	if host := hostFromRepoValue(os.Getenv("GH_REPO")); host != "" {
		return host
	}
	if host := cachedRemoteTargetHost(ctx); host != "" {
		return host
	}
	if host := strings.TrimSpace(os.Getenv("GH_HOST")); host != "" {
		return strings.ToLower(host)
	}
	return defaultGitHubHost
}

func hostFromHostnameArgs(args []string) string {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--hostname" {
			if host := normalizeRemoteHost(args[i+1]); host != "" {
				return host
			}
		}
	}
	return ""
}

func hostFromRepoArgs(args []string) string {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--repo" || args[i] == "-R" {
			if host := hostFromRepoValue(args[i+1]); host != "" {
				return host
			}
		}
	}
	return ""
}

// gitRemoteURLFunc reads the current repository's origin URL so commands run
// inside an Enterprise Server checkout target that host by default; tests
// replace it.
var gitRemoteURLFunc = defaultGitRemoteURL

var (
	remoteMu         sync.Mutex
	cachedRemoteHost string
	remoteResolved   bool
)

// cachedRemoteTargetHost memoizes both the git remote probe and SSH alias
// resolution so commands in one process do not repeatedly invoke ssh -G.
func cachedRemoteTargetHost(ctx context.Context) string {
	remoteMu.Lock()
	defer remoteMu.Unlock()
	if remoteResolved {
		return cachedRemoteHost
	}
	remoteURL := gitRemoteURLFunc(ctx)
	if ctx.Err() != nil {
		return ""
	}
	resolvedHost := hostFromRemoteURLContext(ctx, remoteURL)
	if ctx.Err() != nil {
		return ""
	}
	cachedRemoteHost = resolvedHost
	remoteResolved = true
	return cachedRemoteHost
}

// defaultGitRemoteURL reads the origin remote of the current repository.
func defaultGitRemoteURL(ctx context.Context) string {
	cmd := exec.CommandContext(ctx, "git", "config", "--get", "remote.origin.url")
	cmd.WaitDelay = githubCommandWaitDelay
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

var (
	remoteSchemeHost = regexp.MustCompile(`^([a-zA-Z][a-zA-Z0-9+.-]*)://(?:[^@/]+@)?([^/:?#]+)`)
	remoteScpHost    = regexp.MustCompile(`^(?:[^/@]+@)?([^:/]+):`)
	remotePlainHost  = regexp.MustCompile(`^([^/:@]+\.[^/:@]+)/`)
)

const (
	sshConfigTimeout   = 2 * time.Second
	sshConfigWaitDelay = 100 * time.Millisecond
)

// sshConfigHostFunc resolves an SSH destination through the user's config.
// Tests replace it so host routing stays deterministic and network-free.
var sshConfigHostFunc = defaultSSHConfigHost

// plausibleRemoteHost rejects unresolved SSH aliases such as "workserver",
// which have no dotted hostname, so a local alias is never treated as a GitHub
// hostname during remote-based inference.
func plausibleRemoteHost(host string) bool {
	return strings.Contains(host, ".") &&
		!strings.HasPrefix(host, ".") &&
		!strings.HasSuffix(host, ".")
}

// normalizeRemoteHost lowercases and strips the DNS root dot from absolute
// names like "ghe.example.com.". Malformed names that still end in a dot
// afterwards stay invalid on purpose.
func normalizeRemoteHost(host string) string {
	return strings.ToLower(strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), "."))
}

// hostFromRepoValue extracts the host from HOST/OWNER/REPO values. Plain
// OWNER/REPO values return "" because their host comes from elsewhere.
func hostFromRepoValue(value string) string {
	parts := strings.Split(strings.Trim(strings.TrimSpace(value), "/"), "/")
	if len(parts) < 3 || parts[0] == "" {
		return ""
	}
	return strings.ToLower(parts[0])
}

// hostFromRemoteURL extracts a hostname from https, ssh, scp-style (with or
// without an ssh user), or bare host/path git remote URLs. SSH destinations
// are resolved through ssh -G before they become API hosts; unrecognized
// values return "" so inference degrades to the next signal.
func hostFromRemoteURLContext(ctx context.Context, raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	if matches := remoteSchemeHost.FindStringSubmatch(value); len(matches) == 3 {
		host := normalizeRemoteHost(matches[2])
		if isSSHRemoteScheme(matches[1]) {
			host = configuredSSHHostContext(ctx, matches[2])
		}
		if plausibleRemoteHost(host) {
			return host
		}
		return ""
	}
	if matches := remoteScpHost.FindStringSubmatch(value); len(matches) == 2 {
		host := configuredSSHHostContext(ctx, matches[1])
		if plausibleRemoteHost(host) {
			return host
		}
		return ""
	}
	if matches := remotePlainHost.FindStringSubmatch(value); len(matches) == 2 {
		host := normalizeRemoteHost(matches[1])
		if plausibleRemoteHost(host) {
			return host
		}
	}
	return ""
}

func isSSHRemoteScheme(scheme string) bool {
	normalized := strings.ToLower(scheme)
	return normalized == "ssh" || normalized == "ssh+git" || strings.HasSuffix(normalized, "+ssh")
}

// ghConfigReadFunc reads gh's local configuration without testing account
// credentials over the network. Tests replace it to avoid local config access.
var ghConfigReadFunc = func() (*ghconfig.Config, error) { return ghconfig.Read(nil) }

var configuredGitHubHostsFunc = configuredGitHubHosts

func configuredGitHubHosts() []string {
	cfg, err := ghConfigReadFunc()
	if err != nil {
		return nil
	}
	hosts, err := cfg.Keys([]string{"hosts"})
	if err != nil {
		return nil
	}
	return hosts
}

// knownGitHubHostFunc identifies API hosts from explicit environment or gh's
// local configuration. Tests replace it so host-routing cases stay isolated.
var knownGitHubHostFunc = knownGitHubHost

func knownGitHubHost(host string) bool {
	host = normalizeRemoteHost(host)
	if host == defaultGitHubHost || host == normalizeRemoteHost(os.Getenv("GH_HOST")) {
		return host != ""
	}
	for _, configured := range configuredGitHubHostsFunc() {
		if host == normalizeRemoteHost(configured) {
			return true
		}
	}
	return false
}

// configuredSSHHost preserves known API hosts and accepts a configured
// HostName only when gh also knows that destination as an API host. This keeps
// SSH transport endpoints such as ssh.github.com out of gh --hostname. A
// missing binary, invalid config, timeout, or unknown destination falls back
// to the original remote host.
func configuredSSHHostContext(ctx context.Context, host string) string {
	normalizedHost := normalizeRemoteHost(host)
	if normalizedHost == "" || strings.HasPrefix(normalizedHost, ".") || strings.HasSuffix(normalizedHost, ".") || knownGitHubHostFunc(normalizedHost) {
		return normalizedHost
	}
	resolved := normalizeRemoteHost(sshConfigHostFunc(ctx, host))
	if resolved == publicGitHubSSHHost {
		return defaultGitHubHost
	}
	if plausibleRemoteHost(resolved) && knownGitHubHostFunc(resolved) {
		return resolved
	}
	return normalizedHost
}

func defaultSSHConfigHost(ctx context.Context, host string) string {
	ctx, cancel := context.WithTimeout(ctx, sshConfigTimeout)
	defer cancel()
	out, err := newSSHConfigCommand(ctx, host).Output()
	if err != nil {
		return ""
	}
	return parseSSHConfigHost(out)
}

func newSSHConfigCommand(ctx context.Context, host string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "ssh", "-G", "--", host)
	cmd.WaitDelay = sshConfigWaitDelay
	return cmd
}

func parseSSHConfigHost(output []byte) string {
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.EqualFold(fields[0], "hostname") {
			return normalizeRemoteHost(fields[1])
		}
	}
	return ""
}

// credentialEnvFor returns the environment entries that authenticate one gh
// subprocess against host with token. github.com uses GH_TOKEN; Enterprise
// Server hosts use both enterprise variable spellings so either gh build
// honors them.
func credentialEnvFor(host, token string) []string {
	if host == defaultGitHubHost {
		return []string{"GH_TOKEN=" + token}
	}
	return []string{
		"GH_ENTERPRISE_TOKEN=" + token,
		"GITHUB_ENTERPRISE_TOKEN=" + token,
	}
}

// fallbackEligible reports whether a failure should trigger an alternate
// account attempt. Auth-plane commands never fall back, explicit token
// environment overrides for the target host are respected, and only
// access-shaped errors qualify.
func fallbackHost(ctx context.Context, args []string, stderr string) (string, bool) {
	if len(args) == 0 || args[0] == "auth" || !isAccessError(stderr) {
		return "", false
	}
	host := targetHostContext(ctx, args)
	return host, ctx.Err() == nil && !explicitTokenSet(host)
}

// explicitTokenSet reports whether the caller pinned credentials for host via
// the environment; fallback must respect that choice instead of second-guessing it.
func explicitTokenSet(host string) bool {
	if host == defaultGitHubHost {
		return os.Getenv("GH_TOKEN") != "" || os.Getenv("GITHUB_TOKEN") != ""
	}
	return os.Getenv("GH_ENTERPRISE_TOKEN") != "" || os.Getenv("GITHUB_ENTERPRISE_TOKEN") != ""
}

var accessErrorMarkers = []string{
	"http 403",
	"http 404",
	"(403)",
	"(404)",
	"forbidden",
	"not accessible",
	"resource protected",
	"bad credentials",
	"not found",
	"not_found",
	"could not resolve",
}

// isAccessError matches failure text that plausibly indicates the active
// account cannot see the target rather than a broken request.
func isAccessError(stderr string) bool {
	lowered := strings.ToLower(stderr)
	for _, marker := range accessErrorMarkers {
		if strings.Contains(lowered, marker) {
			return true
		}
	}
	return false
}

// ghAccount is one authenticated GitHub CLI identity on some host.
type ghAccount struct {
	Login  string
	Active bool
}

// listAccountsFunc resolves an account list for one host; tests replace it.
var listAccountsFunc = listAccounts

// accountTokenFunc resolves one account's token for a host; tests replace it.
var accountTokenFunc = defaultAccountToken

var (
	accountsMu     sync.Mutex
	cachedAccounts = map[string][]ghAccount{}
	cachedTokens   = map[string]string{}
)

// listAccounts discovers logged-in accounts once per process: one auth status
// probe is parsed and cached for every reported host. An empty result is
// cached too so a broken auth state cannot cause repeated probing.
func listAccounts(ctx context.Context, host string) []ghAccount {
	if ctx == nil {
		ctx = context.Background()
	}
	host = normalizeRemoteHost(host)
	accountsMu.Lock()
	defer accountsMu.Unlock()
	if cached, ok := cachedAccounts[host]; ok {
		return cached
	}
	stdout, _, err := ghTransportFunc(ghInvocation{Context: ctx, Args: []string{"auth", "status", "--json", "hosts"}})
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		cachedAccounts[host] = []ghAccount{}
		return cachedAccounts[host]
	}
	for parsedHost, parsedAccounts := range parseAuthStatusJSON(stdout.Bytes()) {
		if len(parsedAccounts) > 0 {
			cachedAccounts[parsedHost] = parsedAccounts
		}
	}
	if _, probed := cachedAccounts[host]; !probed {
		cachedAccounts[host] = []ghAccount{}
	}
	return cachedAccounts[host]
}

// parseAuthStatusJSON reads `gh auth status --json hosts` into per-host
// account lists keyed by normalized hostname. Only successful logins apply;
// expired or pending entries are skipped because their tokens cannot
// authenticate.
func parseAuthStatusJSON(data []byte) map[string][]ghAccount {
	var payload struct {
		Hosts map[string][]struct {
			Login  string `json:"login"`
			Active bool   `json:"active"`
			State  string `json:"state"`
		} `json:"hosts"`
	}
	byHost := map[string][]ghAccount{}
	if err := json.Unmarshal(data, &payload); err != nil {
		return byHost
	}
	for host, entries := range payload.Hosts {
		host = normalizeRemoteHost(host)
		if host == "" {
			continue
		}
		for _, entry := range entries {
			if entry.Login == "" {
				continue
			}
			if entry.State != "" && entry.State != "success" {
				continue
			}
			byHost[host] = append(byHost[host], ghAccount{Login: entry.Login, Active: entry.Active})
		}
	}
	return byHost
}

// fallbackAccountLoginsFor lists non-active accounts on host in discovery order.
func fallbackAccountLoginsFor(ctx context.Context, host string) []string {
	logins := []string{}
	for _, account := range listAccountsFunc(ctx, host) {
		if !account.Active {
			logins = append(logins, account.Login)
		}
	}
	return logins
}

// defaultAccountToken resolves one account's token on host via the gh CLI and
// caches it for this invocation of the extension. Failures are not cached.
func defaultAccountToken(ctx context.Context, login, host string) (string, bool) {
	accountsMu.Lock()
	defer accountsMu.Unlock()
	cacheKey := login + "@" + host
	if token, ok := cachedTokens[cacheKey]; ok {
		return token, true
	}
	stdout, _, err := ghTransportFunc(ghInvocation{Context: ctx, Args: []string{
		"auth", "token", "--user", login, "--hostname", host,
	}})
	if err != nil {
		return "", false
	}
	token := strings.TrimSpace(stdout.String())
	if token == "" {
		return "", false
	}
	cachedTokens[cacheKey] = token
	return token, true
}
