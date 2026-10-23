# Shared local audit executor. No commands run when this file is dot-sourced.
function Stop-AuditBlocked {
    param([string]$Reason)
    $error = [InvalidOperationException]::new($Reason)
    $error.Data['AuditBlocked'] = $true
    throw $error
}

function Invoke-AuditPlan {
    param(
        [Parameter(Mandatory)]$Inventory,
        [Parameter(Mandatory)][hashtable]$Actions,
        [Parameter(Mandatory)][hashtable]$Context,
        [scriptblock]$ToolAvailable = { param($name) [bool](Get-Command $name -ErrorAction SilentlyContinue) }
    )
    $results = [System.Collections.Generic.List[object]]::new()
    $byId = @{}
    foreach ($gate in $Inventory.gates) {
        $status = 'pass'
        $detail = 'Completed successfully.'
        $timer = [Diagnostics.Stopwatch]::StartNew()
        try {
            if ($byId.ContainsKey($gate.id)) { throw "Duplicate gate: $($gate.id)" }
            if (-not $Actions.ContainsKey($gate.id)) { throw "Missing implementation: $($gate.id)" }
            $missing = @($gate.tools | Where-Object { -not (& $ToolAvailable $_) })
            if ($missing.Count) { Stop-AuditBlocked "Missing tools: $($missing -join ', ')" }
            foreach ($dependency in $gate.needs) {
                if (-not $byId.ContainsKey($dependency) -or $byId[$dependency].Status -ne 'pass') {
                    Stop-AuditBlocked "Prerequisite $dependency did not pass; no stale evidence will be used."
                }
            }
            Show-Section $gate.id
            & $Actions[$gate.id] $Context | Out-Host
        } catch {
            $status = if ($_.Exception.Data['AuditBlocked']) { 'blocked' } else { 'fail' }
            $detail = $_.Exception.Message
            Write-Host "${status}: $detail"
        }
        $timer.Stop()
        $result = [pscustomobject]@{
            Gate = $gate.id; Status = $status; Detail = $detail
            Seconds = [math]::Round($timer.Elapsed.TotalSeconds, 3)
        }
        $results.Add($result)
        $byId[$gate.id] = $result
    }
    return $results.ToArray()
}

function Get-AuditExitCode {
    param([object[]]$Results)
    if (-not $Results.Count -or @($Results | Where-Object Status -NE 'pass').Count) { return 1 }
    return 0
}

function Assert-AuditTool {
    param($Context, [string]$Tool)
    Assert-PinnedToolVersions -VersionFile (Join-Path $Context.Root '.github/quality-tools.env') -ToolName $Tool | Out-Null
}

function Assert-AuditCoverage {
    param([string]$CoveragePath)
    $lines = @(& go tool cover "-func=$CoveragePath" 2>&1)
    if ($LASTEXITCODE -ne 0) { throw "go tool cover failed: $($lines -join "`n")" }
    $lines | ForEach-Object { Write-Host $_ }
    $total = $lines | Where-Object { [string]$_ -match '^total:' } | Select-Object -Last 1
    if (-not $total -or [string]$total -notmatch '([0-9.]+)%') { throw 'Missing total coverage.' }
    $coverage = [double]::Parse($Matches[1], [Globalization.CultureInfo]::InvariantCulture)
    if ($coverage -lt 70.0) { throw "Total coverage $coverage% is below 70%." }
}

function Invoke-ReleaseBuilds {
    param($Context)
    $date = @(& git show -s --format=%cs HEAD)
    if ($LASTEXITCODE -ne 0 -or $date.Count -ne 1) { throw 'Cannot resolve release build date.' }
    $values = @{
        RELEASE_VERSION = 'ci'; RELEASE_BUILD_DATE = $date[0]
        RELEASE_OUTPUT_DIR = (Join-Path $Context.Temp 'release-targets')
        # CI uses the canonical manifest, never an inherited legacy override.
        RELEASE_TARGET_MANIFEST = $null
    }
    $previous = @{}
    try {
        foreach ($key in $values.Keys) {
            $previous[$key] = [Environment]::GetEnvironmentVariable($key)
            [Environment]::SetEnvironmentVariable($key, $values[$key])
        }
        Invoke-CheckedCommand 'release target builds' 'go' @('run', './.github/scripts/release-targets', 'build')
    } finally {
        foreach ($key in $previous.Keys) { [Environment]::SetEnvironmentVariable($key, $previous[$key]) }
    }
}

function Get-AuditActions {
    return @{
        'build' = { param($c) Invoke-CheckedCommand 'build' 'go' @('build', '-o', (Join-Path $c.Temp 'bin/'), './...', './.github/scripts/...') }
        'release-builds' = { param($c) Invoke-ReleaseBuilds $c }
        'vet' = { param($c) Invoke-CheckedCommand 'vet' 'go' @('vet', './...', './.github/scripts/...') }
        'unit-tests' = {
            param($c)
            $packages = @(& go list ./... ./.github/scripts/...)
            if ($LASTEXITCODE -ne 0) { throw 'go list failed.' }
            $packages = @($packages | Where-Object { $_ -notmatch '/tests/behavior$' })
            if (-not $packages.Count) { throw 'No unit-test packages discovered.' }
            Invoke-CheckedCommand 'race tests and coverage' 'go' (@('test', '-race', '-count=1', "-coverprofile=$($c.Coverage)") + $packages)
        }
        'helper-tests' = { param($c) Invoke-CheckedCommand 'release automation helpers' 'go' @('test', '-race', '-count=1', './.github/scripts/...') }
        'behavior-tests' = { param($c) Invoke-CheckedCommand 'CLI behavior' 'go' @('test', '-trimpath', '-race', '-count=1', './tests/behavior') }
        'credential-fixtures' = { param($c) Invoke-CheckedCommand 'credential scanner fixtures' 'go' @('test', '-race', '-count=1', '-timeout=90s', './.github/scripts/credential-scan', '-run', '^TestGitleaks', '-gitleaks-integration') }
        'credentials' = { param($c) Invoke-CheckedCommand 'source and reachable-history credentials' 'go' @('run', './.github/scripts/credential-scan', '-report', (Join-Path $c.Temp 'credential-scope.json')) }
        'dashboard-tests' = { param($c) Invoke-CheckedCommand 'Node dashboards' 'node' @('--test', 'src/codex-dashboard/*.test.mjs', 'src/dashboard-hub/*.test.mjs') }
        'migration-credential-tests' = { param($c) Invoke-CheckedCommand 'migration credential parsing and policy' 'pwsh' @('-NoProfile', '-File', 'scripts/test-migration-credential.ps1') }
        'dashboard-quality-fixtures' = { param($c) Invoke-CheckedCommand 'dashboard quality fixtures' 'node' @('--test', '.github/scripts/dashboard-quality/*.test.mjs') }
        'dashboard-quality' = { param($c) Invoke-CheckedCommand 'dashboard JavaScript quality' 'node' @('.github/scripts/dashboard-quality/run.mjs', (Join-Path $c.Temp 'dashboard-quality')) }
        'runner-tests' = {
            param($c)
            Invoke-CheckedCommand 'audit executor tests' 'pwsh' @('-NoProfile', '-File', '.agents/skills/perfection/scripts/test-audit.ps1')
            Invoke-CheckedCommand 'offline dashboard installer tests' 'pwsh' @('-NoProfile', '-File', 'tests/installer/test-dashboard-hub.ps1')
            if ($IsWindows) {
                Invoke-CheckedCommand 'Windows PowerShell installer tests' 'pwsh' @('-NoProfile', '-File', 'tests/installer/test-dashboard-hub.ps1', '-InstallerPowerShell', "$env:WINDIR/System32/WindowsPowerShell/v1.0/powershell.exe")
            }
        }
        'format' = { param($c) Invoke-NoOutputCommand 'formatting' 'gofmt' @('-l', '.', '.github/scripts') }
        'tidy' = { param($c) Invoke-CheckedCommand 'module tidiness' 'go' @('mod', 'tidy', '-diff') }
        'ruleset' = { param($c) Invoke-CheckedCommand 'ruleset validation' 'go' @('run', '.github/scripts/validate-main-ruleset.go') }
        'changelog' = {
            param($c)
            & gh api repos/HemSoft/gh-x --silent 2>$null
            if ($LASTEXITCODE -ne 0) { Stop-AuditBlocked 'Cannot read HemSoft/gh-x through gh; verify network and GitHub authentication.' }
            Invoke-CheckedCommand 'changelog release links' 'go' @('run', './.github/scripts/changelog-check')
        }
        'staticcheck' = { param($c) Assert-AuditTool $c 'staticcheck'; Invoke-CheckedCommand 'staticcheck' 'staticcheck' @('./...', './.github/scripts/...') }
        'gocritic' = { param($c) Assert-AuditTool $c 'gocritic'; Invoke-CheckedCommand 'gocritic' 'gocritic' @('check', './...', './.github/scripts/...') }
        'errcheck' = { param($c) Assert-AuditTool $c 'errcheck'; Invoke-CheckedCommand 'errcheck' 'errcheck' @('-exclude', '.errcheck_excludes', './...', './.github/scripts/...') }
        'deadcode' = { param($c) Assert-AuditTool $c 'deadcode'; Invoke-NoOutputCommand 'deadcode' 'deadcode' @('./...', './.github/scripts/...') }
        'vulnerabilities' = { param($c) Assert-AuditTool $c 'govulncheck'; Invoke-CheckedCommand 'govulncheck' 'govulncheck' @('./...', './.github/scripts/...') }
        'suppressions' = {
            param($c)
            $files = @(Get-ChildItem -LiteralPath $c.Root -Recurse -File -Filter '*.go')
            Assert-NoSourceMatches 'lint suppressions' $files '//nolint|//lint:ignore|//nosec|#nosec'
        }
        'output-policy' = {
            param($c)
            $files = @(Get-ChildItem -LiteralPath $c.Root -Recurse -File -Filter '*.go' | Where-Object Name -NotLike '*_test.go')
            Assert-NoSourceMatches 'fmt.Print production calls' $files 'fmt\.Print'
        }
        'markdown' = {
            param($c)
            Invoke-CheckedCommand 'Markdown lint' 'npx' @('--yes', "markdownlint-cli2@$($c.Pins.MARKDOWNLINT_CLI2_VERSION)", '**/*.md', '#node_modules', '#.agents', '#.github/agents')
        }
        'coverage' = { param($c) Assert-AuditCoverage $c.Coverage }
        'cyclomatic' = { param($c) Assert-AuditTool $c 'gocyclo'; Invoke-NoOutputCommand 'cyclomatic <= 10' 'gocyclo' @('-over', '10', '-ignore', '_test\.go', '.', '.github/scripts') }
        'cognitive' = { param($c) Assert-AuditTool $c 'gocognit'; Invoke-NoOutputCommand 'cognitive <= 15' 'gocognit' @('-over', '15', '-ignore', '_test\.go', '.', '.github/scripts') }
        'crap' = { param($c) Assert-AuditTool $c 'gocyclo'; Assert-CrapThreshold $c.Coverage 30.0 }
        'mutation-fixtures' = { param($c) Invoke-CheckedCommand 'mutation fixtures' 'bash' @('.github/scripts/test-mutation-gate.sh') }
        'mutation' = {
            param($c)
            Assert-AuditTool $c 'gremlins'
            Assert-MutationThresholds ([double]$c.Pins.MUTATION_EFFICACY_THRESHOLD) ([double]$c.Pins.MUTATION_COVERAGE_THRESHOLD) $c.Pins.MUTATION_PACKAGE_SCOPE
        }
        'performance-tests' = { param($c) Invoke-CheckedCommand 'performance gate tests' 'node' @('--test', 'benchmarks/*.test.mjs') }
        'performance' = {
            param($c)
            # Conservative local qualification always measures, regardless of changed paths.
            Invoke-CheckedCommand 'performance budgets' 'node' @('benchmarks/run.mjs', '--out', (Join-Path $c.Temp 'performance'))
        }
    }
}

function Invoke-LocalAudit {
    param([string]$ReportPath)
    $root = (& git rev-parse --show-toplevel 2>$null)
    if ($LASTEXITCODE -ne 0 -or -not $root) { throw 'Run this script from a gh-x worktree.' }
    $root = $root.Trim()
    if ($ReportPath) { $ReportPath = [IO.Path]::GetFullPath($ReportPath) }
    $temp = Join-Path ([IO.Path]::GetTempPath()) "gh-x-audit-$([guid]::NewGuid().ToString('N'))"
    New-Item -ItemType Directory -Path $temp | Out-Null
    if (-not $ReportPath) { $ReportPath = Join-Path $temp 'report.json' }
    $inventory = Get-Content -LiteralPath (Join-Path $root '.github/local-quality-gates.json') -Raw | ConvertFrom-Json
    $pins = @{}
    foreach ($line in Get-Content -LiteralPath (Join-Path $root '.github/quality-tools.env')) {
        if ($line -match '^(?<name>[A-Z0-9_]+)=(?<value>\S+)$') { $pins[$Matches.name] = $Matches.value }
    }
    foreach ($key in @('MARKDOWNLINT_CLI2_VERSION', 'MUTATION_EFFICACY_THRESHOLD', 'MUTATION_COVERAGE_THRESHOLD', 'MUTATION_PACKAGE_SCOPE')) {
        if (-not $pins[$key]) { throw "Missing $key in .github/quality-tools.env." }
    }
    $context = @{ Root = $root; Temp = $temp; Coverage = (Join-Path $temp 'coverage.out'); Pins = $pins }
    Push-Location $root
    try {
        $revision = (& git rev-parse HEAD)
        if ($LASTEXITCODE -ne 0) { throw 'Cannot identify the audited revision.' }
        $sourceStatus = @(& git status --porcelain)
        if ($LASTEXITCODE -ne 0) { throw 'Cannot identify the audited worktree state.' }
        $results = @(Invoke-AuditPlan -Inventory $inventory -Actions (Get-AuditActions) -Context $context)
        $exitCode = Get-AuditExitCode $results
        $report = [ordered]@{
            Revision = $revision
            SourceStatus = $sourceStatus
            LocalPassed = ($exitCode -eq 0)
            Gates = $results
            Hosted = @($inventory.hosted | ForEach-Object { [ordered]@{ Gate = $_.step; Status = 'not-run'; Reason = $_.reason } })
            ArtifactDirectory = $temp
        }
        $report | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath $ReportPath -Encoding utf8
        $results | Format-Table Gate, Status, Seconds, Detail -Wrap | Out-Host
        Write-Host "Report: $ReportPath"
        Write-Host 'GitHub-only qualification, not run locally:'
        $report.Hosted | ForEach-Object { Write-Host "  $($_.Gate): $($_.Reason)" }
        if ($exitCode -eq 0) {
            Write-Host 'All local gates passed. GitHub-hosted checks remain separate qualification.' -ForegroundColor Green
        } else {
            Write-Host 'Local audit incomplete: one or more gates failed or were blocked.' -ForegroundColor Red
        }
        return $exitCode
    } finally {
        Pop-Location
        # Remove only the outputs owned by this invocation, never source or global caches.
        foreach ($name in @('bin', 'release-targets', 'coverage.out')) {
            Remove-Item -LiteralPath (Join-Path $temp $name) -Recurse -Force -ErrorAction SilentlyContinue
        }
    }
}
