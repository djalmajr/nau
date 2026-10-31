# Install or update the nau binary on Windows, from a ref.
# Re-running the script with another -Ref updates the node to that ref.
# Pin an automatic backend selection to its absolute path on each install.
#
# On Windows, -Agent installs (or rewrites) the nau agent scheduled
# task and (re)starts it; the task is created only when hub.json exists.
#
# Prerequisites: Go 1.26 or newer on the PATH. The script never prints
# environment variables or configuration contents.

[CmdletBinding()]
param(
    [string]$Ref = 'main',
    [string]$BinDir = '',
    [string]$MachineLabel = '',
    [switch]$Agent,
    [switch]$RemoveAgent,
    [switch]$AgentDryRun,
    [switch]$Help
)

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

$ModulePath = 'github.com/djalmajr/nau/cmd/nau'

function Show-Usage {
    @'
Usage: powershell -File scripts\install.ps1 [-Ref <ref>] [-BinDir <dir>] [-MachineLabel <label>] [-Agent] [-RemoveAgent] [-AgentDryRun] [-Help]

Install or update the nau binary into the bin directory, from the ref.
Updating is re-running the script with another -Ref.
On Windows, -Agent installs the nau agent scheduled task and (re)starts
it; it is created only when the config directory holds a hub.json.
An automatic backend selection is pinned to its absolute path on the PATH.

Options:
  -Ref <ref>           ref to install (default: main)
  -BinDir <dir>        bin directory (default: %USERPROFILE%\.local\bin)
  -MachineLabel <lbl>  set machine_label when it is still empty
  -Agent               install or rewrite the nau agent scheduled task and (re)start it
  -RemoveAgent         stop and remove the nau agent scheduled task
  -AgentDryRun         print what -Agent would install (executable, arguments, triggers, settings, one per line) and exit without touching anything
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
    Write-Host 'nau install: warning: soho not found on the PATH; herdr_soho_bin stays automatic'
}

# Windows PowerShell 5.1 turns the redirected stderr of a native command
# into a terminating error under 'Stop'. Run a native command whose
# stderr is discarded under 'Continue' and judge it by its exit code.
function Invoke-Quiet([string]$FilePath, [string[]]$Arguments) {
    $ErrorActionPreference = 'Continue'
    & $FilePath @Arguments 2>$null
}

# The nau agent user service: a scheduled task in the interactive user
# session, at the root task path. It is never a child of this script:
# an ssh session is session 0 and dies with the connection.
$AgentTaskName = 'nau agent'
$AgentTaskPath = '\'

function Get-AgentConfigDir {
    # the same directory os.UserConfigDir + 'nau' resolves on Windows
    return (Join-Path $env:APPDATA 'nau')
}

function Get-AgentHubFile {
    return (Join-Path (Get-AgentConfigDir) 'hub.json')
}

function Get-AgentStateFile {
    return (Join-Path (Get-AgentConfigDir) 'agent-state.json')
}

function Get-AgentLogPath {
    return (Join-Path $env:LOCALAPPDATA 'nau\agent.log')
}

function Get-AgentWrapperPath {
    return (Join-Path $env:LOCALAPPDATA 'nau\agent.cmd')
}

# The two wrapper lines with the expanded paths: conhost --headless
# re-tokenizes the client line and breaks nested quotes, so the task
# action runs this .cmd instead of quoting the paths itself.
function Get-AgentWrapperLines {
    $nau = Join-Path $BinDir 'nau.exe'
    $log = Get-AgentLogPath
    return @('@echo off', ('"' + $nau + '" agent run 1>>"' + $log + '" 2>&1'))
}

# The conhost action arguments: the wrapper path, expanded, is the
# only quoted argument, so a path with spaces works, and the hidden
# conhost window keeps the task out of the taskbar.
function Get-AgentActionArguments {
    return ('--headless cmd.exe /d /c "' + (Get-AgentWrapperPath) + '"')
}

# Write the wrapper .cmd atomically (temp file in the same folder, then
# Replace over the existing wrapper or Move on first creation) in
# ASCII with CRLF; on failure the stage is dropped and the previous
# wrapper is kept.
function Write-AgentWrapper {
    $wrapper = Get-AgentWrapperPath
    $dir = [System.IO.Path]::GetDirectoryName($wrapper)
    if (-not (Test-Path -LiteralPath $dir)) {
        New-Item -ItemType Directory -Path $dir -Force | Out-Null
    }
    $stage = Join-Path $dir ('.agent.cmd.' + $PID + '.tmp')
    $text = ((Get-AgentWrapperLines) -join "`r`n") + "`r`n"
    [System.IO.File]::WriteAllText($stage, $text, (New-Object System.Text.ASCIIEncoding))
    try {
        if (Test-Path -LiteralPath $wrapper) {
            # [NullString]::Value, not $null: PowerShell turns $null into an
            # empty string for a .NET string parameter, and Replace refuses it.
            [System.IO.File]::Replace($stage, $wrapper, [NullString]::Value)
        }
        else {
            [System.IO.File]::Move($stage, $wrapper)
        }
    }
    catch {
        Remove-Item -LiteralPath $stage -ErrorAction SilentlyContinue
        Fail 'could not install the agent wrapper; the previous wrapper was kept'
    }
}

# Refuse (exit 2) when -Agent would have to store a character above
# 0x7F in the ASCII wrapper (it would be written as '?', pointing the
# task at another path): a non-ASCII character in the bin dir or in
# LOCALAPPDATA.
function Test-AgentNonAscii {
    foreach ($value in @($BinDir, $env:LOCALAPPDATA)) {
        foreach ($char in $value.ToCharArray()) {
            if ([int]$char -gt 0x7F) {
                Write-Host 'nau install: -Agent is refused: the bin dir or LOCALAPPDATA contains a non-ASCII character, which the service wrapper cannot hold'
                exit 2
            }
        }
    }
}

# Refuse (exit 2) when -Agent cannot build a safe cmd.exe command
# line: a double quote or a percent sign in the bin dir or in
# LOCALAPPDATA.
function Test-AgentQuotable {
    $bad = @()
    if ($BinDir.IndexOf('"') -ge 0 -or $BinDir.IndexOf('%') -ge 0) {
        $bad += 'the bin dir'
    }
    if ($env:LOCALAPPDATA.IndexOf('"') -ge 0 -or $env:LOCALAPPDATA.IndexOf('%') -ge 0) {
        $bad += 'the LOCALAPPDATA path'
    }
    if ($bad.Count -gt 0) {
        Write-Host ('nau install: -Agent is refused: ' + ($bad -join ' and ') + ' contains a double quote or a percent sign, which cmd.exe cannot quote safely')
        exit 2
    }
}

# Print what -Agent would install, one item per line, and touch nothing
# (this is also the -AgentDryRun output): the wrapper content and the
# action, the triggers and the settings.
function Show-AgentDryRun {
    'nau install: agent dry run (nothing is installed or started)'
    'wrapper: ' + (Get-AgentWrapperPath)
    foreach ($line in (Get-AgentWrapperLines)) {
        'wrapper line: ' + $line
    }
    'executable: conhost.exe'
    'arguments: ' + (Get-AgentActionArguments)
    'trigger: at logon of the current user'
    'trigger: once, now, repeating every 5 minutes without a repetition duration'
    'settings: MultipleInstances=IgnoreNew'
    'settings: ExecutionTimeLimit=zero (no limit)'
    'settings: AllowStartIfOnBatteries'
    'settings: DontStopIfGoingOnBatteries'
}

# Register (or replace, with -Force) the agent scheduled task: at logon
# plus a one-shot repetition every 5 minutes (a dead agent is re-erected
# on the next repetition), hidden through the conhost action that runs
# the wrapper.
function Register-AgentTask {
    $principal = New-ScheduledTaskPrincipal -UserId $env:USERNAME -LogonType Interactive -RunLevel Limited
    $triggers = @(
        (New-ScheduledTaskTrigger -AtLogOn -User $env:USERNAME)
        (New-ScheduledTaskTrigger -Once -At (Get-Date) -RepetitionInterval (New-TimeSpan -Minutes 5))
    )
    $settings = New-ScheduledTaskSettingsSet -MultipleInstances IgnoreNew -ExecutionTimeLimit (New-TimeSpan -Seconds 0) -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries
    $action = New-ScheduledTaskAction -Execute 'conhost.exe' -Argument (Get-AgentActionArguments)
    $task = New-ScheduledTask -Settings $settings -Trigger $triggers -Action $action -Principal $principal
    Register-ScheduledTask -TaskName $AgentTaskName -TaskPath $AgentTaskPath -InputObject $task -Force | Out-Null
}

# Restart the agent service, in order: write the wrapper, register (or
# replace) the task, stop the task (which kills the running agent),
# start the task, and wait for a fresh health file under a wall deadline
# of 20 s. The agent writes the health file before its first read, so a
# file newer than the restart moment proves the service started.
function Restart-AgentTask {
    Write-AgentWrapper
    Register-AgentTask
    Stop-ScheduledTask -TaskName $AgentTaskName -TaskPath $AgentTaskPath -ErrorAction SilentlyContinue
    $since = Get-Date
    Start-ScheduledTask -TaskName $AgentTaskName -TaskPath $AgentTaskPath
    $deadline = $since.AddSeconds(20)
    $stateFile = Get-AgentStateFile
    while ((Get-Date) -lt $deadline) {
        if ((Test-Path -LiteralPath $stateFile) -and ((Get-Item -LiteralPath $stateFile).LastWriteTime -ge $since)) {
            Write-Host 'nau install: agent running'
            return
        }
        Start-Sleep -Milliseconds 500
    }
    Fail ('the agent did not write a fresh health file; see ' + (Get-AgentLogPath))
}

# Stop and remove the agent scheduled task, and only it: a task that is
# not registered is skipped, and the wrapper is removed with it. The
# log is left in place.
function Remove-AgentTask {
    Stop-ScheduledTask -TaskName $AgentTaskName -TaskPath $AgentTaskPath -ErrorAction SilentlyContinue
    Unregister-ScheduledTask -TaskName $AgentTaskName -TaskPath $AgentTaskPath -Confirm:$false -ErrorAction SilentlyContinue
    Remove-Item -LiteralPath (Get-AgentWrapperPath) -ErrorAction SilentlyContinue
    Write-Host 'nau install: agent service removed'
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

if ($Agent -or $RemoveAgent -or $AgentDryRun) {
    if (-not $env:LOCALAPPDATA) {
        Write-Host 'nau install: -Agent needs LOCALAPPDATA'
        exit 2
    }
    if (-not $env:APPDATA) {
        Write-Host 'nau install: -Agent needs APPDATA'
        exit 2
    }
    Test-AgentNonAscii
    Test-AgentQuotable
}

# -AgentDryRun prints the task definition and exits 0 before the go
# install, without touching anything (it wins over -Agent when both
# are given). Without hub.json it prints the same refusal -Agent would
# take.
if ($AgentDryRun) {
    if (Test-Path -LiteralPath (Get-AgentHubFile)) {
        Show-AgentDryRun
    }
    else {
        Write-Host ('nau install: no hub.json in ' + (Get-AgentConfigDir) + '; run nau connect <hub-url> first; the agent service would not be installed')
    }
    exit 0
}

# -RemoveAgent stops and removes the task and exits, without installing
# anything.
if ($RemoveAgent) {
    Remove-AgentTask
    exit 0
}

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

    # nau agent service: -Agent registers (or replaces) the task and
    # (re)starts it, but only when the config directory holds a hub.json;
    # without it, warn to run nau connect first and keep the node usable.
    # This only happens after a successful swap: a failed go install
    # never touches the service.
    if ($Agent) {
        if (-not (Test-Path -LiteralPath (Get-AgentHubFile))) {
            Write-Host ('nau install: no hub.json in ' + (Get-AgentConfigDir) + '; run nau connect <hub-url> first; the agent service was not installed')
        }
        else {
            Restart-AgentTask
        }
    }

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
    # doctor is informational: its non-zero exit must not fail the install.
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
