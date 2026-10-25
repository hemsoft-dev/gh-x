param(
    [int]$Port = 4765,
    [switch]$NoBrowser,
    [string]$NodeCommand = "node",
    [int]$StartupTimeoutSeconds = 10,
    [string]$ControlDirectory,
    [switch]$Prepare
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$repositoryRoot = [System.IO.Path]::GetFullPath($PSScriptRoot).TrimEnd('\')
$dashboardEntry = Join-Path $repositoryRoot "src\dashboard-hub\main.mjs"
$userProfilePath = [Environment]::GetFolderPath("UserProfile")
if (-not $ControlDirectory) { $ControlDirectory = Join-Path $userProfilePath ".codex\dashboard-hub" }
$controlDirectory = [IO.Path]::GetFullPath($ControlDirectory)
$controlPath = Join-Path $controlDirectory "control.json"
$localUrl = "http://127.0.0.1:$Port/"
$nativeSource = Join-Path $repositoryRoot 'scripts\DashboardBackgroundProcess.cs'
$sha = [Security.Cryptography.SHA256]::Create()
try {
    $nativeHash = [BitConverter]::ToString($sha.ComputeHash([IO.File]::ReadAllBytes($nativeSource))).Replace('-', '')
} finally { $sha.Dispose() }
$nativeAssembly = Join-Path $controlDirectory "launcher-$nativeHash.dll"

if ($Prepare) {
    & (Join-Path $repositoryRoot 'scripts\prepare-dashboard-launcher.ps1') -SourcePath $nativeSource -AssemblyPath $nativeAssembly -ExpectedSourceHash $nativeHash
    Write-Host 'Prepared windowless dashboard launcher.'
    return
}

function Test-DashboardHub {
    try {
        $health = Invoke-RestMethod -Method Get -Uri "${localUrl}api/health" -TimeoutSec 1
        return $health.ready -eq $true -and $health.service -eq "cli-dashboard-hub"
    } catch {
        return $false
    }
}

if (-not (Test-DashboardHub)) {
    New-Item -ItemType Directory -Path $controlDirectory -Force | Out-Null
    $node = Get-Command -Name $NodeCommand -ErrorAction Stop
    $nativeType = 'HemSoft.DashboardBackgroundProcess' -as [type]
    if ($nativeType -and $nativeType.Assembly.Location -ne $nativeAssembly) {
        throw 'A different dashboard launcher assembly is already loaded; use a fresh PowerShell process.'
    }
    if (-not $nativeType) {
        if (-not (Test-Path -LiteralPath $nativeAssembly -PathType Leaf)) {
            throw 'Windowless dashboard launcher is not prepared. Run the installer or run-dashboard-hub.ps1 -Prepare -NoBrowser first.'
        }
        [Reflection.Assembly]::LoadFrom($nativeAssembly) | Out-Null
    }
    $serverProcess = [HemSoft.DashboardBackgroundProcess]::Start(
        $node.Path, @($dashboardEntry, '--port', [string]$Port, '--control', $controlPath),
        $repositoryRoot, (Join-Path $controlDirectory 'stdout.log'), (Join-Path $controlDirectory 'stderr.log'))

    try {
        $deadline = [DateTime]::UtcNow.AddSeconds($StartupTimeoutSeconds)
        do {
            if ($serverProcess.HasExited) {
                $errorLog = Get-Content -LiteralPath (Join-Path $controlDirectory "stderr.log") -Raw -ErrorAction SilentlyContinue
                throw "The dashboard hub exited with code $($serverProcess.ExitCode). $errorLog"
            }
            Start-Sleep -Milliseconds 100
        } while (-not (Test-DashboardHub) -and [DateTime]::UtcNow -lt $deadline)

        if (-not (Test-DashboardHub)) {
            if (-not $serverProcess.HasExited) { $serverProcess.Kill() }
            if (-not $serverProcess.WaitForExit(5000)) { throw 'Owned dashboard startup process did not stop after timeout.' }
            throw "The dashboard hub did not become ready within $StartupTimeoutSeconds seconds."
        }
    } finally { $serverProcess.Dispose() }
}

if (-not $NoBrowser) {
    Start-Process $localUrl | Out-Null
}

Write-Host "CLI dashboard hub ready at $localUrl"
