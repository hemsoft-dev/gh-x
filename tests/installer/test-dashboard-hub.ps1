# Offline integration tests of the real installer. No Pester, Tailscale daemon or scheduler access.
param([string]$InstallerPath, [string]$InstallerPowerShell, [string]$CaseName)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$repository = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '../..'))
if (-not $InstallerPath) { $InstallerPath = Join-Path $repository 'install-dashboard-hub.ps1' }
$fixture = Join-Path ([IO.Path]::GetTempPath()) "gh-x installer $([guid]::NewGuid().ToString('N'))"
New-Item -ItemType Directory -Path $fixture | Out-Null
$failures = [Collections.Generic.List[string]]::new()
$passed = 0

function Assert-Test {
    param([bool]$Condition, [string]$Message)
    if (-not $Condition) { throw $Message }
}

try {
    # A real native executable provides actual nonzero exits on Windows and Linux.
    $native = Join-Path $fixture 'tailscale.exe'
    $source = Join-Path $fixture 'native.go'
    @'
package main
import ("encoding/json"; "fmt"; "os"; "strings"; "time")
type reply struct { Output string; Error string; Exit int; Delay int }
type config struct { Replies map[string]reply; DNS string; IP string; Serve map[string]interface{} }
func main() {
    root := os.Getenv("HUB_INSTALL_FIXTURE")
    data, err := os.ReadFile(root + "/native.json"); if err != nil { panic(err) }
    var c config; if err = json.Unmarshal(data, &c); err != nil { panic(err) }
    args := os.Args[1:]; key := "unknown"
    if len(args) > 0 && args[0] == "status" { key = "status" }
    if len(args) > 0 && args[0] == "ip" { key = "ip" }
    if len(args) > 1 && args[0] == "serve" && args[1] == "status" { key = "serve-status" }
    if len(args) > 1 && args[0] == "serve" && args[1] == "--bg" {
        key = "https"
        for _, a := range args { if a == "--tcp=80" { key = "tcp" } }
    }
    log, err := os.OpenFile(root + "/native-calls.jsonl", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600); if err != nil { panic(err) }
    if err = json.NewEncoder(log).Encode(args); err != nil { panic(err) }; if err = log.Close(); err != nil { panic(err) }
    r, ok := c.Replies[key]; if !ok { fmt.Fprintln(os.Stderr, "unknown native operation"); os.Exit(99) }
    if r.Error != "" { fmt.Fprintln(os.Stderr, r.Error) }
    if r.Delay != 0 { time.Sleep(time.Duration(r.Delay) * time.Millisecond) }
    if r.Exit != 0 { fmt.Fprintln(os.Stderr, "fixture native failure: " + key); os.Exit(r.Exit) }
    if key == "https" || key == "tcp" {
        // Model the observable effect of scoped Serve writes, including destructive reset.
        for _, a := range args { if a == "--reset" { c.Serve = map[string]interface{}{} } }
        if key == "https" {
            path := ""; for i, a := range args { if a == "--set-path" && i+1 < len(args) { path = args[i+1] } }
            if path == "" { fmt.Fprintln(os.Stderr, "missing scoped path"); os.Exit(98) }
            c.Serve[path] = args[len(args)-1]
        } else { c.Serve["tcp:80"] = strings.TrimPrefix(args[len(args)-1], "tcp://") }
        saved, err := json.Marshal(c.Serve); if err != nil { panic(err) }
        if err = os.WriteFile(root + "/serve-after.json", saved, 0600); err != nil { panic(err) }
        // Carry mutations into the next native invocation.
        saved, err = json.Marshal(c); if err != nil { panic(err) }
        if err = os.WriteFile(root + "/native.json", saved, 0600); err != nil { panic(err) }
    }
    fmt.Print(r.Output)
}
'@ | Set-Content $source -Encoding utf8
    & go build -o $native $source
    if ($LASTEXITCODE -ne 0) { throw 'Cannot build native command double' }
    $powerShell = if ($InstallerPowerShell) { $InstallerPowerShell } else { Join-Path $PSHOME $(if ($IsWindows) { 'pwsh.exe' } else { 'pwsh' }) }
    $cases = @(
        @{ Name = 'both Serve calls fail'; Reply = 'https'; Exit = 42; Error = 'HTTPS Serve.*42'; Calls = 4; Registered = 1 },
        @{ Name = 'TCP failure after HTTPS'; Reply = 'tcp'; Exit = 43; Error = 'TCP Serve.*43'; Calls = 5; Registered = 1; Https = $true },
        @{ Name = 'address native failure'; Reply = 'ip'; Exit = 44; Error = 'IPv4 address lookup.*44'; Calls = 2 },
        @{ Name = 'device status native failure'; Reply = 'status'; Exit = 45; Error = 'device status.*45'; Calls = 1 },
        @{ Name = 'Serve config native failure'; Reply = 'serve-status'; Exit = 46; Error = 'Serve configuration.*46'; Calls = 3 },
        @{ Name = 'invalid address'; IP = 'not-an-ip'; Error = 'IPv4'; Calls = 2 },
        @{ Name = 'empty address'; IP = ''; Error = 'IPv4'; Calls = 2 },
        @{ Name = 'IPv6 instead of IPv4'; IP = 'fd7a:115c:a1e0::1'; Error = 'IPv4'; Calls = 2 },
        @{ Name = 'different device address'; IP = '100.100.100.101'; Error = 'IPv4'; Calls = 2 },
        @{ Name = 'multiple address lines'; IP = "100.100.100.100`n100.100.100.101"; Error = 'IPv4'; Calls = 2 },
        @{ Name = 'invalid DNS'; DNS = 'wrong/path.ts.net'; Error = 'DNS'; Calls = 1 },
        @{ Name = 'empty DNS'; DNS = ''; Error = 'DNS'; Calls = 1 },
        @{ Name = 'bad DNS label'; DNS = '-wrong.tailfixture.ts.net'; Error = 'DNS'; Calls = 1 },
        @{ Name = 'malformed status'; Reply = 'status'; Output = '{'; Error = 'device status'; Calls = 1 },
        @{ Name = 'malformed Serve config'; Reply = 'serve-status'; Output = '{'; Error = 'Serve configuration'; Calls = 3 },
        @{ Name = 'human task same basename'; Existing = 'other-path'; Error = 'Scheduled task'; Calls = 0 },
        @{ Name = 'human task other principal'; Existing = 'other-user'; Error = 'Scheduled task'; Calls = 0 },
        @{ Name = 'human task extra action'; Existing = 'extra-action'; Error = 'Scheduled task'; Calls = 0 },
        @{ Name = 'human task Command text'; Existing = 'command-text'; Error = 'Scheduled task'; Calls = 0 },
        @{ Name = 'human task encoded command'; Existing = 'encoded-command'; Error = 'Scheduled task'; Calls = 0 },
        @{ Name = 'disabled owned task stays contained'; Existing = 'owned-disabled'; Error = 'disabled.*nothing was changed'; Calls = 0 },
        @{ Name = 'human HTTPS mount'; Collision = 'https'; Error = 'HTTPS.*already'; Calls = 3 },
        @{ Name = 'HTTPS app capabilities'; Collision = 'https-caps'; Error = 'HTTPS.*already'; Calls = 3 },
        @{ Name = 'false foreground Funnel'; Collision = 'foreground-funnel-false'; Success = $true; Https = $true; Calls = 5; Registered = 1 },
        @{ Name = 'unknown foreground Funnel value'; Collision = 'foreground-funnel-invalid'; Error = 'Serve configuration'; Calls = 3 },
        @{ Name = 'human TCP target'; Collision = 'tcp'; Error = 'TCP.*already'; Calls = 3 },
        @{ Name = 'TCP TLS termination'; Collision = 'tcp-tls'; Error = 'TCP.*already'; Calls = 3 },
        @{ Name = 'TCP proxy protocol'; Collision = 'tcp-proxy'; Error = 'TCP.*already'; Calls = 3 },
        @{ Name = 'foreground port 443'; Collision = 'foreground-443'; Error = 'foreground Serve'; Calls = 3 },
        @{ Name = 'foreground port 80'; Collision = 'foreground-80'; Error = 'foreground Serve'; Calls = 3 },
        @{ Name = 'foreground web handler'; Collision = 'foreground-web'; Error = 'foreground Serve'; Calls = 3 },
        @{ Name = 'unknown foreground shape'; Collision = 'foreground-shape'; Error = 'Serve configuration'; Calls = 3 },
        @{ Name = 'human port 443'; Collision = '443'; Error = '443.*already'; Calls = 3 },
        @{ Name = 'public Funnel port'; Collision = 'funnel'; Error = '443.*already public'; Calls = 3 },
        @{ Name = 'unknown Funnel shape'; Collision = 'funnel-shape'; Error = 'Serve configuration'; Calls = 3 },
        @{ Name = 'unknown Funnel value'; Collision = 'funnel-value'; Error = 'Serve configuration'; Calls = 3 },
        @{ Name = 'UTF8 native error'; Reply = 'https'; Exit = 47; NativeError = ([string][char]0x00E9 + 'chec ' + [char]0x03BB); Error = 'HTTPS Serve.*47'; Calls = 4; Registered = 1 },
        @{ Name = 'timeout native diagnostic'; Reply = 'https'; Delay = 31000; NativeError = 'fixture pre-timeout diagnostic'; Error = 'HTTPS Serve.*timed out.*completion is unverified'; Calls = 4; Registered = 1 },
        @{ Name = 'success first host'; Success = $true; Calls = 5; Registered = 1; Https = $true },
        @{ Name = 'success second host'; DNS = 'second-device.tailfixture.ts.net.'; Success = $true; Calls = 5; Registered = 1; Https = $true },
        @{ Name = 'healthy hub is not started again'; Healthy = $true; Success = $true; Calls = 5; Registered = 1; Https = $true },
        @{ Name = 'noninteractive task start failure'; StartFailure = $true; Error = 'fixture task start failure'; Calls = 3; Registered = 1 },
        @{ Name = 'same owned task and routes'; Existing = 'owned'; Collision = 'owned'; Success = $true; Calls = 5; Registered = 1; Https = $true }
    )
    if ($CaseName) {
        $cases = @($cases | Where-Object Name -EQ $CaseName)
        if (-not $cases.Count) { throw "Unknown case: $CaseName" }
    }
    foreach ($case in $cases) {
        $folder = Join-Path $fixture ([guid]::NewGuid().ToString('N'))
        New-Item -ItemType Directory -Path $folder | Out-Null
        Copy-Item $InstallerPath (Join-Path $folder 'install-dashboard-hub.ps1')
        @'
param([int]$Port, [switch]$NoBrowser, [switch]$Prepare)
if (-not $NoBrowser -or $Port -ne 4765) { throw 'Unexpected launcher arguments' }
if ($Prepare) { 'prepared' | Set-Content (Join-Path $PSScriptRoot 'prepare.log'); return }
throw 'Installer must not launch the hub in its interactive session'
'@ | Set-Content (Join-Path $folder 'run-dashboard-hub.ps1') -Encoding utf8
        $dns = if ($case.ContainsKey('DNS')) { $case.DNS } else { 'first-device.tailfixture.ts.net' }
        $ip = if ($case.ContainsKey('IP')) { $case.IP } else { '100.100.100.100' }
        $serve = @{ TCP = @{ '443' = @{ HTTPS = $true } }; Web = @{} }
        $hostKey = $dns.TrimEnd('.') + ':443'
        $serve.Web[$hostKey] = @{ Handlers = @{ '/unrelated' = @{ Proxy = 'http://127.0.0.1:9001' } } }
        if ($case.ContainsKey('Collision')) {
            switch ($case.Collision) {
                'https' { $serve.Web[$hostKey].Handlers['/dashboards'] = @{ Proxy = 'http://127.0.0.1:9002' } }
                'https-caps' { $serve.Web[$hostKey].Handlers['/dashboards'] = @{ Proxy = 'http://127.0.0.1:4765'; AcceptAppCaps = @('fixture.example/read') } }
                'foreground-funnel-false' { $serve['Foreground'] = @{ session = @{ AllowFunnel = @{ $hostKey = $false } } } }
                'foreground-funnel-invalid' { $serve['Foreground'] = @{ session = @{ AllowFunnel = @{ $hostKey = 'unknown' } } } }
                'tcp' { $serve.TCP['80'] = @{ TCPForward = '127.0.0.1:9003' } }
                'tcp-tls' { $serve.TCP['80'] = @{ TCPForward = '127.0.0.1:4765'; TerminateTLS = 'human.tailfixture.ts.net' } }
                'tcp-proxy' { $serve.TCP['80'] = @{ TCPForward = '127.0.0.1:4765'; ProxyProtocol = 1 } }
                'foreground-443' { $serve['Foreground'] = @{ session = @{ TCP = @{ '443' = @{ HTTPS = $true } } } } }
                'foreground-80' { $serve['Foreground'] = @{ session = @{ TCP = @{ '80' = @{ TCPForward = '127.0.0.1:9005' } } } } }
                'foreground-web' { $serve['Foreground'] = @{ session = @{ Web = @{ $hostKey = @{ Handlers = @{ '/' = @{ Proxy = 'http://127.0.0.1:9006' } } } } } } }
                'foreground-shape' { $serve['Foreground'] = 'unknown' }
                '443' { $serve.TCP['443'] = @{ TCPForward = '127.0.0.1:9004' } }
                'funnel' { $serve['AllowFunnel'] = @{ $hostKey = $true } }
                'funnel-shape' { $serve['AllowFunnel'] = 'unknown' }
                'funnel-value' { $serve['AllowFunnel'] = @{ $hostKey = 'unknown' } }
                'owned' {
                    $serve.Web[$hostKey].Handlers['/dashboards'] = @{ Proxy = 'http://127.0.0.1:4765' }
                    $serve.TCP['80'] = @{ TCPForward = '127.0.0.1:4765' }
                }
            }
        }
        $config = @{ Replies = @{
            status = @{ Output = (@{ Self = @{ DNSName = $dns; TailscaleIPs = @('100.100.100.100') } } | ConvertTo-Json -Depth 8 -Compress); Exit = 0 }
            ip = @{ Output = $ip; Exit = 0 }
            'serve-status' = @{ Output = ($serve | ConvertTo-Json -Depth 8 -Compress); Exit = 0 }
            https = @{ Output = 'HTTPS fixture configured'; Exit = 0 }
            tcp = @{ Output = 'TCP fixture configured'; Exit = 0 }
        }; Serve = @{ '/unrelated' = 'http://127.0.0.1:9001' }; DNS = $dns; IP = $ip }
        if ($case.ContainsKey('Reply')) {
            if ($case.ContainsKey('Exit')) { $config.Replies[$case.Reply].Exit = $case.Exit }
            if ($case.ContainsKey('Output')) { $config.Replies[$case.Reply].Output = $case.Output }
            if ($case.ContainsKey('NativeError')) { $config.Replies[$case.Reply]['Error'] = $case.NativeError }
            if ($case.ContainsKey('Delay')) { $config.Replies[$case.Reply]['Delay'] = $case.Delay }
        }
        if ($case.Name -eq 'both Serve calls fail') { $config.Replies.tcp.Exit = 42 }
        $config | ConvertTo-Json -Depth 10 | Set-Content (Join-Path $folder 'native.json') -Encoding utf8
        $existing = $null
        if ($case.ContainsKey('Existing')) {
            $path = if ($case.Existing -eq 'other-path') { Join-Path $fixture 'human/run-dashboard-hub.ps1' } else { Join-Path $folder 'run-dashboard-hub.ps1' }
            $user = if ($case.Existing -eq 'other-user') { 'FIXTUREDOMAIN\human' } else { 'FIXTUREDOMAIN\fixture-user' }
            $arguments = "-NoProfile -File `"$path`" -Port 4765 -NoBrowser"
            if ($case.Existing -eq 'command-text') { $arguments = "-Command Write-Output 'placeholder -File `"$path`" -Port 4765 -NoBrowser'" }
            if ($case.Existing -eq 'encoded-command') { $arguments = "-EncodedCommand ZQBjAGgAbwA= -File `"$path`"" }
            $action = @{ Execute = $powerShell; Arguments = $arguments }
            $existing = @{ Actions = @($action); Principal = @{ UserId = $user }; Settings = @{ Enabled = ($case.Existing -ne 'owned-disabled') }; State = $(if ($case.Existing -eq 'owned-disabled') { 'Disabled' } else { 'Ready' }) }
            if ($case.Existing -eq 'extra-action') { $existing.Actions += @{ Execute = 'human.exe'; Arguments = '' } }
        }
        @{ Native = $native; PowerShell = $powerShell; ExistingTask = $existing; Healthy = ($case.ContainsKey('Healthy') -and $case.Healthy); StartFailure = ($case.ContainsKey('StartFailure') -and $case.StartFailure) } | ConvertTo-Json -Depth 8 | Set-Content (Join-Path $folder 'wrapper.json') -Encoding utf8
        @'
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$PSNativeCommandUseErrorActionPreference = $false
[Console]::OutputEncoding = [Text.Encoding]::GetEncoding(28591)
$env:HUB_INSTALL_FIXTURE = $PSScriptRoot
$env:USERDOMAIN = 'FIXTUREDOMAIN'
$env:USERNAME = 'fixture-user'
$global:fixtureConfig = Get-Content (Join-Path $PSScriptRoot 'wrapper.json') -Raw | ConvertFrom-Json
function Get-Command {
    [CmdletBinding()] param([string]$Name)
    switch ($Name) {
        'powershell.exe' { return [pscustomobject]@{ Path = $global:fixtureConfig.PowerShell } }
        'tailscale.exe' { return [pscustomobject]@{ Path = $global:fixtureConfig.Native } }
        default { throw "Unmocked command discovery: $Name" }
    }
}
function Get-ScheduledTask { param($TaskName, $ErrorAction) return $global:fixtureConfig.ExistingTask }
function New-ScheduledTaskAction { param($Execute, $Argument, $WorkingDirectory) return @{ Execute = $Execute; Arguments = $Argument; WorkingDirectory = $WorkingDirectory } }
function New-ScheduledTaskTrigger { param([switch]$AtLogOn, $User) return @{ User = $User } }
function New-ScheduledTaskPrincipal { param($UserId, $LogonType, $RunLevel) return @{ UserId = $UserId; LogonType = $LogonType; RunLevel = $RunLevel } }
function New-ScheduledTaskSettingsSet { param([switch]$AllowStartIfOnBatteries, [switch]$DontStopIfGoingOnBatteries, $ExecutionTimeLimit, $RestartCount, $RestartInterval) return @{ Fixture = $true } }
function Register-ScheduledTask {
    param($TaskName, $Action, $Trigger, $Principal, $Settings, $Description, [switch]$Force)
    @{ TaskName = $TaskName; Action = $Action; Principal = $Principal } | ConvertTo-Json -Depth 8 -Compress | Add-Content (Join-Path $PSScriptRoot 'scheduler.jsonl')
}
function Start-ScheduledTask {
    param($TaskName)
    if ($global:fixtureConfig.Healthy) { throw 'Healthy hub must not be started again' }
    if ($global:fixtureConfig.StartFailure) { throw 'fixture task start failure' }
    if (-not (Test-Path (Join-Path $PSScriptRoot 'prepare.log'))) { throw 'Task used before native preparation' }
    $task = Get-Content (Join-Path $PSScriptRoot 'scheduler.jsonl') | Select-Object -Last 1 | ConvertFrom-Json
    if ($task.Principal.LogonType -ne 'S4U' -or $task.TaskName -ne $TaskName) { throw 'Unsafe task start' }
    'started' | Set-Content (Join-Path $PSScriptRoot 'launcher.log')
}
function Invoke-RestMethod {
    param($Method, $Uri, $TimeoutSec)
    if ($Uri -ne 'http://127.0.0.1:4765/api/health') { throw 'Unexpected readiness target' }
    return @{ ready = ($global:fixtureConfig.Healthy -or (Test-Path (Join-Path $PSScriptRoot 'launcher.log'))); service = 'cli-dashboard-hub' }
}
try { & (Join-Path $PSScriptRoot 'install-dashboard-hub.ps1'); exit 0 }
catch { [Console]::OutputEncoding = [Text.UTF8Encoding]::new($false); [Console]::Error.WriteLine($_.Exception.Message); exit 1 }
'@ | Set-Content (Join-Path $folder 'wrapper.ps1') -Encoding utf8
        $process = $null
        try {
            $start = [Diagnostics.ProcessStartInfo]::new($powerShell)
            $start.UseShellExecute = $false
            $start.RedirectStandardOutput = $true
            $start.RedirectStandardError = $true
            $start.StandardOutputEncoding = [Text.Encoding]::UTF8
            $start.StandardErrorEncoding = [Text.Encoding]::UTF8
            $start.CreateNoWindow = $true
            foreach ($argument in @('-NoProfile', '-File', (Join-Path $folder 'wrapper.ps1'))) { $start.ArgumentList.Add($argument) }
            $process = [Diagnostics.Process]::Start($start)
            $stdout = $process.StandardOutput.ReadToEndAsync()
            $stderr = $process.StandardError.ReadToEndAsync()
            if (-not $process.WaitForExit(45000)) { $process.Kill($true); $process.WaitForExit(); throw 'Offline installer timed out' }
            $output = $stdout.GetAwaiter().GetResult()
            $errorText = $stderr.GetAwaiter().GetResult()
            $calls = @(if (Test-Path (Join-Path $folder 'native-calls.jsonl')) {
                Get-Content (Join-Path $folder 'native-calls.jsonl') | ForEach-Object { [pscustomobject]@{ Arguments = @($_ | ConvertFrom-Json) } }
            })
            $registrations = @(if (Test-Path (Join-Path $folder 'scheduler.jsonl')) { Get-Content (Join-Path $folder 'scheduler.jsonl') })
            $expectedRegistrations = if ($case.ContainsKey('Registered')) { $case.Registered } else { 0 }
            if ($case.ContainsKey('Success')) { Assert-Test ($process.ExitCode -eq 0) "Expected success, got $errorText" }
            else { Assert-Test ($process.ExitCode -ne 0 -and $errorText -match $case.Error) "Expected failing operation '$($case.Error)', exit $($process.ExitCode): $errorText" }
            if ($case.ContainsKey('NativeError')) { Assert-Test ($errorText.Contains($case.NativeError)) 'Native diagnostic bytes were lost or misdecoded' }
            Assert-Test ($calls.Count -eq $case.Calls) "Expected $($case.Calls) native calls, got $($calls.Count)"
            Assert-Test ($registrations.Count -eq $expectedRegistrations) 'Scheduler mutation count differs'
            foreach ($registration in $registrations) {
                $task = $registration | ConvertFrom-Json
                Assert-Test ($task.Principal.LogonType -eq 'S4U') 'Background task must use a noninteractive S4U principal'
                Assert-Test ($task.Principal.UserId -eq 'FIXTUREDOMAIN\fixture-user' -and $task.Principal.RunLevel -eq 'Limited') 'Task changed its user or privilege level'
                Assert-Test ($task.Action.Arguments -match '-NonInteractive' -and $task.Action.Arguments -match '-NoBrowser') 'Background task must prohibit prompting and browser launch'
            }
            Assert-Test ($output -notmatch 'desktop-phubt5b') 'Hard-coded hostname survived'
            $expectedHttps = $case.ContainsKey('Https') -and $case.Https
            Assert-Test (($output -match 'Published privately at:') -eq $expectedHttps) 'HTTPS success claim does not match confirmed operation'
            $success = $case.ContainsKey('Success') -and $case.Success
            Assert-Test (($output -match 'DNS-independent fallback:') -eq $success) 'TCP success claim does not match confirmed operation'
            if ($expectedHttps) {
                Assert-Test ($output.Contains("https://$($dns.TrimEnd('.'))/dashboards/")) 'Wrong device HTTPS URL'
                $after = Get-Content (Join-Path $folder 'serve-after.json') -Raw | ConvertFrom-Json
                Assert-Test ($after.'/unrelated' -eq 'http://127.0.0.1:9001') 'Unrelated Serve mount changed'
                Assert-Test ($after.'/dashboards' -eq 'http://127.0.0.1:4765') 'Requested mount was not applied'
            }
            if ($success) {
                Assert-Test ($output.Contains('http://100.100.100.100/')) 'Wrong fallback address'
                Assert-Test ($after.'tcp:80' -eq '127.0.0.1:4765') 'Requested TCP target was not applied'
                Assert-Test (($calls[-2].Arguments -join ' ') -eq 'serve --bg --yes --set-path /dashboards http://127.0.0.1:4765') 'HTTPS operation is not narrowly scoped'
                Assert-Test (($calls[-1].Arguments -join ' ') -eq 'serve --bg --yes --tcp=80 tcp://127.0.0.1:4765') 'TCP operation is not narrowly scoped'
            }
            if ($case.Name -eq 'TCP failure after HTTPS') { Assert-Test ($errorText -match 'Partial setup.*HTTPS.*completed') 'Partial HTTPS completion not reported' }
            if ($expectedRegistrations -eq 0) {
                Assert-Test (-not (Test-Path (Join-Path $folder 'launcher.log'))) 'Hub started before preflight failed'
                Assert-Test (-not (Test-Path (Join-Path $folder 'prepare.log'))) 'Native preparation ran before preflight failed'
            }
            $passed++
            Write-Host "PASS $($case.Name)"
        } catch {
            $failures.Add("$($case.Name): $($_.Exception.Message)")
            Write-Host "FAIL $($case.Name): $($_.Exception.Message)"
        } finally {
            if ($null -ne $process) { $process.Dispose() }
        }
    }
    if ($failures.Count) { throw "Installer tests failed: $($failures.Count)/$($cases.Count).`n$($failures -join "`n")" }
    Write-Host "Installer tests passed: $passed/$($cases.Count), native doubles and scheduler mocks only."
    if (-not $CaseName) {
        & (Join-Path $PSScriptRoot 'test-dashboard-launch.ps1')
        if ($InstallerPowerShell) {
            & (Join-Path $PSScriptRoot 'test-dashboard-launch.ps1') -LauncherPowerShell $InstallerPowerShell
        }
    }
} finally {
    Remove-Item -LiteralPath $fixture -Recurse -Force
}
