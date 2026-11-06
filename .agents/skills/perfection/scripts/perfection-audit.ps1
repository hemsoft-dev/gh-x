# Run local CI gates independently; hosted qualification remains in GitHub.
param([string]$ReportPath)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Show-Section {
    param([Parameter(Mandatory)][string]$Name)

    Write-Host "`n=== $Name ===" -ForegroundColor Cyan
}

function Invoke-CheckedCommand {
    param(
        [Parameter(Mandatory)][string]$Name,
        [Parameter(Mandatory)][string]$Executable,
        [Parameter()][string[]]$Arguments = @()
    )

    Show-Section $Name
    & $Executable @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "$Name failed with exit code $LASTEXITCODE."
    }
}

function Invoke-NoOutputCommand {
    param(
        [Parameter(Mandatory)][string]$Name,
        [Parameter(Mandatory)][string]$Executable,
        [Parameter()][string[]]$Arguments = @()
    )

    Show-Section $Name
    $output = @(& $Executable @Arguments 2>&1)
    $exitCode = $LASTEXITCODE
    if ($output.Count -gt 0) {
        $output | ForEach-Object { Write-Host $_ }
    }
    if ($exitCode -ne 0) {
        throw "$Name failed with exit code $exitCode."
    }
    if ($output.Count -gt 0) {
        throw "$Name produced findings."
    }
}

function Assert-NoSourceMatches {
    param(
        [Parameter(Mandatory)][string]$Name,
        [Parameter(Mandatory)][System.IO.FileInfo[]]$Files,
        [Parameter(Mandatory)][string]$Pattern
    )

    Show-Section $Name
    $matches = @(Select-String -LiteralPath $Files.FullName -Pattern $Pattern)
    if ($matches.Count -gt 0) {
        $matches | ForEach-Object { Write-Host $_ }
        throw "$Name produced findings."
    }
}

function Assert-PinnedToolVersions {
    param(
        [Parameter(Mandatory)][string]$VersionFile,
        [Parameter(Mandatory)][string]$ToolName
    )

    Show-Section "pinned analyzer version: $ToolName"
    $pins = @{}
    foreach ($line in Get-Content -LiteralPath $VersionFile) {
        if ($line -match '^(?<name>[A-Z0-9_]+)=(?<version>\S+)$') {
            $pins[$Matches.name] = $Matches.version
        }
    }

    $tools = @(
        @{ Name = 'staticcheck'; Module = 'honnef.co/go/tools'; Pin = 'STATICCHECK_VERSION' },
        @{ Name = 'gocritic'; Module = 'github.com/go-critic/go-critic'; Pin = 'GOCRITIC_VERSION' },
        @{ Name = 'errcheck'; Module = 'github.com/kisielk/errcheck'; Pin = 'ERRCHECK_VERSION' },
        @{ Name = 'deadcode'; Module = 'golang.org/x/tools'; Pin = 'DEADCODE_VERSION' },
        @{ Name = 'govulncheck'; Module = 'golang.org/x/vuln'; Pin = 'GOVULNCHECK_VERSION' },
        @{ Name = 'gocyclo'; Module = 'github.com/fzipp/gocyclo'; Pin = 'GOCYCLO_VERSION' },
        @{ Name = 'gocognit'; Module = 'github.com/uudashr/gocognit'; Pin = 'GOCOGNIT_VERSION' },
        @{ Name = 'gremlins'; Module = 'github.com/go-gremlins/gremlins'; Pin = 'GREMLINS_VERSION' }
    )

    foreach ($tool in @($tools | Where-Object Name -EQ $ToolName)) {
        $expected = $pins[$tool.Pin]
        if (-not $expected) {
            throw "Missing $($tool.Pin) in $VersionFile."
        }

        $commandPath = (Get-Command $tool.Name -ErrorAction Stop).Source
        $metadata = @(& go version -m $commandPath 2>&1)
        if ($LASTEXITCODE -ne 0) {
            $metadata | ForEach-Object { Write-Host $_ }
            throw "Could not inspect $($tool.Name)."
        }

        $modulePattern = '^\s*mod\s+' + [regex]::Escape($tool.Module) + '\s+(?<version>\S+)'
        $moduleLine = $metadata | Where-Object { [string]$_ -match $modulePattern } | Select-Object -First 1
        if (-not $moduleLine -or [string]$moduleLine -notmatch $modulePattern) {
            throw "$($tool.Name) was not built from $($tool.Module)."
        }

        $actual = $Matches.version
        if ($actual -ne $expected) {
            throw "$($tool.Name) is $actual; expected $expected from .github/quality-tools.env."
        }
        Write-Host "$($tool.Name) $actual"
    }

    return $pins
}

function Assert-CrapThreshold {
    param(
        [Parameter(Mandatory)][string]$CoveragePath,
        [Parameter(Mandatory)][double]$Threshold
    )

    Show-Section "CRAP score < $Threshold"

    $complexityLines = @(& gocyclo -ignore '_test\.go' . .github/scripts 2>&1)
    if ($LASTEXITCODE -ne 0) {
        $complexityLines | ForEach-Object { Write-Host $_ }
        throw 'gocyclo failed while calculating CRAP scores.'
    }

    $coverageLines = @(& go tool cover "-func=$CoveragePath" 2>&1)
    if ($LASTEXITCODE -ne 0) {
        $coverageLines | ForEach-Object { Write-Host $_ }
        throw 'go tool cover failed while calculating CRAP scores.'
    }

    $module = (& go list -m).Trim()
    if ($LASTEXITCODE -ne 0) { throw 'Cannot resolve module for coverage paths.' }
    $failures = @(Get-CrapFindings $complexityLines $coverageLines $module $Threshold)

    if ($failures.Count -gt 0) {
        $failures | Sort-Object CRAP -Descending | Format-Table -AutoSize | Out-Host
        throw "$($failures.Count) function(s) have CRAP score >= $Threshold."
    }

    Write-Host "All functions have CRAP score below $Threshold."
}

function Get-CrapFindings {
    param([string[]]$ComplexityLines, [string[]]$CoverageLines, [string]$Module, [double]$Threshold)
    $coverageByFunction = @{}
    foreach ($line in $coverageLines) {
        $text = [string]$line
        if ($text -match '^(?<file>.+\.go):(?<line>\d+):\s+(?<function>\S+)\s+(?<coverage>[0-9.]+)%$') {
            $coverageFile = $Matches.file.Replace('\', '/')
            if (-not $coverageFile.StartsWith($Module + '/', [StringComparison]::Ordinal)) { throw "Coverage file outside module: $coverageFile" }
            $relativeFile = $coverageFile.Substring($Module.Length + 1)
            $coverageByFunction["$relativeFile|$($Matches.line)"] = [double]$Matches.coverage
        }
    }

    $failures = @()
    foreach ($line in $complexityLines) {
        $parts = ([string]$line -split '\s+')
        if ($parts.Count -lt 4) {
            continue
        }

        $complexity = 0
        if (-not [int]::TryParse($parts[0], [ref]$complexity)) {
            continue
        }

        $functionName = $parts[2]
        if ($functionName -match '^\([^)]+\)\.(.+)$') {
            $functionName = $Matches[1]
        } elseif ($functionName.Contains('.')) {
            $functionName = $functionName.Split('.', 2)[1]
        }

        if ($parts[3] -notmatch '^(?<file>.+\.go):(?<line>\d+):\d+$') { throw "Invalid complexity location: $($parts[3])" }
        $fileName = $Matches.file.Replace('\', '/') -replace '^\./', ''
        $key = "$fileName|$($Matches.line)"
        $coverage = if ($coverageByFunction.ContainsKey($key)) {
            [double]$coverageByFunction[$key]
        } else {
            0.0
        }
        $uncovered = 1.0 - ($coverage / 100.0)
        $crap = ([math]::Pow($complexity, 2) * [math]::Pow($uncovered, 3)) + $complexity
        if ($crap -ge $Threshold) {
            $failures += [pscustomobject]@{
                CRAP = [math]::Round($crap, 1)
                Complexity = $complexity
                Coverage = $coverage
                Function = $functionName
                File = $fileName
            }
        }
    }

    return $failures
}

. (Join-Path $PSScriptRoot 'audit-plan.ps1')

# Dot-sourcing exposes the same executor to offline regression tests.
if ($MyInvocation.InvocationName -ne '.') {
    exit (Invoke-LocalAudit -ReportPath $ReportPath)
}
