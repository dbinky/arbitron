#Requires -Version 5.1
<#
.SYNOPSIS
    Arbitron Windows local installer (P3).

.DESCRIPTION
    Installs Arbitron for the current user under %LOCALAPPDATA%\Arbitron with a
    per-user at-logon Scheduled Task as the service mechanism. No elevation is
    required. The flow mirrors the Unix install.sh local path exactly:

        detect OS/arch -> install type = Local -> detect existing install ->
        Fresh | Upgrade | Reconfigure | Uninstall

    Entry point usage:
        irm https://raw.githubusercontent.com/dbinky/arbitron/main/install.ps1 | iex

.NOTES
    This script is dot-sourceable for Pester: set $env:ARBITRON_INSTALL_NOEXEC
    to any value and dot-source it to define functions without running
    Invoke-Main.
#>
[CmdletBinding()]
param(
    [string]$Version = $env:ARBITRON_VERSION,
    [string]$Config  = $env:ARBITRON_CONFIG,
    [switch]$Yes
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

# --------------------------------------------------------------------------
# Script-level constants and state
# --------------------------------------------------------------------------

# The GitHub repository hosting releases + raw scripts. The Go module path
# github.com/arbitron/arbitron is an IMPORT PATH, never a URL; every download
# URL is built from $ArbitronRepo. Overridable for testing/forks.
$script:ArbitronRepo = if ($env:ARBITRON_REPO) { $env:ARBITRON_REPO } else { 'dbinky/arbitron' }

# Service / scheduled-task name (single source of truth).
$script:ArbitronTaskName = 'Arbitron'

# Release version to install/upgrade to. P5 Task 1/2 finalize the resolver
# (Get-ArbitronVersion); for P3 we honor -Version / $env:ARBITRON_VERSION and
# fall back to 'latest' (the flow functions accept an explicit -Version too).
$script:ResolvedVersion = if ($Version) { $Version } else { 'latest' }

# Explicit config-path override (-Config / ARBITRON_CONFIG); empty = default
# %LOCALAPPDATA%\Arbitron\config.yaml.
$script:Config = $Config

# Non-interactive flag: honored by all prompt helpers. Seeded from -Yes /
# env so dot-sourced tests can toggle $script:ArbitronNonInteractive directly.
$script:ArbitronNonInteractive = [bool]$Yes -or [bool]$env:ARBITRON_YES

# --------------------------------------------------------------------------
# Windows-cmdlet shims (cross-platform test seams)
# --------------------------------------------------------------------------
# The ScheduledTasks / Appx cmdlets exist only on Windows. To keep install.ps1
# dot-sourceable and its Pester suite runnable on non-Windows CI/dev hosts (and
# so Pester can Mock these names with the same parameter contract everywhere),
# define thin shims for any that are absent. Each shim carries the parameters the
# installer actually passes so a Mock's $PSBoundParameters captures them
# identically on Windows and elsewhere. On a real Windows host the genuine
# cmdlets are present, so these definitions never load.
# The shim bodies carry the parameter contracts the installer uses; they are
# registered via Set-Item Function: so they survive Pester's dot-source-inside-
# a-function scoping (a plain `function` keyword would scope to BeforeAll only).
$script:ArbitronShimDefs = @{
    'Get-ScheduledTask'            = { [CmdletBinding()] param([string]$TaskName) throw 'Get-ScheduledTask is only available on Windows.' }
    'Register-ScheduledTask'       = { [CmdletBinding()] param([string]$TaskName, $Action, $Trigger, $Principal, $Settings, [switch]$Force) throw 'Register-ScheduledTask is only available on Windows.' }
    'Unregister-ScheduledTask'     = { [CmdletBinding()] param([string]$TaskName, [switch]$Confirm) throw 'Unregister-ScheduledTask is only available on Windows.' }
    'Start-ScheduledTask'          = { [CmdletBinding()] param([string]$TaskName) throw 'Start-ScheduledTask is only available on Windows.' }
    'Stop-ScheduledTask'           = { [CmdletBinding()] param([string]$TaskName) throw 'Stop-ScheduledTask is only available on Windows.' }
    'New-ScheduledTaskAction'      = { [CmdletBinding()] param([string]$Execute, [string]$Argument) throw 'New-ScheduledTaskAction is only available on Windows.' }
    'New-ScheduledTaskTrigger'     = { [CmdletBinding()] param([switch]$AtLogOn) throw 'New-ScheduledTaskTrigger is only available on Windows.' }
    'New-ScheduledTaskPrincipal'   = { [CmdletBinding()] param([string]$UserId, [string]$LogonType, [string]$RunLevel) throw 'New-ScheduledTaskPrincipal is only available on Windows.' }
    'New-ScheduledTaskSettingsSet' = { [CmdletBinding()] param([switch]$AllowStartIfOnBatteries, [switch]$DontStopIfGoingOnBatteries) throw 'New-ScheduledTaskSettingsSet is only available on Windows.' }
    'Add-AppxPackage'              = { [CmdletBinding()] param([string]$Path) throw 'Add-AppxPackage is only available on Windows.' }
}
foreach ($shimName in $script:ArbitronShimDefs.Keys) {
    if (-not (Get-Command -Name $shimName -ErrorAction SilentlyContinue)) {
        Set-Item -Path "Function:$shimName" -Value $script:ArbitronShimDefs[$shimName]
    }
}

# --------------------------------------------------------------------------
# Platform / architecture detection
# --------------------------------------------------------------------------

# Test-IsWindows is a seam so Assert-SupportedPlatform is mockable on non-Windows
# CI/dev hosts. On PS7 $IsWindows exists; on PS5.1 it is always Windows.
function Test-IsWindows {
    [CmdletBinding()]
    param()
    if (Get-Variable -Name IsWindows -Scope Global -ErrorAction SilentlyContinue) {
        return [bool]$IsWindows
    }
    # PowerShell 5.1 only runs on Windows.
    return $true
}

function Assert-SupportedPlatform {
    [CmdletBinding()]
    param()
    if (-not (Test-IsWindows)) {
        throw 'install.ps1 supports Windows only. Use install.sh on macOS/Linux.'
    }
}

function Get-Architecture {
    [CmdletBinding()]
    param(
        [string]$Raw = $env:PROCESSOR_ARCHITECTURE
    )
    switch ($Raw.ToUpperInvariant()) {
        'AMD64' { return 'amd64' }
        'X64'   { return 'amd64' }
        'ARM64' { return 'arm64' }
        default { throw "Unsupported processor architecture: '$Raw'." }
    }
}

# Get-InstallType returns an install-type "enum" string. P3 always yields Local
# (local needs no elevation); the function exists so P4 can extend it to Shared
# without touching call sites.
function Get-InstallType {
    [CmdletBinding()]
    param()
    return 'Local'
}

# Single source of truth for every filesystem path. P4 adds a 'Shared' branch
# here (ProgramFiles/ProgramData) without editing any call site.
function Get-ArbitronPaths {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][ValidateSet('Local', 'Shared')][string]$InstallType
    )
    switch ($InstallType) {
        'Local' {
            $root = Join-Path $env:LOCALAPPDATA 'Arbitron'
            return @{
                Root            = $root
                Bin             = Join-Path $root 'bin'
                BinaryExe       = Join-Path $root 'bin\arbitron.exe'
                Config          = Join-Path $root 'config.yaml'
                Data            = Join-Path $root 'data'
                Workspaces      = Join-Path $root 'workspaces'
                Logs            = Join-Path $root 'logs'
                LogFile         = Join-Path $root 'logs\arbitron.log'
                ClaudeConfigDir = Join-Path $root 'claude'
                NpmPrefix       = Join-Path $root 'npm'
                Runtime         = Join-Path $root 'runtime'
            }
        }
        'Shared' {
            throw 'Shared install-type is delivered by P4; not available in install.ps1 (P3).'
        }
    }
}

# --------------------------------------------------------------------------
# Logging (with secret redaction)
# --------------------------------------------------------------------------

function Write-ArbitronLog {
    [CmdletBinding()]
    param(
        [ValidateSet('Info', 'Warn', 'Error')][string]$Level = 'Info',
        [Parameter(Mandatory)][string]$Message,
        # When supplied, the secret value is NEVER written to output. The message
        # itself must not embed the secret; pass it via -Secret so the logger can
        # guarantee redaction.
        [string]$Secret
    )
    $ts = (Get-Date).ToString('yyyy-MM-ddTHH:mm:ss')
    $safe = $Message
    if ($PSBoundParameters.ContainsKey('Secret') -and $Secret) {
        # Defensive: if the message ever contains the literal secret, mask it.
        $safe = $safe.Replace($Secret, '***')
    }
    $color = switch ($Level) {
        'Info'  { 'Cyan' }
        'Warn'  { 'Yellow' }
        'Error' { 'Red' }
    }
    Write-Host "[$ts] [$Level] $safe" -ForegroundColor $color
}

# --------------------------------------------------------------------------
# Interactive prompt helpers (honor non-interactive mode)
# --------------------------------------------------------------------------

function Read-PromptDefault {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][string]$Prompt,
        [string]$Default = ''
    )
    if ($script:ArbitronNonInteractive) { return $Default }
    $suffix = if ($Default) { " [$Default]" } else { '' }
    $answer = Read-Host -Prompt "$Prompt$suffix"
    if ([string]::IsNullOrWhiteSpace($answer)) { return $Default }
    return $answer
}

function Read-PromptYesNo {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][string]$Prompt,
        [bool]$Default = $false
    )
    if ($script:ArbitronNonInteractive) { return $Default }
    $hint = if ($Default) { 'Y/n' } else { 'y/N' }
    $answer = Read-Host -Prompt "$Prompt [$hint]"
    if ([string]::IsNullOrWhiteSpace($answer)) { return $Default }
    return ($answer.Trim().ToLowerInvariant() -in @('y', 'yes'))
}

function Read-PromptChoice {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][string]$Prompt,
        [Parameter(Mandatory)][string[]]$Choices,
        [string]$Default
    )
    if ($script:ArbitronNonInteractive) {
        return $(if ($Default) { $Default } else { $Choices[0] })
    }
    while ($true) {
        $list = $Choices -join '/'
        $suffix = if ($Default) { " [$Default]" } else { '' }
        $answer = Read-Host -Prompt "$Prompt ($list)$suffix"
        if ([string]::IsNullOrWhiteSpace($answer) -and $Default) { return $Default }
        $match = $Choices | Where-Object { $_ -ieq $answer.Trim() } | Select-Object -First 1
        if ($match) { return $match }
        Write-ArbitronLog -Level Warn -Message "Please choose one of: $list"
    }
}

# --------------------------------------------------------------------------
# Checksum verification (the single gate every download must pass)
# --------------------------------------------------------------------------

function Test-Sha256 {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)][string]$ExpectedHash
    )
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        Write-ArbitronLog -Level Error -Message "Cannot verify checksum: file not found at $Path"
        return $false
    }
    $actual = (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash
    return ($actual -ieq $ExpectedHash.Trim())
}

# --------------------------------------------------------------------------
# Dependency resolution
# --------------------------------------------------------------------------
# Ladder per dependency: present? -> winget -> bootstrap App Installer ->
# direct-download into an Arbitron-owned prefix. claude installs via npm -g into
# an Arbitron-owned npm prefix so it never fights a system Node. Every download
# is checksum-verified via Test-Sha256 before use. Data-driven + prefix-
# parameterized so P4 reuses the resolver against machine-wide prefixes.

# Seams for external tools so tests can mock them cross-platform.
function Test-WingetPresent {
    [CmdletBinding()]
    param()
    return [bool](Get-Command -Name winget -ErrorAction SilentlyContinue)
}

function Invoke-Winget {
    [CmdletBinding()]
    param([Parameter(ValueFromRemainingArguments)][string[]]$Arguments)
    & winget @Arguments
    return $LASTEXITCODE
}

function Invoke-Npm {
    [CmdletBinding()]
    param([Parameter(ValueFromRemainingArguments)][string[]]$Arguments)
    & npm @Arguments
    return $LASTEXITCODE
}

function Invoke-ArbitronDownload {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][string]$Uri,
        [Parameter(Mandatory)][string]$OutFile
    )
    $dir = Split-Path -Parent $OutFile
    if ($dir -and -not (Test-Path -LiteralPath $dir)) {
        New-Item -ItemType Directory -Path $dir -Force | Out-Null
    }
    Invoke-WebRequest -Uri $Uri -OutFile $OutFile -UseBasicParsing
}

function Get-DependencyVersion {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][pscustomobject]$Spec
    )
    try {
        $raw = & $Spec.Command @($Spec.VersionArgs) 2>$null | Out-String
        $m = [regex]::Match($raw, '(\d+)\.(\d+)(?:\.(\d+))?')
        if (-not $m.Success) { return $null }
        return [version]$m.Value
    } catch {
        return $null
    }
}

function Get-DependencySpec {
    [CmdletBinding()]
    param()
    return @(
        [pscustomobject]@{
            Name = 'node'; Command = 'node'; MinVersion = '18.0.0'
            WingetId = 'OpenJS.NodeJS'; VersionArgs = @('--version'); Optional = $false
        }
        [pscustomobject]@{
            Name = 'git'; Command = 'git'; MinVersion = '2.0.0'
            WingetId = 'Git.Git'; VersionArgs = @('--version'); Optional = $false
        }
        [pscustomobject]@{
            Name = 'gh'; Command = 'gh'; MinVersion = '2.0.0'
            WingetId = 'GitHub.cli'; VersionArgs = @('--version'); Optional = $false
        }
        [pscustomobject]@{
            Name = 'claude'; Command = 'claude'; MinVersion = '0.0.0'
            WingetId = $null; VersionArgs = @('--version'); Optional = $false; ViaNpm = $true
        }
        [pscustomobject]@{
            Name = 'opencode'; Command = 'opencode'; MinVersion = '0.0.0'
            WingetId = $null; VersionArgs = @('--version'); Optional = $true
        }
    )
}

function Test-DependencyPresent {
    [CmdletBinding()]
    param([Parameter(Mandatory)][pscustomobject]$Spec)
    $cmd = Get-Command -Name $Spec.Command -ErrorAction SilentlyContinue
    if (-not $cmd) { return $false }
    $min = [version]$Spec.MinVersion
    if ($min -eq [version]'0.0.0') { return $true }
    $ver = Get-DependencyVersion -Spec $Spec
    if (-not $ver) { return $false }
    return ($ver -ge $min)
}

function Install-AppInstaller {
    [CmdletBinding()]
    param()
    $accept = Read-PromptYesNo -Prompt 'winget (App Installer) is missing. Bootstrap it now?' -Default $true
    if (-not $accept) { return $false }
    $tmp = Join-Path ([System.IO.Path]::GetTempPath()) ('AppInstaller_' + [guid]::NewGuid() + '.msixbundle')
    $url = 'https://aka.ms/getwinget'
    Write-ArbitronLog -Level Info -Message 'Downloading App Installer (winget)...'
    Invoke-ArbitronDownload -Uri $url -OutFile $tmp
    # NOTE: aka.ms/getwinget has no pinned checksum; we install the MSIX as-is
    # (signed bundle). Dependency artifacts with a known hash use Test-Sha256.
    Add-AppxPackage -Path $tmp
    Remove-Item -LiteralPath $tmp -Force -ErrorAction SilentlyContinue
    return $true
}

function Install-ViaWinget {
    [CmdletBinding()]
    param([Parameter(Mandatory)][pscustomobject]$Spec)
    Write-ArbitronLog -Level Info -Message "Installing $($Spec.Name) via winget ($($Spec.WingetId))..."
    $code = Invoke-Winget 'install' '--id' $Spec.WingetId '--silent' `
        '--accept-source-agreements' '--accept-package-agreements'
    if ($code -ne 0) {
        throw "winget failed to install $($Spec.Name) (exit $code)."
    }
}

function Install-ViaDirectDownload {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][pscustomobject]$Spec,
        [Parameter(Mandatory)][string]$Prefix
    )
    if (-not $Spec.PSObject.Properties['DownloadUrl'] -or -not $Spec.DownloadUrl) {
        throw "No direct-download URL is configured for $($Spec.Name)."
    }
    $dest = Join-Path $Prefix $Spec.Name
    New-Item -ItemType Directory -Path $dest -Force | Out-Null
    $artifact = Join-Path $dest ([System.IO.Path]::GetFileName($Spec.DownloadUrl))
    Write-ArbitronLog -Level Info -Message "Downloading $($Spec.Name) from $($Spec.DownloadUrl)..."
    Invoke-ArbitronDownload -Uri $Spec.DownloadUrl -OutFile $artifact
    if (-not (Test-Sha256 -Path $artifact -ExpectedHash $Spec.DownloadSha256)) {
        Remove-Item -LiteralPath $artifact -Force -ErrorAction SilentlyContinue
        throw "Checksum verification failed for $($Spec.Name); aborting (no install)."
    }
    if ($artifact -like '*.zip') {
        Expand-Archive -Path $artifact -DestinationPath $dest -Force
    }
    return $dest
}

function Add-PathPrefix {
    [CmdletBinding()]
    param([Parameter(Mandatory)][string]$Prefix)
    $sep = [System.IO.Path]::PathSeparator
    if (($env:PATH -split [regex]::Escape($sep)) -notcontains $Prefix) {
        $env:PATH = "$Prefix$sep$($env:PATH)"
    }
}

function Install-ClaudeCli {
    [CmdletBinding()]
    param([Parameter(Mandatory)][hashtable]$Paths)
    # claude requires node; resolve it first.
    Resolve-Dependency -Name node -Paths $Paths | Out-Null
    $prefix = $Paths.NpmPrefix
    New-Item -ItemType Directory -Path $prefix -Force | Out-Null
    Write-ArbitronLog -Level Info -Message "Installing claude CLI into Arbitron npm prefix ($prefix)..."
    $code = Invoke-Npm 'install' '-g' '--prefix' $prefix '@anthropic-ai/claude-code'
    if ($code -ne 0) {
        throw "npm failed to install @anthropic-ai/claude-code (exit $code)."
    }
    Add-PathPrefix -Prefix $prefix
    Add-PathPrefix -Prefix (Join-Path $prefix 'bin')
}

function Resolve-Dependency {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][string]$Name,
        [Parameter(Mandatory)][hashtable]$Paths
    )
    $spec = Get-DependencySpec | Where-Object Name -eq $Name | Select-Object -First 1
    if (-not $spec) { throw "Unknown dependency: $Name" }

    if (Test-DependencyPresent -Spec $spec) {
        Write-ArbitronLog -Level Info -Message "$Name already present; skipping."
        return [pscustomobject]@{ Name = $Name; Status = 'present' }
    }

    # claude is special-cased: npm -g into the Arbitron-owned prefix.
    if ($spec.PSObject.Properties['ViaNpm'] -and $spec.ViaNpm) {
        Install-ClaudeCli -Paths $Paths
        return [pscustomobject]@{ Name = $Name; Status = 'resolved' }
    }

    # Ladder: winget -> bootstrap App Installer -> direct download.
    if (Test-WingetPresent) {
        Install-ViaWinget -Spec $spec
        return [pscustomobject]@{ Name = $Name; Status = 'resolved' }
    }

    $bootstrapped = Install-AppInstaller
    if ($bootstrapped -and (Test-WingetPresent)) {
        Install-ViaWinget -Spec $spec
        return [pscustomobject]@{ Name = $Name; Status = 'resolved' }
    }

    if ($spec.Optional) {
        Write-ArbitronLog -Level Warn -Message "$Name is optional and was not installed; continuing."
        return [pscustomobject]@{ Name = $Name; Status = 'skipped' }
    }

    Install-ViaDirectDownload -Spec $spec -Prefix $Paths.Runtime | Out-Null
    return [pscustomobject]@{ Name = $Name; Status = 'resolved' }
}

function Resolve-AllDependencies {
    [CmdletBinding()]
    param([Parameter(Mandatory)][hashtable]$Paths)
    $results = @()
    foreach ($spec in Get-DependencySpec) {
        try {
            $results += Resolve-Dependency -Name $spec.Name -Paths $Paths
        } catch {
            if ($spec.Optional) {
                Write-ArbitronLog -Level Warn -Message "Optional dependency $($spec.Name) failed: $($_.Exception.Message)"
                $results += [pscustomobject]@{ Name = $spec.Name; Status = 'skipped' }
            } else {
                throw
            }
        }
    }
    return $results
}

# --------------------------------------------------------------------------
# Existing-install detection
# --------------------------------------------------------------------------

function Test-ExistingInstall {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][hashtable]$Paths
    )
    if (-not (Test-Path -LiteralPath $Paths.Config -PathType Leaf)) {
        return $false
    }
    try {
        $task = Get-ScheduledTask -TaskName $script:ArbitronTaskName -ErrorAction Stop
        return [bool]$task
    } catch {
        return $false
    }
}

# --------------------------------------------------------------------------
# Config Q&A, config.yaml writer, secrets, binary download
# --------------------------------------------------------------------------

# Master key = base64 of 32 random bytes (consumed as ARBITRON_MASTER_KEY).
# RNG behind New-RandomBytes seam for test determinism.
function New-RandomBytes {
    [CmdletBinding()]
    param([int]$Count = 32)
    $bytes = New-Object byte[] $Count
    [System.Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($bytes)
    return $bytes
}

function New-MasterKey {
    [CmdletBinding()]
    param()
    return [Convert]::ToBase64String((New-RandomBytes -Count 32))
}

# Seam over [Environment]::SetEnvironmentVariable User-scope (mockable in tests;
# also persists into the process so the current run can read it).
function Set-UserEnvironmentVariable {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][string]$Name,
        [AllowNull()][string]$Value
    )
    [Environment]::SetEnvironmentVariable($Name, $Value, 'User')
    Set-Item -Path "Env:\$Name" -Value $Value -ErrorAction SilentlyContinue
}

function Set-ArbitronSecret {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][string]$Name,
        [Parameter(Mandatory)][string]$Value,
        [ValidateSet('Env', 'Dpapi')][string]$Method = 'Env',
        [Parameter(Mandatory)][hashtable]$Paths
    )
    Write-ArbitronLog -Level Info -Message "Storing secret $Name (method: $Method)." -Secret $Value
    switch ($Method) {
        'Env' {
            Set-UserEnvironmentVariable -Name $Name -Value $Value
        }
        'Dpapi' {
            if (-not (Test-Path -LiteralPath $Paths.Data)) {
                New-Item -ItemType Directory -Path $Paths.Data -Force | Out-Null
            }
            $blob = Join-Path $Paths.Data "$Name.secret"
            $secure = ConvertTo-SecureString -String $Value -AsPlainText -Force
            # ConvertFrom-SecureString uses DPAPI (CurrentUser) on Windows.
            $protected = ConvertFrom-SecureString -SecureString $secure
            Set-Content -Path $blob -Value $protected -Encoding ascii
        }
    }
}

function Remove-ArbitronSecret {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][string]$Name,
        [Parameter(Mandatory)][hashtable]$Paths
    )
    Set-UserEnvironmentVariable -Name $Name -Value $null
    $blob = Join-Path $Paths.Data "$Name.secret"
    if (Test-Path -LiteralPath $blob) { Remove-Item -LiteralPath $blob -Force }
}

$script:ArbitronRceWarning = @'
WARNING: Enabling unattended agent tools passes --dangerously-skip-permissions
to the claude CLI. This removes EVERY tool-permission prompt: the agent will run
Bash/Write/Edit with no human in the loop. This is RCE-EQUIVALENT whenever any
prompt input can be influenced by an untrusted source. Only enable it if every
input is fully trusted. Default: No.
'@

function Invoke-ConfigQA {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][hashtable]$Paths,
        [hashtable]$Defaults = @{}
    )
    $portDefault = if ($Defaults.ContainsKey('Port')) { $Defaults.Port } else { 8080 }
    $port = [int](Read-PromptDefault -Prompt 'Dashboard port' -Default "$portDefault")

    $llmDefault = if ($Defaults.ContainsKey('LlmMode')) { $Defaults.LlmMode } else { 'subscription' }
    $llmMode = Read-PromptChoice -Prompt 'LLM access (subscription = claude /login, apikey = paste Anthropic key)' `
        -Choices @('subscription', 'apikey') -Default $llmDefault

    $apiKey = $null
    if ($llmMode -eq 'apikey') {
        $apiKey = Read-PromptDefault -Prompt 'Paste your Anthropic API key' -Default ''
    }

    $dataDefault = if ($Defaults.ContainsKey('DataDir')) { $Defaults.DataDir } else { $Paths.Data }
    $dataDir = Read-PromptDefault -Prompt 'Data location' -Default $dataDefault

    # The skip-permissions prompt is always explicit, never silently defaulted,
    # and is preceded by the RCE-equivalent warning.
    Write-ArbitronLog -Level Warn -Message $script:ArbitronRceWarning
    $skipDefault = if ($Defaults.ContainsKey('SkipPermissions')) { [bool]$Defaults.SkipPermissions } else { $false }
    $skip = Read-PromptYesNo -Prompt 'Enable unattended agent tools (--dangerously-skip-permissions)?' -Default $skipDefault

    return @{
        Port            = $port
        LlmMode         = $llmMode
        ApiKey          = $apiKey
        SkipPermissions = $skip
        DataDir         = $dataDir
    }
}

function Write-ArbitronConfig {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][hashtable]$Paths,
        [Parameter(Mandatory)][hashtable]$Answers
    )
    $dataDir = $Answers.DataDir
    foreach ($d in @($Paths.Root, $dataDir, $Paths.Logs)) {
        if ($d -and -not (Test-Path -LiteralPath $d)) {
            New-Item -ItemType Directory -Path $d -Force | Out-Null
        }
    }
    $dbPath        = Join-Path $dataDir 'arbitron.db'
    $workspacePath = Join-Path $dataDir 'workspaces'
    $logFile       = $Paths.LogFile
    $provider      = if ($Answers.LlmMode -eq 'apikey') { 'anthropic' } else { 'claude-cli' }
    $skip          = if ($Answers.SkipPermissions) { 'true' } else { 'false' }

    # YAML scalars containing backslashes (Windows paths) are quoted.
    $lines = @(
        '# Arbitron configuration (generated by install.ps1). Loopback / local mode.'
        'server:'
        '  host: 127.0.0.1'
        "  port: $($Answers.Port)"
        '  publicbaseurl: "http://127.0.0.1:' + $Answers.Port + '"'
        'database:'
        "  path: `"$dbPath`""
        'workspace:'
        "  path: `"$workspacePath`""
        'logging:'
        '  level: info'
        "  file: `"$logFile`""
        'llm:'
        "  provider: $provider"
        "  skippermissions: $skip"
    )
    # API key (when present) goes to the secrets sink, NEVER into config.yaml.
    $content = ($lines -join "`n") + "`n"
    Set-Content -Path $Paths.Config -Value $content -Encoding utf8
}

# Resolve the SHA256SUMS line for a given artifact file name.
function Get-Sha256ForArtifact {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][string]$SumsUri,
        [Parameter(Mandatory)][string]$ArtifactName
    )
    $tmp = Join-Path ([System.IO.Path]::GetTempPath()) ('SHA256SUMS_' + [guid]::NewGuid())
    Invoke-ArbitronDownload -Uri $SumsUri -OutFile $tmp
    try {
        foreach ($line in (Get-Content -LiteralPath $tmp)) {
            # Format: "<hash>  <filename>"
            $parts = $line -split '\s+', 2
            if ($parts.Count -eq 2 -and $parts[1].Trim() -eq $ArtifactName) {
                return $parts[0].Trim()
            }
        }
    } finally {
        Remove-Item -LiteralPath $tmp -Force -ErrorAction SilentlyContinue
    }
    throw "No SHA256 entry found for $ArtifactName."
}

function Get-ArbitronBinary {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][hashtable]$Paths,
        [Parameter(Mandatory)][string]$Version,
        [Parameter(Mandatory)][string]$Arch
    )
    $artifact = "arbitron_${Version}_windows_${Arch}.exe"
    $base = "https://github.com/$script:ArbitronRepo/releases/download/v$Version"
    $binUri  = "$base/$artifact"
    $sumsUri = "$base/SHA256SUMS"

    if (-not (Test-Path -LiteralPath $Paths.Bin)) {
        New-Item -ItemType Directory -Path $Paths.Bin -Force | Out-Null
    }
    $tmp = Join-Path ([System.IO.Path]::GetTempPath()) ('arbitron_' + [guid]::NewGuid() + '.exe')
    Write-ArbitronLog -Level Info -Message "Downloading $artifact ..."
    Invoke-ArbitronDownload -Uri $binUri -OutFile $tmp
    try {
        $expected = Get-Sha256ForArtifact -SumsUri $sumsUri -ArtifactName $artifact
        if (-not (Test-Sha256 -Path $tmp -ExpectedHash $expected)) {
            throw "Binary checksum verification failed; aborting (no binary placed)."
        }
        Move-Item -LiteralPath $tmp -Destination $Paths.BinaryExe -Force
    } finally {
        if (Test-Path -LiteralPath $tmp) { Remove-Item -LiteralPath $tmp -Force -ErrorAction SilentlyContinue }
    }
}

# --------------------------------------------------------------------------
# Service registration (Scheduled Task), health-check, summary
# --------------------------------------------------------------------------

# Register-ArbitronService is behind an install-type switch so P4 can plug in the
# native Windows Service for the Shared branch while local keeps the Scheduled
# Task (no elevation needed).
function Register-ArbitronService {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][ValidateSet('Local', 'Shared')][string]$InstallType,
        [Parameter(Mandatory)][hashtable]$Paths,
        [Parameter(Mandatory)][int]$Port
    )
    switch ($InstallType) {
        'Local' {
            $arg = "serve --config `"$($Paths.Config)`""
            $action = New-ScheduledTaskAction -Execute $Paths.BinaryExe -Argument $arg
            $trigger = New-ScheduledTaskTrigger -AtLogOn
            $principal = New-ScheduledTaskPrincipal -UserId ([Environment]::UserName) -LogonType Interactive
            $settings = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries
            Write-ArbitronLog -Level Info -Message "Registering at-logon Scheduled Task '$script:ArbitronTaskName'..."
            Register-ScheduledTask -TaskName $script:ArbitronTaskName -Action $action `
                -Trigger $trigger -Principal $principal -Settings $settings -Force
            Start-ScheduledTask -TaskName $script:ArbitronTaskName
        }
        'Shared' {
            throw 'Shared service registration is delivered by P4.'
        }
    }
}

# Seam over the actual HTTP probe so health polling is mockable.
function Invoke-HealthRequest {
    [CmdletBinding()]
    param([Parameter(Mandatory)][string]$Uri)
    try {
        $resp = Invoke-WebRequest -Uri $Uri -UseBasicParsing -TimeoutSec 5
        return [int]$resp.StatusCode
    } catch {
        return 0
    }
}

function Test-ArbitronHealth {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][int]$Port,
        [Parameter(Mandatory)][hashtable]$Paths,
        [int]$TimeoutSeconds = 30
    )
    $uri = "http://127.0.0.1:$Port/"
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    $attempt = 0
    do {
        $attempt++
        $code = Invoke-HealthRequest -Uri $uri
        if ($code -eq 200) {
            Write-ArbitronLog -Level Info -Message "Health check OK ($uri)."
            return $true
        }
        if ((Get-Date) -lt $deadline) { Start-Sleep -Seconds 2 }
    } while ((Get-Date) -lt $deadline)
    throw "Arbitron did not become healthy at $uri after $TimeoutSeconds s. Check the log at $($Paths.LogFile)."
}

function Write-InstallSummary {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][hashtable]$Paths,
        [Parameter(Mandatory)][int]$Port,
        [string]$LlmMode = 'subscription'
    )
    Write-ArbitronLog -Level Info -Message '----------------------------------------------------------'
    Write-ArbitronLog -Level Info -Message 'Arbitron installation complete.'
    Write-ArbitronLog -Level Info -Message "  Dashboard:    http://127.0.0.1:$Port"
    Write-ArbitronLog -Level Info -Message "  Service task: $script:ArbitronTaskName (at-logon Scheduled Task)"
    Write-ArbitronLog -Level Info -Message "  Config:       $($Paths.Config)"
    Write-ArbitronLog -Level Info -Message "  Log file:     $($Paths.LogFile)"
    if ($LlmMode -eq 'subscription') {
        Write-ArbitronLog -Level Info -Message '  Next step:    run `claude /login` if you have not already authenticated.'
    }
    Write-ArbitronLog -Level Info -Message '----------------------------------------------------------'
}

# Subscription-login seam (interactive; mocked in tests).
function Invoke-ClaudeLogin {
    [CmdletBinding()]
    param([Parameter(Mandatory)][hashtable]$Paths)
    $env:CLAUDE_CONFIG_DIR = $Paths.ClaudeConfigDir
    [Environment]::SetEnvironmentVariable('CLAUDE_CONFIG_DIR', $Paths.ClaudeConfigDir, 'User')
    if (-not (Test-Path -LiteralPath $Paths.ClaudeConfigDir)) {
        New-Item -ItemType Directory -Path $Paths.ClaudeConfigDir -Force | Out-Null
    }
    Write-ArbitronLog -Level Info -Message 'Launching `claude /login` for subscription auth...'
    try { & claude '/login' } catch {
        Write-ArbitronLog -Level Warn -Message "Could not launch claude /login automatically; run it manually. ($($_.Exception.Message))"
    }
}

# Parse a minimal set of defaults out of an existing config.yaml for reconfigure.
function Get-ExistingConfigDefaults {
    [CmdletBinding()]
    param([Parameter(Mandatory)][hashtable]$Paths)
    $defaults = @{}
    if (-not (Test-Path -LiteralPath $Paths.Config)) { return $defaults }
    foreach ($line in (Get-Content -LiteralPath $Paths.Config)) {
        if ($line -match '^\s*port:\s*(\d+)') { $defaults.Port = [int]$Matches[1] }
        if ($line -match '^\s*provider:\s*(\S+)') {
            $defaults.LlmMode = if ($Matches[1] -eq 'anthropic') { 'apikey' } else { 'subscription' }
        }
        if ($line -match '^\s*path:\s*"?(.+?arbitron\.db)"?\s*$') {
            $defaults.DataDir = Split-Path -Parent ($Matches[1].Trim('"'))
        }
        if ($line -match '^\s*skippermissions:\s*(true|false)') {
            $defaults.SkipPermissions = ($Matches[1] -eq 'true')
        }
    }
    return $defaults
}

function Get-ConfigPort {
    [CmdletBinding()]
    param([Parameter(Mandatory)][hashtable]$Paths)
    $d = Get-ExistingConfigDefaults -Paths $Paths
    if ($d.ContainsKey('Port')) { return $d.Port }
    return 8080
}

# --------------------------------------------------------------------------
# Lifecycle flow functions
# --------------------------------------------------------------------------

function Invoke-FreshInstall {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][hashtable]$Paths,
        [string]$InstallType = 'Local',
        [string]$Arch = 'amd64',
        [string]$Version = $script:ResolvedVersion
    )
    Write-ArbitronLog -Level Info -Message 'Starting fresh Arbitron install...'
    foreach ($d in @($Paths.Root, $Paths.Bin, $Paths.Data, $Paths.Logs)) {
        if (-not (Test-Path -LiteralPath $d)) { New-Item -ItemType Directory -Path $d -Force | Out-Null }
    }

    # 1. Dependencies.
    Resolve-AllDependencies -Paths $Paths | Out-Null

    # 2. Config Q&A.
    $answers = Invoke-ConfigQA -Paths $Paths

    # 3. Binary download + verify.
    Get-ArbitronBinary -Paths $Paths -Version $Version -Arch $Arch

    # 4. Write config + secrets.
    Write-ArbitronConfig -Paths $Paths -Answers $answers
    $masterKey = New-MasterKey
    Set-ArbitronSecret -Name 'ARBITRON_MASTER_KEY' -Value $masterKey -Method Env -Paths $Paths
    if ($answers.LlmMode -eq 'apikey' -and $answers.ApiKey) {
        Set-ArbitronSecret -Name 'ARBITRON_LLM_APIKEY' -Value $answers.ApiKey -Method Env -Paths $Paths
    } elseif ($answers.LlmMode -eq 'subscription') {
        Invoke-ClaudeLogin -Paths $Paths
    }

    # 5. Register + start service.
    Register-ArbitronService -InstallType $InstallType -Paths $Paths -Port $answers.Port

    # 6. Health-check.
    Test-ArbitronHealth -Port $answers.Port -Paths $Paths | Out-Null

    # 7. Summary.
    Write-InstallSummary -Paths $Paths -Port $answers.Port -LlmMode $answers.LlmMode
}

function Invoke-Upgrade {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][hashtable]$Paths,
        [string]$InstallType = 'Local',
        [string]$Arch = 'amd64',
        [string]$Version = $script:ResolvedVersion
    )
    Write-ArbitronLog -Level Info -Message "Upgrading Arbitron to $Version..."
    # Download + verify the new exe BEFORE touching the running task.
    Get-ArbitronBinary -Paths $Paths -Version $Version -Arch $Arch
    Stop-ScheduledTask -TaskName $script:ArbitronTaskName
    Start-ScheduledTask -TaskName $script:ArbitronTaskName
    Write-ArbitronLog -Level Warn -Message 'Migrations run forward-only on serve start; downgrading after an upgrade is not supported (no down-migrations). Keep a data backup before upgrading.'
    $port = Get-ConfigPort -Paths $Paths
    Write-InstallSummary -Paths $Paths -Port $port
}

function Invoke-Reconfigure {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][hashtable]$Paths,
        [string]$InstallType = 'Local',
        [string]$Arch = 'amd64'
    )
    Write-ArbitronLog -Level Info -Message 'Reconfiguring Arbitron (data + secrets preserved)...'
    $defaults = Get-ExistingConfigDefaults -Paths $Paths
    $answers = Invoke-ConfigQA -Paths $Paths -Defaults $defaults
    Write-ArbitronConfig -Paths $Paths -Answers $answers
    # If switching into API-key mode, (re)store the key; never regenerate master key.
    if ($answers.LlmMode -eq 'apikey' -and $answers.ApiKey) {
        Set-ArbitronSecret -Name 'ARBITRON_LLM_APIKEY' -Value $answers.ApiKey -Method Env -Paths $Paths
    }
    Stop-ScheduledTask -TaskName $script:ArbitronTaskName
    Start-ScheduledTask -TaskName $script:ArbitronTaskName
    Write-InstallSummary -Paths $Paths -Port $answers.Port -LlmMode $answers.LlmMode
}

function Invoke-Uninstall {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][hashtable]$Paths,
        [string]$InstallType = 'Local'
    )
    Write-ArbitronLog -Level Info -Message 'Uninstalling Arbitron...'
    try {
        Stop-ScheduledTask -TaskName $script:ArbitronTaskName
    } catch {
        Write-ArbitronLog -Level Info -Message "Scheduled Task was not running (nothing to stop): $($_.Exception.Message)"
    }
    try { Unregister-ScheduledTask -TaskName $script:ArbitronTaskName -Confirm:$false } catch {
        Write-ArbitronLog -Level Warn -Message "Could not unregister the Scheduled Task: $($_.Exception.Message)"
    }
    if (Test-Path -LiteralPath $Paths.Bin)    { Remove-Item -Recurse -Force $Paths.Bin -ErrorAction SilentlyContinue }
    if (Test-Path -LiteralPath $Paths.Config) { Remove-Item -Force $Paths.Config -ErrorAction SilentlyContinue }

    Remove-ArbitronSecret -Name 'ARBITRON_MASTER_KEY' -Paths $Paths
    Remove-ArbitronSecret -Name 'ARBITRON_LLM_APIKEY' -Paths $Paths

    $removeData = Read-PromptYesNo -Prompt "Also delete the data directory ($($Paths.Data))? This destroys your database." -Default $false
    if ($removeData) {
        if (Test-Path -LiteralPath $Paths.Data) { Remove-Item -Recurse -Force $Paths.Data -ErrorAction SilentlyContinue }
        Write-ArbitronLog -Level Info -Message 'Data directory removed.'
    } else {
        Write-ArbitronLog -Level Info -Message "Data directory kept at $($Paths.Data)."
    }
    Write-ArbitronLog -Level Info -Message 'Uninstall complete.'
}

# --------------------------------------------------------------------------
# Lifecycle dispatch
# --------------------------------------------------------------------------

function Invoke-Main {
    [CmdletBinding()]
    param()
    Assert-SupportedPlatform
    $arch = Get-Architecture
    $installType = Get-InstallType
    $paths = Get-ArbitronPaths -InstallType $installType
    # Honor an explicit -Config / ARBITRON_CONFIG override (P0 mechanism): the
    # written file and the Scheduled Task --config both follow it.
    if ($script:Config) { $paths.Config = $script:Config }
    Write-ArbitronLog -Level Info -Message "Arbitron installer - install type: $installType, arch: $arch"

    if (-not (Test-ExistingInstall -Paths $paths)) {
        Invoke-FreshInstall -Paths $paths -InstallType $installType -Arch $arch
        return
    }

    Write-ArbitronLog -Level Info -Message 'An existing Arbitron install was detected.'
    $action = Read-PromptChoice -Prompt 'What would you like to do?' `
        -Choices @('Upgrade', 'Reconfigure', 'Uninstall') -Default 'Upgrade'
    switch ($action) {
        'Upgrade'     { Invoke-Upgrade     -Paths $paths -InstallType $installType -Arch $arch }
        'Reconfigure' { Invoke-Reconfigure -Paths $paths -InstallType $installType -Arch $arch }
        'Uninstall'   { Invoke-Uninstall   -Paths $paths -InstallType $installType }
    }
}

# Bottom-of-file auto-invoke guard. Dot-sourcing for tests sets
# ARBITRON_INSTALL_NOEXEC so functions are defined without running the flow.
if (-not $env:ARBITRON_INSTALL_NOEXEC) {
    Invoke-Main
}
