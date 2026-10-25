param(
    [Parameter(Mandatory)][string]$SourcePath,
    [Parameter(Mandatory)][string]$AssemblyPath,
    [Parameter(Mandatory)][string]$ExpectedSourceHash
)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
if ([Environment]::OSVersion.Platform -ne 'Win32NT') { throw 'Dashboard launcher preparation requires Windows.' }
if (Test-Path -LiteralPath $AssemblyPath -PathType Leaf) { return }
$framework = if ([Environment]::Is64BitProcess) { 'Framework64' } else { 'Framework' }
$compiler = Join-Path $env:WINDIR "Microsoft.NET\$framework\v4.0.30319\csc.exe"
if (-not (Test-Path -LiteralPath $compiler -PathType Leaf)) { throw 'Windows .NET Framework C# compiler is unavailable.' }
$directory = Split-Path -Parent $AssemblyPath
New-Item -ItemType Directory -Path $directory -Force | Out-Null
$temporary = Join-Path $directory "$([guid]::NewGuid().ToString('N')).dll"
$snapshot = [IO.Path]::ChangeExtension($temporary, '.cs')
$process = $null
try {
    $bytes = [IO.File]::ReadAllBytes($SourcePath)
    $sha = [Security.Cryptography.SHA256]::Create()
    try { $actualHash = [BitConverter]::ToString($sha.ComputeHash($bytes)).Replace('-', '') }
    finally { $sha.Dispose() }
    if ($actualHash -cne $ExpectedSourceHash) { throw 'Dashboard launcher source changed during preparation; nothing was published.' }
    [IO.File]::WriteAllBytes($snapshot, $bytes)
    # Windows paths cannot contain quotes; both paths end in file names, not slashes.
    $start = New-Object Diagnostics.ProcessStartInfo
    $start.FileName = $compiler
    $start.Arguments = "/nologo /target:library /optimize+ /out:`"$temporary`" `"$snapshot`""
    $start.UseShellExecute = $false
    $start.CreateNoWindow = $true
    $start.RedirectStandardInput = $true
    $start.RedirectStandardOutput = $true
    $start.RedirectStandardError = $true
    $process = [Diagnostics.Process]::Start($start)
    $process.StandardInput.Close()
    $stdout = $process.StandardOutput.ReadToEndAsync()
    $stderr = $process.StandardError.ReadToEndAsync()
    if (-not $process.WaitForExit(30000)) {
        $process.Kill()
        $process.WaitForExit()
        throw 'Windowless launcher preparation timed out.'
    }
    $output = $stdout.GetAwaiter().GetResult()
    $errorText = $stderr.GetAwaiter().GetResult()
    if ($process.ExitCode -ne 0) { throw "Windowless launcher preparation failed: $output $errorText" }
    # Publish complete bytes only; never overwrite another concurrent preparation.
    if (-not (Test-Path -LiteralPath $AssemblyPath)) { Move-Item -LiteralPath $temporary -Destination $AssemblyPath }
} finally {
    if ($null -ne $process) { $process.Dispose() }
    if (Test-Path -LiteralPath $temporary) { Remove-Item -LiteralPath $temporary -Force }
    if (Test-Path -LiteralPath $snapshot) { Remove-Item -LiteralPath $snapshot -Force }
}
