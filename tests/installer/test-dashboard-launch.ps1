# Real launcher/native-helper qualification. Never registers tasks or reads user data.
param([string]$LauncherPowerShell, [string]$LauncherPath, [string]$NativeSourcePath)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$repository = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '../..'))
if (-not $LauncherPath) { $LauncherPath = Join-Path $repository 'run-dashboard-hub.ps1' }
if (-not $LauncherPowerShell) { $LauncherPowerShell = (Get-Process -Id $PID).Path }
if (-not $NativeSourcePath) { $NativeSourcePath = Join-Path $repository 'scripts/DashboardBackgroundProcess.cs' }
function Assert-Launch { param([bool]$Condition, [string]$Message) if (-not $Condition) { throw $Message } }
function Read-LaunchPipe {
    param($Task, [string]$Label)
    Assert-Launch ($Task.Wait(5000)) "$Label did not reach EOF after process exit; an inherited writer may still be open"
    return $Task.GetAwaiter().GetResult()
}

# Reject the old path BEFORE executing it, so even a regression cannot flash a window.
$source = Get-Content -LiteralPath $LauncherPath -Raw
Assert-Launch ($source -notmatch '(?s)\$serverProcess\s*=\s*Start-Process') 'Legacy hidden-only Node launch is unsafe'
Assert-Launch ($source -notmatch 'Add-Type') 'Background launcher must not invoke the legacy runtime compiler'
$preparation = Get-Content (Join-Path $repository 'scripts/prepare-dashboard-launcher.ps1') -Raw
Assert-Launch ($preparation -match '\$start.CreateNoWindow = \$true' -and $preparation -match '\$start.UseShellExecute = \$false') 'Preparation must suppress compiler console creation'
$nativeSource = Get-Content -LiteralPath $NativeSourcePath -Raw
Assert-Launch ($nativeSource -match '0x080[08]0004, IntPtr.Zero, directory') 'Native launch must set CREATE_NO_WINDOW before any probe'
if (-not ('HemSoft.DashboardBackgroundProcess' -as [type])) {
    Add-Type -Path $NativeSourcePath
}
foreach ($example in @(
    @{ Input = ''; Expected = '""' },
    @{ Input = 'a b'; Expected = '"a b"' },
    @{ Input = 'a"b'; Expected = '"a\"b"' },
    @{ Input = 'a\'; Expected = '"a\\"' }
)) {
    Assert-Launch ([HemSoft.DashboardBackgroundProcess]::Quote($example.Input) -ceq $example.Expected) 'Native argument escaping failed'
}
if ([Environment]::OSVersion.Platform -ne 'Win32NT') {
    Write-Host 'PASS launcher policy and native argument quoting; real CREATE_NO_WINDOW qualification requires Windows'
    return
}

$fixture = Join-Path ([IO.Path]::GetTempPath()) "gh-x launch $([char]0x03bb) space $([guid]::NewGuid().ToString('N'))"
New-Item -ItemType Directory -Path (Join-Path $fixture 'scripts') -Force | Out-Null
Copy-Item $LauncherPath (Join-Path $fixture 'run-dashboard-hub.ps1')
Copy-Item $NativeSourcePath (Join-Path $fixture 'scripts/DashboardBackgroundProcess.cs')
Copy-Item (Join-Path $repository 'scripts/prepare-dashboard-launcher.ps1') (Join-Path $fixture 'scripts')
$native = Join-Path $fixture 'node probe.exe'
$process = $null
$owned = $null
try {
    @'
package main
import ("encoding/json"; "fmt"; "net"; "net/http"; "os"; "path/filepath"; "strconv"; "syscall"; "time")
func main() {
    args := os.Args[1:]; port, control, sentinel := "", "", ""
    for i, a := range args { if a == "--port" { port = args[i+1] }; if a == "--control" { control = args[i+1] }; if a == "--sentinel" { sentinel = args[i+1] } }
    root := filepath.Dir(control)
    console, _, _ := syscall.NewLazyDLL("kernel32.dll").NewProc("GetConsoleWindow").Call()
    cwd, _ := os.Getwd()
    if sentinel != "" {
        handle, _ := strconv.ParseUint(sentinel, 10, 64)
        // An unrelated inherited pipe is a real leak even if it is not stdout.
        kind, _, _ := syscall.NewLazyDLL("kernel32.dll").NewProc("GetFileType").Call(uintptr(handle))
        raw, _ := json.Marshal(map[string]interface{}{"console":console, "inheritedPipe":kind == 3})
        if err := os.WriteFile(control, raw, 0600); err != nil { panic(err) }
        return
    }
    raw, _ := json.Marshal(map[string]interface{}{"pid":os.Getpid(), "console":console, "args":args, "cwd":cwd})
    if err := os.WriteFile(control, raw, 0600); err != nil { panic(err) }
    if _, err := os.Stat(filepath.Join(root,"fail")); err == nil { fmt.Fprintln(os.Stderr,"owned probe startup failure"); os.Exit(23) }
    listener, err := net.Listen("tcp", "127.0.0.1:"+port); if err != nil { panic(err) }
    handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type","application/json")
        fmt.Fprint(w,`{"ready":true,"service":"cli-dashboard-hub"}`)
    })
    if _, err := os.Stat(filepath.Join(root,"timeout")); err != nil { go http.Serve(listener,handler) }
    for {
        if _, err := os.Stat(filepath.Join(root,"emit")); err == nil { fmt.Println("stdout survives launcher exit"); fmt.Fprintln(os.Stderr,"stderr survives launcher exit"); os.Remove(filepath.Join(root,"emit")) }
        if _, err := os.Stat(filepath.Join(root,"stop")); err == nil { return }
        time.Sleep(20*time.Millisecond)
    }
}
'@ | Set-Content (Join-Path $fixture 'probe.go') -Encoding utf8
    & go build -o $native (Join-Path $fixture 'probe.go')
    if ($LASTEXITCODE -ne 0) { throw 'Cannot build owned native console probe' }
    Write-Host 'BEGIN native handle-isolation probe in fixture host'
    $sentinel = [IO.Pipes.AnonymousPipeServerStream]::new([IO.Pipes.PipeDirection]::In, [IO.HandleInheritability]::Inheritable)
    try {
        $isolationControl = Join-Path $fixture 'handle-isolation.json'
        $owned = [HemSoft.DashboardBackgroundProcess]::Start($native,
            @('--control', $isolationControl, '--sentinel', $sentinel.ClientSafePipeHandle.DangerousGetHandle().ToInt64().ToString()),
            $fixture, (Join-Path $fixture 'isolation-out.log'), (Join-Path $fixture 'isolation-err.log'))
        Assert-Launch ($owned.WaitForExit(5000)) 'Owned handle-isolation probe timed out'
        Assert-Launch ($owned.ExitCode -eq 0) 'Owned handle-isolation probe failed'
        $isolation = Get-Content $isolationControl -Raw | ConvertFrom-Json
        Assert-Launch ($isolation.console -eq 0) 'Handle-isolation probe acquired a console'
        Assert-Launch (-not $isolation.inheritedPipe) 'Native helper leaked an unrelated inheritable pipe into the child'
        $owned.Dispose(); $owned = $null
        Write-Host 'PASS native helper excludes unrelated inheritable pipes'
    } finally { $sentinel.Dispose() }
    foreach ($mode in @('ready', 'fail', 'timeout')) {
        Write-Host "BEGIN native windowless launcher $mode under $LauncherPowerShell"
        $controlDirectory = Join-Path $fixture $mode
        New-Item -ItemType Directory -Path $controlDirectory | Out-Null
        if ($mode -ne 'ready') { New-Item -ItemType File -Path (Join-Path $controlDirectory $mode) | Out-Null }
        $listener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, 0)
        $listener.Start()
        $port = $listener.LocalEndpoint.Port
        $listener.Stop()
        $control = Join-Path $controlDirectory 'control.json'
        $start = [Diagnostics.ProcessStartInfo]::new($LauncherPowerShell)
        $start.UseShellExecute = $false
        $start.CreateNoWindow = $true
        $start.RedirectStandardOutput = $true
        $start.RedirectStandardError = $true
        foreach ($argument in @('-NoProfile', '-NonInteractive', '-File', (Join-Path $fixture 'run-dashboard-hub.ps1'),
            '-NoBrowser', '-NodeCommand', $native, '-ControlDirectory', $controlDirectory, '-Port', [string]$port, '-StartupTimeoutSeconds', '2')) {
            $start.ArgumentList.Add($argument)
        }
        $start.ArgumentList.Add('-Prepare')
        $prepare = [Diagnostics.Process]::Start($start)
        try {
            $prepareOut = $prepare.StandardOutput.ReadToEndAsync()
            $prepareError = $prepare.StandardError.ReadToEndAsync()
            Assert-Launch ($prepare.WaitForExit(40000)) 'Owned preparation timed out'
            $null = Read-LaunchPipe $prepareOut 'Preparation stdout'
            $preparationError = Read-LaunchPipe $prepareError 'Preparation stderr'
            Assert-Launch ($prepare.ExitCode -eq 0) "Owned preparation failed: $preparationError"
        } finally { if (-not $prepare.HasExited) { $prepare.Kill($true) }; $prepare.Dispose() }
        Write-Host "Prepared native launcher $mode"
        $start.ArgumentList.RemoveAt($start.ArgumentList.Count - 1)
        $process = [Diagnostics.Process]::Start($start)
        $stdout = $process.StandardOutput.ReadToEndAsync()
        $stderr = $process.StandardError.ReadToEndAsync()
        if (-not $process.WaitForExit(30000)) { $process.Kill($true); throw 'Owned launcher qualification timed out' }
        Write-Host "Launcher $mode exited; checking bounded pipe EOF"
        # Acquire the known fixture child before draining pipes, so a pipe-leak
        # regression still cleans up that exact owned process instead of hanging.
        if ($mode -eq 'ready' -and (Test-Path $control)) {
            $owned = Get-Process -Id (Get-Content $control -Raw | ConvertFrom-Json).pid
            $null = $owned.Handle
        }
        $output = Read-LaunchPipe $stdout 'Launcher stdout'
        $errorText = Read-LaunchPipe $stderr 'Launcher stderr'
        $evidence = Get-Content $control -Raw | ConvertFrom-Json
        Assert-Launch ($evidence.console -eq 0) 'Owned Node probe unexpectedly acquired a console'
        Assert-Launch ($evidence.cwd -eq $fixture) 'Native helper changed the working directory'
        Assert-Launch ($evidence.args[0] -eq (Join-Path $fixture 'src\dashboard-hub\main.mjs')) 'Entry path argument was split or changed'
        Assert-Launch ($evidence.args[-1] -eq $control) 'Control path argument was split or changed'
        if ($mode -eq 'ready') {
            Assert-Launch ($null -ne $owned -and -not $owned.HasExited) 'Owned ready probe must remain alive after launcher pipe EOF'
            Assert-Launch ($process.ExitCode -eq 0 -and $output -match 'hub ready') "Launcher failed: $errorText"
            New-Item -ItemType File -Path (Join-Path $controlDirectory 'emit') | Out-Null
            $deadline = [DateTime]::UtcNow.AddSeconds(5)
            do {
                $logged = (Get-Content (Join-Path $controlDirectory 'stdout.log') -Raw) -match 'stdout survives launcher exit' -and
                    (Get-Content (Join-Path $controlDirectory 'stderr.log') -Raw) -match 'stderr survives launcher exit'
                if (-not $logged) { Start-Sleep -Milliseconds 25 }
            } while (-not $logged -and [DateTime]::UtcNow -lt $deadline)
            Assert-Launch $logged 'Native log handles did not survive the launcher'
            # Reuse the exact invocation; an existing healthy listener must not start a second probe.
            $again = [Diagnostics.Process]::Start($start)
            try {
                $againOut = $again.StandardOutput.ReadToEndAsync()
                $againError = $again.StandardError.ReadToEndAsync()
                Assert-Launch ($again.WaitForExit(10000) -and $again.ExitCode -eq 0) 'Healthy hub reuse failed'
                $null = Read-LaunchPipe $againOut 'Reuse stdout'
                $null = Read-LaunchPipe $againError 'Reuse stderr'
                Assert-Launch ((Get-Content $control -Raw | ConvertFrom-Json).pid -eq $evidence.pid) 'Healthy hub was replaced'
            } finally { if (-not $again.HasExited) { $again.Kill($true) }; $again.Dispose() }
            New-Item -ItemType File -Path (Join-Path $controlDirectory 'stop') | Out-Null
            Assert-Launch ($owned.WaitForExit(5000)) 'Owned probe did not exit'
            $owned.Dispose(); $owned = $null
        } else {
            Assert-Launch ($process.ExitCode -ne 0) 'Failed startup unexpectedly succeeded'
            if ($mode -eq 'fail') { Assert-Launch ($errorText -match '(?s)23.*owned probe startup failure') 'Startup diagnostic was lost' }
            else {
                Assert-Launch ($errorText -match 'did not become ready') 'Timeout diagnostic was lost'
                Assert-Launch (-not (Get-Process -Id $evidence.pid -ErrorAction SilentlyContinue)) 'Timed-out owned process survived'
            }
        }
        $process.Dispose(); $process = $null
        Write-Host "PASS native windowless launcher $mode under $LauncherPowerShell"
    }
} finally {
    # These are only the exact test-owned handles, never a discovered Home service.
    if ($null -ne $owned) {
        if (-not $owned.HasExited) { $owned.Kill(); $null = $owned.WaitForExit(5000) }
        $owned.Dispose()
    }
    if ($null -ne $process) {
        if (-not $process.HasExited) { $process.Kill($true); $null = $process.WaitForExit(5000) }
        $process.Dispose()
    }
    Remove-Item -LiteralPath $fixture -Recurse -Force
}
