# Install or update the nau binary on Windows, from a ref.
# Re-running the script with another -Ref updates the node to that ref.
# Pin an automatic backend selection to its absolute path on each install.
#
# Prerequisites: Go 1.26 or newer on the PATH. The script never prints
# environment variables or configuration contents.

[CmdletBinding()]
param(
    [string]$Ref = 'main',
    [string]$BinDir = '',
    [string]$MachineLabel = '',
    [switch]$Help
)

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

$ModulePath = 'github.com/djalmajr/nau/cmd/nau'

function Show-Usage {
    @'
Usage: powershell -File scripts\install.ps1 [-Ref <ref>] [-BinDir <dir>] [-MachineLabel <label>] [-Help]

Install or update the nau binary into the bin directory, from the ref.
Updating is re-running the script with another -Ref.
An automatic backend selection is pinned to its absolute path on the PATH.

Options:
  -Ref <ref>           ref to install (default: main)
  -BinDir <dir>        bin directory (default: %USERPROFILE%\.local\bin)
  -MachineLabel <lbl>  set machine_label when it is still empty
  -Help                show this help
'@
}

function Fail([string]$Message) {
    Write-Host "nau install: $Message"
    exit 1
}

function Set-BackendPin {
    # Go owns classification, path safety and the locked conditional write.
    $null = Invoke-Quiet $NauTarget @('config', 'list')
    if ($LASTEXITCODE -ne 0) {
        Write-Host 'nau install: warning: cannot read the config file; herdr_soho_bin unchanged'
        return
    }
    $backend = Get-Command soho -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($backend) {
        # Native stderr warnings must pass through even in Windows PowerShell 5.1.
        $pinPreference = $ErrorActionPreference
        try {
            $ErrorActionPreference = 'Continue'
            $null = & $NauTarget config set --if-automatic herdr_soho_bin $backend.Source
            $pinExit = $LASTEXITCODE
        }
        finally { $ErrorActionPreference = $pinPreference }
        if ($pinExit -ne 0) { Fail 'nau config set herdr_soho_bin failed' }
        return
    }
    Write-Host 'nau install: warning: soho not found on the PATH; herdr_soho_bin stays automatic, and an ssh forced command (nau gate) may not find the backend'
}

# Windows PowerShell 5.1 turns the redirected stderr of a native command
# into a terminating error under 'Stop'. Run a native command whose
# stderr is discarded under 'Continue' and judge it by its exit code.
function Invoke-Quiet([string]$FilePath, [string[]]$Arguments) {
    $ErrorActionPreference = 'Continue'
    & $FilePath @Arguments 2>$null
}

if ($Help) {
    Show-Usage
    exit 0
}

if ($BinDir -eq '') {
    $BinDir = Join-Path $env:USERPROFILE '.local\bin'
}

# Normalize once, right after the parameters are read and before the
# validations, and use that value everywhere: the process-owner check
# needs an absolute path, and a relative one would not resolve against
# the task's working directory.
$BinDir = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($BinDir)

$Go = Get-Command go -ErrorAction SilentlyContinue
if (-not $Go) {
    Fail 'go not found on the PATH; install Go 1.26 or newer first'
}

# A dedicated temp dir, removed on any exit: the fresh binary is built
# there before the installed one is touched.
$Tmp = Join-Path $env:TEMP ('nau-install-' + $PID + '-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $Tmp | Out-Null
try {
    Write-Host "nau install: installing $ModulePath@$Ref"
    $env:GOBIN = $Tmp
    $env:GOPROXY = 'direct'
    go install "$ModulePath`@$Ref"
    if ($LASTEXITCODE -ne 0) {
        Fail ("go install failed (exit " + $LASTEXITCODE + ')')
    }

    # Test the new binary before touching the installed one.
    Write-Host 'nau install: checking the new binary'
    $NauNew = Join-Path $Tmp 'nau.exe'
    if (-not (Test-Path -LiteralPath $NauNew)) {
        Fail 'go install did not produce the new binary'
    }
    $null = Invoke-Quiet $NauNew @('version')
    if ($LASTEXITCODE -ne 0) {
        Fail 'the new binary failed ''nau version''; the installed binary is untouched'
    }

    # Replace the installed binary: Windows allows renaming an exe that
    # is in use, so move the old one aside first, then move the new one
    # into place.
    if (-not (Test-Path -LiteralPath $BinDir)) {
        New-Item -ItemType Directory -Path $BinDir -Force | Out-Null
    }
    $NauTarget = Join-Path $BinDir 'nau.exe'
    $Stamp = (Get-Date -Format 'yyyyMMddHHmmss') + '-' + $PID
    # Stage the new binary next to the target first, on the same volume,
    # so the swap below is two renames in one directory.
    $NauStage = Join-Path $BinDir ('.nau.new-' + $Stamp + '.exe')
    $Backup = ''
    try {
        Copy-Item -LiteralPath $NauNew -Destination $NauStage
        if (Test-Path -LiteralPath $NauTarget) {
            $Backup = 'nau.exe.old-' + $Stamp
            Rename-Item -LiteralPath $NauTarget -NewName $Backup
        }
        Rename-Item -LiteralPath $NauStage -NewName 'nau.exe'
    }
    catch {
        # Put the previous binary back under its name, drop the stage
        # and stop: the node keeps the nau it had.
        if ($Backup -ne '' -and -not (Test-Path -LiteralPath $NauTarget)) {
            Rename-Item -LiteralPath (Join-Path $BinDir $Backup) -NewName 'nau.exe' -ErrorAction SilentlyContinue
        }
        Remove-Item -LiteralPath $NauStage -ErrorAction SilentlyContinue
        Fail 'could not replace the installed binary; the previous one was kept'
    }

    # Set machine_label only when it is still empty; never print the value.
    if ($MachineLabel -ne '') {
        $current = Invoke-Quiet $NauTarget @('config', 'get', 'machine_label')
        if ($LASTEXITCODE -ne 0) {
            $current = ''
        }
        $currentValue = ''
        $match = [regex]::Match(($current -join "`n"), '"value":"([^"]*)"')
        if ($match.Success) {
            $currentValue = $match.Groups[1].Value
        }
        if ($currentValue -eq '') {
            $null = & $NauTarget config set machine_label $MachineLabel
            if ($LASTEXITCODE -ne 0) {
                Fail ("nau config set machine_label failed (exit " + $LASTEXITCODE + ')')
            }
            Write-Host 'nau install: machine_label set'
        }
        else {
            Write-Host 'nau install: machine_label is already set; keeping the current value'
        }
    }

    Set-BackendPin

    # Warn, without failing, when the bin dir is not on the PATH.
    $inPath = $false
    foreach ($entry in ($env:Path -split ';')) {
        if ($entry -ne '' -and $entry -ieq $BinDir) {
            $inPath = $true
            break
        }
    }
    if (-not $inPath) {
        Write-Host ('nau install: warning: ' + $BinDir + ' is not on the PATH; add it to use nau')
    }

    Write-Host 'nau install: installed'
    & $NauTarget version
    if ($LASTEXITCODE -ne 0) {
        Fail ('nau version failed (exit ' + $LASTEXITCODE + ')')
    }
    # doctor is informational: its non-zero exit (for example a missing
    # backend) must not fail the install.
    & $NauTarget doctor
    if ($LASTEXITCODE -ne 0) {
        Write-Host 'nau install: doctor reported a problem (informational); see the output above'
    }
}
finally {
    # Remove the old binaries and leftover stages that are not in use;
    # the ones still in use cannot be removed and are silently ignored.
    if (Test-Path -LiteralPath $BinDir) {
        Get-ChildItem -LiteralPath $BinDir -File | Where-Object { $_.Name -like 'nau.exe.old-*' -or $_.Name -like '.nau.new-*.exe' } | ForEach-Object {
            try {
                Remove-Item -LiteralPath $_.FullName -ErrorAction Stop
            }
            catch {
                # In use; leave it for the next run.
            }
        }
    }
    # A fresh exe can stay locked for a moment after it ran (an antivirus
    # scan, for example): retry the removal briefly before giving up.
    for ($i = 0; $i -lt 5 -and (Test-Path -LiteralPath $Tmp); $i++) {
        Remove-Item -LiteralPath $Tmp -Recurse -Force -ErrorAction SilentlyContinue
        if (Test-Path -LiteralPath $Tmp) {
            Start-Sleep -Milliseconds 500
        }
    }
}
