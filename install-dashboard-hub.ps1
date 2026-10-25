param(
    [ValidateRange(1, 65535)][int]$Port = 4765,
    [ValidatePattern('^/(?:[A-Za-z0-9._~-]+/?)*$')][string]$ServePath = "/dashboards",
    [ValidatePattern('^[^*?\[\]\\/:]+$')][string]$TaskName = "HemSoft CLI Dashboard Hub"
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"
if (@($ServePath.Split('/') | Where-Object { $_ -eq '.' -or $_ -eq '..' }).Count) {
    throw 'ServePath must not contain dot segments; nothing was changed.'
}

$repositoryRoot = [System.IO.Path]::GetFullPath($PSScriptRoot).TrimEnd('\')
$launcherPath = Join-Path $repositoryRoot "run-dashboard-hub.ps1"
$powerShellPath = [IO.Path]::GetFullPath((Get-Command powershell.exe -ErrorAction Stop).Path)
$tailscalePath = [IO.Path]::GetFullPath((Get-Command tailscale.exe -ErrorAction Stop).Path)
$userId = "$env:USERDOMAIN\$env:USERNAME"

function Invoke-Tailscale {
    param([string]$Operation, [string[]]$Arguments)
    # These arguments are constants or validated URL/path/port values without spaces.
    # ProcessStartInfo works in Windows PowerShell 5.1 and separates stderr from JSON.
    $start = New-Object System.Diagnostics.ProcessStartInfo
    $start.FileName = $tailscalePath
    $start.Arguments = $Arguments -join ' '
    $start.UseShellExecute = $false
    $start.RedirectStandardInput = $true
    $start.RedirectStandardOutput = $true
    $start.RedirectStandardError = $true
    $start.StandardOutputEncoding = [Text.Encoding]::UTF8
    $start.StandardErrorEncoding = [Text.Encoding]::UTF8
    $start.CreateNoWindow = $true
    $process = $null
    try {
        $process = [Diagnostics.Process]::Start($start)
        $process.StandardInput.Close()
        $stdout = $process.StandardOutput.ReadToEndAsync()
        $stderr = $process.StandardError.ReadToEndAsync()
        if (-not $process.WaitForExit(30000)) {
            $process.Kill()
            $process.WaitForExit()
            $null = $stdout.GetAwaiter().GetResult()
            $partialError = $stderr.GetAwaiter().GetResult().Trim()
            throw "Tailscale $Operation timed out after 30 seconds; completion is unverified. $partialError"
        }
        $output = $stdout.GetAwaiter().GetResult()
        $errorText = $stderr.GetAwaiter().GetResult()
        if ($process.ExitCode -ne 0) {
            throw "Tailscale $Operation failed with exit code $($process.ExitCode). $($errorText.Trim())"
        }
        return $output.Trim()
    } finally {
        if ($null -ne $process) { $process.Dispose() }
    }
}

function Get-OptionalProperty {
    param($Value, [string]$Name)
    if ($null -ne $Value) {
        $property = $Value.PSObject.Properties[$Name]
        if ($null -ne $property) { return $property.Value }
    }
    return $null
}

function Test-PlainWebProxy {
    param($Handler, [string]$Target)
    if ($Handler -isnot [pscustomobject]) { return $false }
    $properties = @($Handler.PSObject.Properties)
    return $properties.Count -eq 1 -and $properties[0].Name -ceq 'Proxy' -and
        $properties[0].Value -is [string] -and $properties[0].Value -ceq $Target
}

function Test-PlainTcpForward {
    param($Handler, [string]$Target)
    if ($Handler -isnot [pscustomobject]) { return $false }
    foreach ($property in $Handler.PSObject.Properties) {
        switch ($property.Name) {
            'TCPForward' { if ($property.Value -isnot [string] -or $property.Value -cne $Target) { return $false } }
            { $_ -in @('HTTP', 'HTTPS') } { if ($property.Value -isnot [bool] -or $property.Value) { return $false } }
            'TerminateTLS' { if ($property.Value -isnot [string] -or $property.Value.Length) { return $false } }
            'ProxyProtocol' { if (($property.Value -isnot [int] -and $property.Value -isnot [long]) -or $property.Value -ne 0) { return $false } }
            default { return $false }
        }
    }
    return (Get-OptionalProperty $Handler 'TCPForward') -ceq $Target
}

$existingTask = Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
if ($existingTask) {
    $owned = $false
    try {
        $actions = @($existingTask.Actions)
        if ($actions.Count -eq 1) {
            # Accept a real -File invocation, not launcher text embedded in -Command.
            $prefix = '(?:(?:-NoProfile|-NonInteractive)\s+|(?:-WindowStyle\s+(?:Hidden|Normal|Minimized|Maximized)|-ExecutionPolicy\s+(?:Bypass|RemoteSigned|AllSigned|Restricted|Unrestricted|Default))\s+)*'
            $file = [regex]::Match([string]$actions[0].Arguments, '(?i)^\s*' + $prefix + '-File\s+(?:"([^"]+)"|''([^'']+)''|(\S+))(?:\s|$)')
            $scriptPath = ($file.Groups | Select-Object -Skip 1 | Where-Object Success | Select-Object -First 1).Value
            $principal = [string]$existingTask.Principal.UserId
            $sameUser = $principal -eq $userId
            if (-not $sameUser -and $principal -match '^S-1-' -and [Environment]::OSVersion.Platform -eq 'Win32NT') {
                $account = New-Object Security.Principal.NTAccount($userId)
                $sameUser = $principal -eq $account.Translate([Security.Principal.SecurityIdentifier]).Value
            }
            $owned = $file.Success -and $sameUser -and
                [IO.Path]::GetFullPath($scriptPath) -eq $launcherPath -and
                [IO.Path]::GetFullPath([string]$actions[0].Execute) -eq $powerShellPath
        }
    } catch { $owned = $false }
    if (-not $owned) {
        throw "Scheduled task '$TaskName' already exists and is not this user's exact dashboard launcher. Choose another TaskName; nothing was changed."
    }
    if ($existingTask.State -eq 'Disabled' -or -not $existingTask.Settings.Enabled) {
        throw "Scheduled task '$TaskName' is disabled; nothing was changed. Preserve containment until its owner reviews deployment safety."
    }
}

$statusOutput = Invoke-Tailscale 'device status' @('status', '--json')
try {
    $status = $statusOutput | ConvertFrom-Json -ErrorAction Stop
    $dnsName = [string]$status.Self.DNSName
    $deviceIPs = @($status.Self.TailscaleIPs)
} catch { throw "Tailscale device status did not contain valid Self.DNSName and Self.TailscaleIPs." }
if ($dnsName.EndsWith('.')) { $dnsName = $dnsName.Substring(0, $dnsName.Length - 1) }
$label = '[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?'
if ($dnsName.Length -gt 253 -or $dnsName -notmatch "^(?:$label\.)+ts\.net$") {
    throw 'Tailscale device DNS name is invalid; no publication was attempted.'
}
$tailnetIp = [string](Invoke-Tailscale 'IPv4 address lookup' @('ip', '-4'))
$address = $null
if ($tailnetIp -notmatch '^(?:[0-9]{1,3}\.){3}[0-9]{1,3}$' -or
    -not [Net.IPAddress]::TryParse($tailnetIp, [ref]$address) -or
    $address.AddressFamily -ne [Net.Sockets.AddressFamily]::InterNetwork -or
    $address.ToString() -ne $tailnetIp -or $tailnetIp -notin $deviceIPs) {
    throw 'Tailscale IPv4 address lookup did not return one valid address for this device; no publication was attempted.'
}

$configOutput = Invoke-Tailscale 'Serve configuration' @('serve', 'status', '--json')
$foregroundConfigs = @()
try {
    $config = $configOutput | ConvertFrom-Json -ErrorAction Stop
    if ($null -ne $config -and $config -isnot [pscustomobject]) { throw 'Expected an object' }
    $tcp = Get-OptionalProperty $config 'TCP'
    $web = Get-OptionalProperty $config 'Web'
    $funnel = Get-OptionalProperty $config 'AllowFunnel'
    foreach ($value in @($tcp, $web, $funnel)) {
        if ($null -ne $value -and $value -isnot [pscustomobject]) { throw 'Expected protocol maps' }
    }
    if ($null -ne $funnel) {
        foreach ($entry in $funnel.PSObject.Properties) {
            if ($entry.Value -isnot [bool]) { throw 'Expected boolean Funnel values' }
        }
    }
    $foreground = Get-OptionalProperty $config 'Foreground'
    if ($null -ne $foreground -and $foreground -isnot [pscustomobject]) { throw 'Expected foreground map' }
    if ($null -ne $foreground) {
        foreach ($session in $foreground.PSObject.Properties) {
            if ($session.Value -isnot [pscustomobject]) { throw 'Expected foreground configuration' }
            $foregroundTcp = Get-OptionalProperty $session.Value 'TCP'
            $foregroundWeb = Get-OptionalProperty $session.Value 'Web'
            $foregroundFunnel = Get-OptionalProperty $session.Value 'AllowFunnel'
            foreach ($map in @($foregroundTcp, $foregroundWeb, $foregroundFunnel)) {
                if ($null -ne $map -and $map -isnot [pscustomobject]) { throw 'Expected foreground protocol map' }
            }
            if ($null -ne $foregroundFunnel) {
                foreach ($entry in $foregroundFunnel.PSObject.Properties) {
                    if ($entry.Value -isnot [bool]) { throw 'Expected boolean foreground Funnel values' }
                }
            }
            $foregroundConfigs += [pscustomobject]@{ TCP = $foregroundTcp; Web = $foregroundWeb; Funnel = $foregroundFunnel }
        }
    }
} catch { throw 'Tailscale Serve configuration is invalid; no publication was attempted.' }
foreach ($session in $foregroundConfigs) {
    $ports = @(if ($null -ne $session.TCP) { $session.TCP.PSObject.Properties | ForEach-Object Name })
    if ('80' -in $ports -or '443' -in $ports -or
        $null -ne (Get-OptionalProperty $session.Web "${dnsName}:443") -or
        (Get-OptionalProperty $session.Funnel "${dnsName}:443") -eq $true) {
        throw 'Tailscale foreground Serve already claims a requested port or host; nothing was changed. Finish that foreground session before installation.'
    }
}
if ((Get-OptionalProperty $funnel "${dnsName}:443") -eq $true) {
    throw 'Tailscale HTTPS port 443 is already public through Funnel; nothing was changed.'
}
$httpsPort = Get-OptionalProperty $tcp '443'
if ($null -ne $httpsPort -and (Get-OptionalProperty $httpsPort 'HTTPS') -ne $true) {
    throw 'Tailscale port 443 is already used by another Serve target; nothing was changed.'
}
$tcpPort = Get-OptionalProperty $tcp '80'
if ($null -ne $tcpPort -and -not (Test-PlainTcpForward $tcpPort "127.0.0.1:$Port")) {
    throw 'Tailscale TCP port 80 is already used by another Serve target; nothing was changed.'
}
$hostConfig = Get-OptionalProperty $web "${dnsName}:443"
$handlers = Get-OptionalProperty $hostConfig 'Handlers'
if ($null -ne $handlers) {
    foreach ($mount in $handlers.PSObject.Properties) {
        if ($mount.Name.TrimEnd('/') -eq $ServePath.TrimEnd('/') -and
            -not (Test-PlainWebProxy $mount.Value "http://127.0.0.1:$Port")) {
            throw "Tailscale HTTPS path '$ServePath' is already used by another Serve target; nothing was changed."
        }
    }
}

& $launcherPath -Port $Port -NoBrowser -Prepare
$action = New-ScheduledTaskAction `
    -Execute $powerShellPath `
    -Argument "-NoProfile -NonInteractive -WindowStyle Hidden -ExecutionPolicy Bypass -File `"$launcherPath`" -Port $Port -NoBrowser" `
    -WorkingDirectory $repositoryRoot
$trigger = New-ScheduledTaskTrigger -AtLogOn -User $userId
$principal = New-ScheduledTaskPrincipal -UserId $userId -LogonType S4U -RunLevel Limited
$settings = New-ScheduledTaskSettingsSet `
    -AllowStartIfOnBatteries `
    -DontStopIfGoingOnBatteries `
    -ExecutionTimeLimit ([TimeSpan]::Zero) `
    -RestartCount 3 `
    -RestartInterval (New-TimeSpan -Minutes 1)
Register-ScheduledTask `
    -TaskName $TaskName -Action $action -Trigger $trigger -Principal $principal -Settings $settings `
    -Description "Starts the loopback-only Codex CLI and Copilot CLI dashboard hub." -Force | Out-Null
Write-Host "Installed scheduled task: $TaskName"

function Test-HubReady {
    try {
        $health = Invoke-RestMethod -Method Get -Uri "http://127.0.0.1:$Port/api/health" -TimeoutSec 1
        return $health.ready -eq $true -and $health.service -eq 'cli-dashboard-hub'
    } catch { return $false }
}
# Start through the S4U task, never as a child of the interactive installer.
# An already healthy service is reused without touching its process.
if (-not (Test-HubReady)) {
    Start-ScheduledTask -TaskName $TaskName
    $deadline = [DateTime]::UtcNow.AddSeconds(15)
    while (-not (Test-HubReady)) {
        if ([DateTime]::UtcNow -ge $deadline) { throw 'Noninteractive dashboard task did not become ready; Serve was not changed.' }
        Start-Sleep -Milliseconds 100
    }
}

$httpsCompleted = $false
try {
    Invoke-Tailscale 'HTTPS Serve' @('serve', '--bg', '--yes', '--set-path', $ServePath, "http://127.0.0.1:$Port") | Out-Null
    $httpsCompleted = $true
    Write-Host "Published privately at: https://$dnsName$($ServePath.TrimEnd('/'))/"
    Invoke-Tailscale 'TCP Serve' @('serve', '--bg', '--yes', '--tcp=80', "tcp://127.0.0.1:$Port") | Out-Null
    Write-Host "DNS-independent fallback: http://$tailnetIp/"
} catch {
    $completed = 'Loopback hub and scheduled task installed.'
    if ($httpsCompleted) { $completed += ' HTTPS Serve completed; TCP publication is unverified.' }
    else { $completed += ' HTTPS and TCP publication are unverified.' }
    throw "$($_.Exception.Message) Partial setup: $completed Rerun this installer after fixing the named operation. Existing mounts and tasks are not reset or deleted."
}
