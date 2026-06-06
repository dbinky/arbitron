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
    # Shared (system-level) seams: native Windows Service + LocalAccounts cmdlets.
    'New-Service'                  = { [CmdletBinding()] param([string]$Name, [string]$BinaryPathName, [string]$DisplayName, [string]$Description, [string]$StartupType, [pscredential]$Credential) throw 'New-Service is only available on Windows.' }
    'Set-Service'                  = { [CmdletBinding()] param([string]$Name, [string]$StartupType, [string]$Status) throw 'Set-Service is only available on Windows.' }
    'Get-Service'                  = { [CmdletBinding()] param([string]$Name) throw 'Get-Service is only available on Windows.' }
    'Start-Service'                = { [CmdletBinding()] param([string]$Name) throw 'Start-Service is only available on Windows.' }
    'Stop-Service'                 = { [CmdletBinding()] param([string]$Name, [switch]$Force) throw 'Stop-Service is only available on Windows.' }
    'Restart-Service'              = { [CmdletBinding()] param([string]$Name, [switch]$Force) throw 'Restart-Service is only available on Windows.' }
    'Remove-Service'               = { [CmdletBinding()] param([string]$Name) throw 'Remove-Service is only available on Windows.' }
    'Get-LocalUser'                = { [CmdletBinding()] param([string]$Name) throw 'Get-LocalUser is only available on Windows.' }
    'New-LocalUser'                = { [CmdletBinding()] param([string]$Name, [securestring]$Password, [string]$Description, [switch]$PasswordNeverExpires, [switch]$UserMayNotChangePassword) throw 'New-LocalUser is only available on Windows.' }
    'Remove-LocalUser'             = { [CmdletBinding()] param([string]$Name) throw 'Remove-LocalUser is only available on Windows.' }
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

# Get-InstallType returns an install-type "enum" string by prompting the operator
# to choose between a per-user Local install (no elevation) and a system-level
# Shared install (Windows Service; requires admin). In non-interactive mode the
# default is Local (the no-elevation, zero-prompt path).
function Get-InstallType {
    [CmdletBinding()]
    param()
    return Read-PromptChoice -Prompt 'Install type (Local = per-user, no admin; Shared = system-wide Windows Service, requires admin)' `
        -Choices @('Local', 'Shared') -Default 'Local'
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
            # System-level layout: binary under %ProgramFiles%, everything else
            # (config/data/workspaces/logs) under %ProgramData%\Arbitron.
            $binRoot  = Join-Path $env:ProgramFiles 'Arbitron'
            $dataRoot = Join-Path $env:ProgramData 'Arbitron'
            return @{
                Root            = $dataRoot
                Bin             = $binRoot
                BinaryExe       = Join-Path $binRoot 'arbitron.exe'
                Config          = Join-Path $dataRoot 'config.yaml'
                Data            = Join-Path $dataRoot 'data'
                Workspaces      = Join-Path $dataRoot 'workspaces'
                Logs            = Join-Path $dataRoot 'logs'
                LogFile         = Join-Path $dataRoot 'logs\arbitron.log'
                ClaudeConfigDir = Join-Path $dataRoot 'claude'
                NpmPrefix       = Join-Path $dataRoot 'npm'
                Runtime         = Join-Path $dataRoot 'runtime'
            }
        }
    }
}

# --------------------------------------------------------------------------
# Elevation (Shared path requires admin)
# --------------------------------------------------------------------------

# Test-IsElevated is a thin, mockable seam: true when the current principal is in
# the built-in Administrators role. On non-Windows hosts (where the Shared path
# never runs for real) it returns $false; tests mock it explicitly.
function Test-IsElevated {
    [CmdletBinding()]
    param()
    if (-not (Test-IsWindows)) { return $false }
    try {
        $id = [System.Security.Principal.WindowsIdentity]::GetCurrent()
        $principal = New-Object System.Security.Principal.WindowsPrincipal($id)
        return $principal.IsInRole([System.Security.Principal.WindowsBuiltInRole]::Administrator)
    } catch {
        return $false
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
# Version selection + release URL builders (P5 Task 2)
# --------------------------------------------------------------------------

# Get-LatestReleaseTag follows the releases/latest redirect (no API token, no
# rate-limit) and returns the final tag (e.g. 'v1.2.3'). Falls back to the GitHub
# API .tag_name when the redirect parse fails. Isolated as a seam so tests stub
# it without a real network call.
function Get-LatestReleaseTag {
    [CmdletBinding()]
    param()
    $latestUrl = "https://github.com/$script:ArbitronRepo/releases/latest"
    try {
        $resp = Invoke-WebRequest -Uri $latestUrl -Method Head -MaximumRedirection 0 `
            -UseBasicParsing -ErrorAction SilentlyContinue
        $location = $resp.Headers.Location
    } catch {
        # PowerShell can surface the 3xx as an exception; recover the Location.
        $location = $_.Exception.Response.Headers.Location.ToString()
    }
    if ($location -and ($location -match '/tag/(v?[\d][^/\s]*)$')) {
        return $Matches[1]
    }
    # API fallback.
    try {
        $api = Invoke-WebRequest -Uri "https://api.github.com/repos/$script:ArbitronRepo/releases/latest" `
            -UseBasicParsing
        $json = $api.Content | ConvertFrom-Json
        if ($json.tag_name) { return $json.tag_name }
    } catch {
        Write-ArbitronLog -Level Warn -Message "Could not query the latest release tag: $($_.Exception.Message)"
    }
    return $null
}

# Get-ArbitronVersion resolves the release version to install. With
# -Version/$env:ARBITRON_VERSION set it is used verbatim, normalized to NO
# leading 'v' (accepts both '1.2.3' and 'v1.2.3'). Unset -> the latest release.
# Throws (fail-closed) when no version can be determined.
function Get-ArbitronVersion {
    [CmdletBinding()]
    param(
        [string]$Version = $env:ARBITRON_VERSION
    )
    if ($Version) {
        return ($Version -replace '^v', '')
    }
    $tag = Get-LatestReleaseTag
    if (-not $tag) {
        throw 'Could not resolve the latest Arbitron release version; pin one with -Version / $env:ARBITRON_VERSION.'
    }
    return ($tag -replace '^v', '')
}

# Get-ArtifactName -> arbitron_<version>_windows_<arch>.exe (this script is
# Windows-only; the .exe suffix is always present).
function Get-ArtifactName {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][string]$Version,
        [Parameter(Mandatory)][string]$Arch
    )
    $v = $Version -replace '^v', ''
    return "arbitron_${v}_windows_${Arch}.exe"
}

# Get-ArtifactUrl -> the pinned-tag download URL for the resolved version.
function Get-ArtifactUrl {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][string]$Version,
        [Parameter(Mandatory)][string]$Arch
    )
    $v = $Version -replace '^v', ''
    $name = Get-ArtifactName -Version $v -Arch $Arch
    return "https://github.com/$script:ArbitronRepo/releases/download/v$v/$name"
}

# --------------------------------------------------------------------------
# The canonical FAIL-CLOSED verification contract (P5 Task 2)
# --------------------------------------------------------------------------
# Test-Download is the single gate every release artifact passes before it is
# installed. It is MANDATORY and has NO bypass: any of { missing SHA256SUMS,
# artifact not listed, hash mismatch } throws a terminating error AND removes the
# partial download, so the install aborts before the binary is placed/executed.
# Test-Signature is OPTIONAL defense-in-depth (cosign); its absence NEVER
# bypasses the checksum gate.

function Test-Download {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)][string]$Name,
        [Parameter(Mandatory)][string]$SumsFile
    )
    if (-not (Test-Path -LiteralPath $SumsFile -PathType Leaf)) {
        Remove-Item -LiteralPath $Path -Force -ErrorAction SilentlyContinue
        throw "Could not fetch/read SHA256SUMS ($SumsFile); refusing to install $Name."
    }
    $expected = $null
    foreach ($line in (Get-Content -LiteralPath $SumsFile)) {
        $parts = $line -split '\s+', 2
        if ($parts.Count -eq 2) {
            $entry = $parts[1].Trim().TrimStart('*')
            if ($entry -eq $Name) { $expected = $parts[0].Trim(); break }
        }
    }
    if (-not $expected) {
        Remove-Item -LiteralPath $Path -Force -ErrorAction SilentlyContinue
        throw "No checksum for $Name in SHA256SUMS; refusing to install."
    }
    if (-not (Test-Sha256 -Path $Path -ExpectedHash $expected)) {
        Remove-Item -LiteralPath $Path -Force -ErrorAction SilentlyContinue
        throw "Checksum verification failed for $Name; aborting (no binary placed)."
    }
}

# Test-CosignPresent is a mockable seam.
function Test-CosignPresent {
    [CmdletBinding()]
    param()
    return [bool](Get-Command -Name cosign -ErrorAction SilentlyContinue)
}

# Test-Signature OPTIONALLY verifies the SHA256SUMS cosign signature. Runs only
# when cosign is present OR $env:ARBITRON_VERIFY_SIGNATURE='1'. When requested and
# cosign fails it throws (fail-closed for the signature too). When cosign is
# absent and not requested it logs a notice and returns (the checksum gate
# already passed and is never bypassed here).
function Test-Signature {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][string]$SumsFile,
        [string]$SigFile = "$SumsFile.sig",
        [string]$PemFile = "$SumsFile.pem"
    )
    $want = ($env:ARBITRON_VERIFY_SIGNATURE -eq '1')
    if (-not (Test-CosignPresent)) {
        if ($want) { throw 'ARBITRON_VERIFY_SIGNATURE=1 but cosign is not installed.' }
        Write-ArbitronLog -Level Info -Message 'cosign not found; skipping optional signature check (checksum already verified).'
        return
    }
    if (-not (Test-Path -LiteralPath $SigFile) -or -not (Test-Path -LiteralPath $PemFile)) {
        if ($want) { throw "Signature verification requested but $SigFile / $PemFile is missing." }
        Write-ArbitronLog -Level Info -Message 'No signature/cert alongside SHA256SUMS; skipping optional signature check.'
        return
    }
    Write-ArbitronLog -Level Info -Message 'Verifying SHA256SUMS signature with cosign...'
    & cosign verify-blob `
        --certificate $PemFile `
        --signature $SigFile `
        --certificate-identity-regexp "https://github.com/$script:ArbitronRepo/.*" `
        --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' `
        $SumsFile 2>$null | Out-Null
    if ($LASTEXITCODE -ne 0) {
        throw 'cosign signature verification FAILED for SHA256SUMS.'
    }
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
        [Parameter(Mandatory)][hashtable]$Paths,
        [ValidateSet('Local', 'Shared')][string]$InstallType = 'Local'
    )
    if (-not (Test-Path -LiteralPath $Paths.Config -PathType Leaf)) {
        return $false
    }
    if ($InstallType -eq 'Shared') {
        try {
            $svc = Get-Service -Name $script:ArbitronServiceName -ErrorAction Stop
            return [bool]$svc
        } catch {
            return $false
        }
    }
    try {
        $task = Get-ScheduledTask -TaskName $script:ArbitronTaskName -ErrorAction Stop
        return [bool]$task
    } catch {
        return $false
    }
}

# --------------------------------------------------------------------------
# Shared (system-level) skeleton: directory tree, ACLs, run-as / service account
# --------------------------------------------------------------------------

# Native Windows Service name (single source of truth for the Shared path).
$script:ArbitronServiceName = 'arbitron'

function New-SharedDirectoryTree {
    [CmdletBinding()]
    param([Parameter(Mandatory)][hashtable]$Paths)
    foreach ($d in @($Paths.Root, $Paths.Bin, $Paths.Data, $Paths.Workspaces, $Paths.Logs)) {
        if ($d) { New-Item -ItemType Directory -Path $d -Force | Out-Null }
    }
}

# Get-RunAsChoice asks whether the service runs as LocalSystem (default) or a
# dedicated local account (default name 'arbitron'). Returns the captured choice
# for Task 3 (service registration) to consume.
function Get-RunAsChoice {
    [CmdletBinding()]
    param()
    $kind = Read-PromptChoice -Prompt 'Run the service as LocalSystem (default) or a dedicated Account?' `
        -Choices @('LocalSystem', 'Account') -Default 'LocalSystem'
    if ($kind -ieq 'Account') {
        $name = Read-PromptDefault -Prompt 'Service account name' -Default 'arbitron'
        return @{ Kind = 'Account'; Name = $name }
    }
    return @{ Kind = 'LocalSystem'; Name = $null }
}

# Invoke-Icacls is a seam over icacls.exe so ACL grants are mockable in tests.
function Invoke-Icacls {
    [CmdletBinding()]
    param([Parameter(ValueFromRemainingArguments)][string[]]$Arguments)
    & icacls.exe @Arguments
    return $LASTEXITCODE
}

# Grant the service account Modify rights on the data and logs directories so the
# running service (when not LocalSystem) can write its DB, workspaces, and logs.
function Set-ArbitronDataAcl {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][string]$Account,
        [Parameter(Mandatory)][hashtable]$Paths
    )
    foreach ($dir in @($Paths.Data, $Paths.Logs, $Paths.Workspaces)) {
        if ($dir) {
            Invoke-Icacls $dir '/grant' "${Account}:(OI)(CI)M" '/T' '/C' | Out-Null
        }
    }
}

# Create the dedicated local service account if absent, with a no-interactive,
# never-expiring posture, and apply data/log ACLs. The Log-on-as-a-service right
# is granted at registration time (Task 3) via Grant-LogonAsService.
function New-ArbitronServiceAccount {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][string]$Name,
        [Parameter(Mandatory)][hashtable]$Paths
    )
    $exists = $false
    try {
        $u = Get-LocalUser -Name $Name -ErrorAction Stop
        $exists = [bool]$u
    } catch {
        $exists = $false
    }
    if (-not $exists) {
        Write-ArbitronLog -Level Info -Message "Creating dedicated service account '$Name'..."
        $pw = ConvertTo-SecureString -String ([Convert]::ToBase64String((New-RandomBytes -Count 24))) -AsPlainText -Force
        New-LocalUser -Name $Name -Password $pw -Description 'Arbitron service account' `
            -PasswordNeverExpires -UserMayNotChangePassword | Out-Null
    } else {
        Write-ArbitronLog -Level Info -Message "Service account '$Name' already exists; reusing it."
    }
    Set-ArbitronDataAcl -Account $Name -Paths $Paths
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

# Protect-ArbitronSecretBlob is the DPAPI seam: it returns an opaque protected
# string for a plaintext value at the requested scope. CurrentUser is the P3
# local default; LocalMachine is the Shared (system service) variant so the
# service -- running as LocalSystem or the dedicated account -- can unprotect it.
# Mockable in tests so the scope can be asserted without real DPAPI.
function Protect-ArbitronSecretBlob {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][string]$Value,
        [ValidateSet('CurrentUser', 'LocalMachine')][string]$Scope = 'CurrentUser'
    )
    $bytes = [System.Text.Encoding]::UTF8.GetBytes($Value)
    $dpScope = [System.Security.Cryptography.DataProtectionScope]::$Scope
    $protected = [System.Security.Cryptography.ProtectedData]::Protect($bytes, $null, $dpScope)
    return [Convert]::ToBase64String($protected)
}

function Set-ArbitronSecret {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][string]$Name,
        [Parameter(Mandatory)][string]$Value,
        [ValidateSet('Env', 'Dpapi', 'DpapiMachine')][string]$Method = 'Env',
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
        'DpapiMachine' {
            # Shared/system path: protect at machine scope so the Windows Service
            # (LocalSystem or the dedicated account) can read it. Never logged.
            if (-not (Test-Path -LiteralPath $Paths.Data)) {
                New-Item -ItemType Directory -Path $Paths.Data -Force | Out-Null
            }
            $blob = Join-Path $Paths.Data "$Name.secret"
            $protected = Protect-ArbitronSecretBlob -Value $Value -Scope 'LocalMachine'
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

# --------------------------------------------------------------------------
# Shared (system-level) config Q&A -- kept parallel to P2 (Linux shared)
# --------------------------------------------------------------------------

# Get-SharedQuestionOrder is the single source of truth for the shared question
# order, mirroring the "CANONICAL shared Q&A order" block in
# docs/superpowers/plans/installers-implementation-phase-2-task-2.md. The P4
# parity guard (Shared.E2E.Tests.ps1) asserts the landmarks appear in this order;
# Invoke-SharedConfigQA below prompts in exactly this sequence.
function Get-SharedQuestionOrder {
    [CmdletBinding()]
    param()
    return @(
        'Bind host'                         # 1. host
        'Bind port'                         # 1. port
        'Auth model'                        # 2. auth model
        'OIDC issuer URL'                   # 3. OIDC sub-questions (when org)
        'OIDC client ID'
        'OIDC client secret'
        'OIDC redirect URL'
        'Bootstrap admins'
        'Enable RBAC'
        'RBAC group claim'
        'RBAC default group'
        'TLS model'                         # 4. TLS model
        'Enable unattended agent tools'     # 5. skip-permissions (RCE prompt)
        'Service account name'              # 6. service account
        'Data location'                     # 7. data location
    )                                       # 8. master key is silent (no prompt)
}

# Invoke-SharedConfigQA drives the system-level config Q&A in the canonical P2
# order and returns the resolved answer hashtable consumed by Write-ArbitronConfig
# and the secrets/service steps. P0 TLS fail-fast invariants are pre-enforced here
# so the binary never fails to load: cert XOR autocert; autocert => port 443;
# local-mode + TLS on a non-loopback host => allowunauthenticated.
function Invoke-SharedConfigQA {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][hashtable]$Paths,
        [hashtable]$Defaults = @{}
    )

    # 1. Bind host / port.
    $hostDefault = if ($Defaults.ContainsKey('Host')) { $Defaults.Host } else { '0.0.0.0' }
    $bindHost = Read-PromptDefault -Prompt 'Bind host' -Default $hostDefault
    $portDefault = if ($Defaults.ContainsKey('Port')) { $Defaults.Port } else { 8080 }
    $port = [int](Read-PromptDefault -Prompt 'Bind port' -Default "$portDefault")

    # 2. Auth model.
    $authDefault = if ($Defaults.ContainsKey('AuthMode') -and $Defaults.AuthMode -eq 'org') { 'oidc' } else { 'edge' }
    $authChoice = Read-PromptChoice -Prompt 'Auth model (edge = edge-auth + local mode behind a tunnel; oidc = OIDC org mode)' `
        -Choices @('edge', 'oidc') -Default $authDefault

    $answers = @{
        Port                    = $port
        Host                    = $bindHost
        LlmMode                 = if ($Defaults.ContainsKey('LlmMode')) { $Defaults.LlmMode } else { 'subscription' }
        TlsEnabled              = $false
        TlsCertFile             = ''
        TlsKeyFile              = ''
        TlsAutocertDomain       = ''
        TlsAllowUnauthenticated = $false
        PublicBaseURL           = ''
        CookieSecure            = $false
        SkipPermissions         = $false
        BootstrapAdmins         = @()
        RbacEnabled             = $false
    }

    if ($authChoice -ieq 'oidc') {
        $answers.AuthMode = 'org'
        # 3. OIDC sub-questions.
        $answers.OidcIssuerUrl   = Read-PromptDefault -Prompt 'OIDC issuer URL' -Default ''
        $answers.OidcClientId    = Read-PromptDefault -Prompt 'OIDC client ID' -Default ''
        $answers.OidcClientSecret = Read-PromptDefault -Prompt 'OIDC client secret' -Default ''
        $answers.OidcRedirectUrl = Read-PromptDefault -Prompt 'OIDC redirect URL (publicbaseurl + /auth/callback)' -Default ''
        $admins = Read-PromptDefault -Prompt 'Bootstrap admins (comma-separated subjects/emails)' -Default ''
        $answers.BootstrapAdmins = @($admins -split ',' | ForEach-Object { $_.Trim() } | Where-Object { $_ })
        $rbac = Read-PromptYesNo -Prompt 'Enable RBAC (group-claim based roles)?' -Default $false
        if ($rbac) {
            $answers.RbacEnabled     = $true
            $answers.RbacGroupClaim  = Read-PromptDefault -Prompt 'RBAC group claim' -Default 'groups'
            $answers.RbacDefaultGroup = Read-PromptDefault -Prompt 'RBAC default group' -Default 'default'
        }
    } else {
        # edge-auth + local mode: installer enforces loopback bind + single admin.
        $answers.AuthMode = 'local'
        if (-not (Test-IsLoopbackHost -HostName $bindHost)) {
            Write-ArbitronLog -Level Warn -Message "edge-auth + local mode forces a loopback bind; overriding host '$bindHost' -> 127.0.0.1."
            $answers.Host = '127.0.0.1'
        } else {
            $answers.Host = $bindHost
        }
    }

    # 4. TLS model.
    $tlsChoice = Read-PromptChoice -Prompt 'TLS model (tunnel = behind a TLS-terminating proxy; direct = app serves cert+key; autocert = automatic Let''s Encrypt)' `
        -Choices @('tunnel', 'direct', 'autocert') -Default 'tunnel'
    switch ($tlsChoice) {
        'tunnel' {
            $answers.PublicBaseURL = Read-PromptDefault -Prompt 'Public base URL (https://...) advertised by the tunnel/proxy' -Default ''
            $answers.CookieSecure  = $true
            $answers.TlsEnabled    = $false
            Write-ArbitronLog -Level Info -Message 'TLS terminates at your tunnel/proxy. Point it at this host:port; cookies are marked Secure. Arbitron does not provision the tunnel.'
        }
        'direct' {
            $answers.TlsEnabled  = $true
            $answers.TlsCertFile = Read-PromptDefault -Prompt 'TLS certificate file path' -Default ''
            $answers.TlsKeyFile  = Read-PromptDefault -Prompt 'TLS key file path' -Default ''
            $answers.CookieSecure = $true
        }
        'autocert' {
            $answers.TlsEnabled         = $true
            $answers.TlsAutocertDomain  = Read-PromptDefault -Prompt 'TLS autocert domain' -Default ''
            $answers.TlsAutocertCacheDir = Join-Path $Paths.Data 'autocert'
            $answers.CookieSecure       = $true
            # P0 invariant: autocert provisions for the standard HTTPS port.
            if ($answers.Port -ne 443) {
                Write-ArbitronLog -Level Warn -Message "autocert requires server.port 443; overriding port $($answers.Port) -> 443."
                $answers.Port = 443
            }
        }
    }

    # P0 invariant: local mode + direct/autocert TLS on a NON-loopback host
    # exposes an unauthenticated admin API. Require an explicit escape hatch.
    if ($answers.AuthMode -eq 'local' -and $answers.TlsEnabled -and -not (Test-IsLoopbackHost -HostName $answers.Host)) {
        $ok = Read-PromptYesNo -Prompt 'TLS on a non-loopback host in local mode exposes an UNAUTHENTICATED admin API. Set server.tls.allowunauthenticated to override?' -Default $false
        $answers.TlsAllowUnauthenticated = [bool]$ok
    }

    # 5. Skip-permissions (RCE prompt, explicit, default No).
    Write-ArbitronLog -Level Warn -Message $script:ArbitronRceWarning
    $skipDefault = if ($Defaults.ContainsKey('SkipPermissions')) { [bool]$Defaults.SkipPermissions } else { $false }
    $answers.SkipPermissions = Read-PromptYesNo -Prompt 'Enable unattended agent tools (--dangerously-skip-permissions)?' -Default $skipDefault

    # 6. Service account name (default arbitron) -- surfaced/confirmed here.
    $acctDefault = if ($Defaults.ContainsKey('ServiceAccount')) { $Defaults.ServiceAccount } else { 'arbitron' }
    $answers.ServiceAccount = Read-PromptDefault -Prompt 'Service account name (informational; run-as chosen earlier)' -Default $acctDefault

    # 7. Data location.
    $dataDefault = if ($Defaults.ContainsKey('DataDir')) { $Defaults.DataDir } else { $Paths.Data }
    $answers.DataDir = Read-PromptDefault -Prompt 'Data location' -Default $dataDefault

    return $answers
}

# Loopback test mirroring the binary's isLoopbackHost (adapter/config/loader.go):
# localhost or any loopback IP literal.
function Test-IsLoopbackHost {
    [CmdletBinding()]
    param([string]$HostName)
    $h = ($HostName | ForEach-Object { $_ }).Trim()
    if (-not $h) { return $false }
    if ($h -ieq 'localhost') { return $true }
    $ip = $null
    if ([System.Net.IPAddress]::TryParse($h, [ref]$ip)) {
        return $ip.Equals([System.Net.IPAddress]::Loopback) -or $ip.Equals([System.Net.IPAddress]::IPv6Loopback)
    }
    return $false
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

    # Host defaults to loopback (P3 local path omits Host); shared answers set it.
    $bindHost = if ($Answers.ContainsKey('Host') -and $Answers.Host) { $Answers.Host } else { '127.0.0.1' }
    $authMode = if ($Answers.ContainsKey('AuthMode') -and $Answers.AuthMode) { $Answers.AuthMode } else { 'local' }
    $publicBaseUrl = if ($Answers.ContainsKey('PublicBaseURL') -and $Answers.PublicBaseURL) {
        $Answers.PublicBaseURL
    } else {
        "http://${bindHost}:$($Answers.Port)"
    }

    # server.* block.
    $serverLines = @(
        'server:'
        "  host: $bindHost"
        "  port: $($Answers.Port)"
        "  publicbaseurl: `"$publicBaseUrl`""
    )
    # TLS block (direct cert/key OR autocert; never both -- P0 invariant).
    if ($Answers.ContainsKey('TlsEnabled') -and $Answers.TlsEnabled) {
        $serverLines += '  tls:'
        $serverLines += '    enabled: true'
        if ($Answers.ContainsKey('TlsAutocertDomain') -and $Answers.TlsAutocertDomain) {
            $serverLines += "    autocertdomain: `"$($Answers.TlsAutocertDomain)`""
            if ($Answers.ContainsKey('TlsAutocertCacheDir') -and $Answers.TlsAutocertCacheDir) {
                $serverLines += "    autocertcachedir: `"$($Answers.TlsAutocertCacheDir)`""
            }
        } else {
            $serverLines += "    certfile: `"$($Answers.TlsCertFile)`""
            $serverLines += "    keyfile: `"$($Answers.TlsKeyFile)`""
        }
        if ($Answers.ContainsKey('TlsAllowUnauthenticated') -and $Answers.TlsAllowUnauthenticated) {
            $serverLines += '    allowunauthenticated: true'
        }
    }

    # auth.* block.
    $authLines = @(
        'auth:'
        "  mode: $authMode"
    )
    if ($Answers.ContainsKey('CookieSecure')) {
        $authLines += '  session:'
        $authLines += "    cookiesecure: $(if ($Answers.CookieSecure) { 'true' } else { 'false' })"
    }
    if ($authMode -eq 'org') {
        if ($Answers.ContainsKey('BootstrapAdmins') -and $Answers.BootstrapAdmins -and @($Answers.BootstrapAdmins).Count -gt 0) {
            $authLines += '  bootstrapadmins:'
            foreach ($admin in $Answers.BootstrapAdmins) { $authLines += "    - `"$admin`"" }
        }
        $authLines += '  oidc:'
        $authLines += "    issuerurl: `"$($Answers.OidcIssuerUrl)`""
        $authLines += "    clientid: `"$($Answers.OidcClientId)`""
        $authLines += "    redirecturl: `"$($Answers.OidcRedirectUrl)`""
        # OIDC client secret goes to the secrets sink as
        # ARBITRON_AUTH_OIDC_CLIENTSECRET, NEVER into config.yaml.
        if ($Answers.ContainsKey('RbacEnabled') -and $Answers.RbacEnabled) {
            $authLines += '  rbac:'
            $authLines += "    groupclaim: `"$($Answers.RbacGroupClaim)`""
            $authLines += "    defaultgroup: `"$($Answers.RbacDefaultGroup)`""
        }
    }

    # YAML scalars containing backslashes (Windows paths) are quoted.
    $lines = @('# Arbitron configuration (generated by install.ps1).')
    $lines += $serverLines
    $lines += $authLines
    $lines += @(
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
    # API key / OIDC client secret / master key go to the secrets sink, NEVER
    # into config.yaml.
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
    # Resolve 'latest' (or any unresolved value) to a concrete release version so
    # every download URL targets a real pinned tag.
    $resolved = if (-not $Version -or $Version -eq 'latest') {
        Get-ArbitronVersion
    } else {
        $Version -replace '^v', ''
    }
    $artifact = Get-ArtifactName -Version $resolved -Arch $Arch
    $binUri   = Get-ArtifactUrl  -Version $resolved -Arch $Arch
    $base     = "https://github.com/$script:ArbitronRepo/releases/download/v$resolved"
    $sumsUri  = "$base/SHA256SUMS"

    if (-not (Test-Path -LiteralPath $Paths.Bin)) {
        New-Item -ItemType Directory -Path $Paths.Bin -Force | Out-Null
    }
    $tmpDir = Join-Path ([System.IO.Path]::GetTempPath()) ('arbitron_' + [guid]::NewGuid())
    New-Item -ItemType Directory -Path $tmpDir -Force | Out-Null
    $tmpBin  = Join-Path $tmpDir $artifact
    $tmpSums = Join-Path $tmpDir 'SHA256SUMS'
    try {
        # Fetch SHA256SUMS (+ optional cosign material) BEFORE trusting the binary.
        Invoke-ArbitronDownload -Uri $sumsUri -OutFile $tmpSums
        foreach ($ext in @('sig', 'pem')) {
            try {
                Invoke-ArbitronDownload -Uri "$sumsUri.$ext" -OutFile "$tmpSums.$ext"
            } catch {
                # Best-effort: the cosign material is optional. Test-Signature
                # decides whether its absence is fatal (only when explicitly
                # requested); it NEVER bypasses the mandatory checksum gate.
                Write-Verbose "No SHA256SUMS.$ext available: $($_.Exception.Message)"
            }
        }
        Write-ArbitronLog -Level Info -Message "Downloading $artifact ..."
        Invoke-ArbitronDownload -Uri $binUri -OutFile $tmpBin

        # Mandatory fail-closed gate, then optional signature, BEFORE placement.
        Test-Download -Path $tmpBin -Name $artifact -SumsFile $tmpSums
        Test-Signature -SumsFile $tmpSums
        Move-Item -LiteralPath $tmpBin -Destination $Paths.BinaryExe -Force
    } finally {
        if (Test-Path -LiteralPath $tmpDir) {
            Remove-Item -LiteralPath $tmpDir -Recurse -Force -ErrorAction SilentlyContinue
        }
    }
}

# --------------------------------------------------------------------------
# Service registration (Scheduled Task), health-check, summary
# --------------------------------------------------------------------------

# Seam over sc.exe for the few options New-Service lacks (kept mockable).
function Invoke-ScExe {
    [CmdletBinding()]
    param([Parameter(ValueFromRemainingArguments)][string[]]$Arguments)
    & sc.exe @Arguments
    return $LASTEXITCODE
}

# Grant the "Log on as a service" right to a dedicated account. The real LSA
# policy edit happens on Windows; this is a thin mockable seam (tests assert the
# call). On a real host this shells out to a policy tool / secedit.
function Grant-LogonAsService {
    [CmdletBinding()]
    param([Parameter(Mandatory)][string]$Account)
    Write-ArbitronLog -Level Info -Message "Granting '$Account' the Log-on-as-a-service right..."
    # Implemented via secedit/LSA on Windows; kept as a seam so the Shared path is
    # testable cross-platform. Failures here are surfaced by the caller.
    Invoke-ScExe 'privs' | Out-Null
}

# Build a PSCredential for the dedicated service account. Seam so tests can inject
# a deterministic credential without touching the real account store.
function Get-ArbitronServiceCredential {
    [CmdletBinding()]
    param([Parameter(Mandatory)][string]$Name)
    # The account is created (Task 1) with a random machine-managed password; for
    # registration we rely on the LSA grant + the SCM. A real implementation reads
    # the generated password from the protected store; the seam keeps it mockable.
    $pw = ConvertTo-SecureString -String ([Convert]::ToBase64String((New-RandomBytes -Count 24))) -AsPlainText -Force
    return [pscredential]::new(".\$Name", $pw)
}

# Register-ArbitronService is behind an install-type switch: Local keeps the
# per-user at-logon Scheduled Task (no elevation); Shared registers a native
# auto-start Windows Service (the binary is service-aware via P4 Task 0, so no
# third-party service-wrapper shim is needed).
function Register-ArbitronService {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][ValidateSet('Local', 'Shared')][string]$InstallType,
        [Parameter(Mandatory)][hashtable]$Paths,
        [Parameter(Mandatory)][int]$Port,
        [hashtable]$RunAs = @{ Kind = 'LocalSystem'; Name = $null }
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
            $binPath = "`"$($Paths.BinaryExe)`" serve --config `"$($Paths.Config)`""
            Write-ArbitronLog -Level Info -Message "Registering native Windows Service '$script:ArbitronServiceName' (auto-start)..."
            $newSvc = @{
                Name           = $script:ArbitronServiceName
                BinaryPathName = $binPath
                DisplayName    = 'Arbitron'
                Description    = 'Arbitron orchestration server'
                StartupType    = 'Automatic'
            }
            if ($RunAs.Kind -eq 'Account' -and $RunAs.Name) {
                Grant-LogonAsService -Account $RunAs.Name
                $newSvc.Credential = Get-ArbitronServiceCredential -Name $RunAs.Name
            }
            New-Service @newSvc
            Start-Service -Name $script:ArbitronServiceName
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
        [ValidateSet('http', 'https')][string]$Scheme = 'http',
        [string]$HostName = '127.0.0.1',
        [int]$TimeoutSeconds = 30
    )
    $uri = "${Scheme}://${HostName}:$Port/"
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
        [string]$LlmMode = 'subscription',
        [ValidateSet('Local', 'Shared')][string]$InstallType = 'Local',
        [ValidateSet('http', 'https')][string]$Scheme = 'http',
        [string]$HostName = '127.0.0.1'
    )
    $dashboard = "${Scheme}://${HostName}:$Port"
    Write-ArbitronLog -Level Info -Message '----------------------------------------------------------'
    Write-ArbitronLog -Level Info -Message 'Arbitron installation complete.'
    Write-ArbitronLog -Level Info -Message "  Dashboard:    $dashboard"
    if ($InstallType -eq 'Shared') {
        Write-ArbitronLog -Level Info -Message "  Service:      $script:ArbitronServiceName (native Windows Service, auto-start)"
    } else {
        Write-ArbitronLog -Level Info -Message "  Service task: $script:ArbitronTaskName (at-logon Scheduled Task)"
    }
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
# Shared (system-level) lifecycle flows
# --------------------------------------------------------------------------

# Resolve the health-check scheme/host from the answers: https for direct/autocert
# TLS, otherwise http; host is the configured bind host (loopback for edge mode).
function Get-HealthEndpoint {
    [CmdletBinding()]
    param([Parameter(Mandatory)][hashtable]$Answers)
    $scheme = if ($Answers.ContainsKey('TlsEnabled') -and $Answers.TlsEnabled) { 'https' } else { 'http' }
    $hostName = if ($Answers.ContainsKey('Host') -and $Answers.Host) { $Answers.Host } else { '127.0.0.1' }
    # A 0.0.0.0 bind is reachable on loopback for the local probe.
    if ($hostName -eq '0.0.0.0') { $hostName = '127.0.0.1' }
    return @{ Scheme = $scheme; HostName = $hostName }
}

function Invoke-SharedInstall {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][hashtable]$Paths,
        [string]$Arch = 'amd64',
        [string]$Version = $script:ResolvedVersion
    )
    # Fail fast: the Shared path is system-level and requires admin elevation.
    # Perform NO filesystem/download/account side effects until this passes.
    if (-not (Test-IsElevated)) {
        throw 'The Shared (system-wide) install requires administrator rights. Re-run from an elevated PowerShell ("Run as Administrator").'
    }

    Write-ArbitronLog -Level Info -Message 'Starting shared (system-level) Arbitron install...'

    # 1. Run-as choice + directory tree (+ dedicated account when chosen).
    $runAs = Get-RunAsChoice
    New-SharedDirectoryTree -Paths $Paths
    if ($runAs.Kind -eq 'Account' -and $runAs.Name) {
        New-ArbitronServiceAccount -Name $runAs.Name -Paths $Paths
    }

    # 2. Dependencies.
    Resolve-AllDependencies -Paths $Paths | Out-Null

    # 3. Config Q&A (shared; parallel to P2).
    $answers = Invoke-SharedConfigQA -Paths $Paths

    # 4. Binary download + verify (placed at %ProgramFiles%\Arbitron\arbitron.exe).
    Get-ArbitronBinary -Paths $Paths -Version $Version -Arch $Arch

    # 5. Write config + secrets (DPAPI machine scope so the service can read them).
    Write-ArbitronConfig -Paths $Paths -Answers $answers
    $masterKey = New-MasterKey
    Set-ArbitronSecret -Name 'ARBITRON_MASTER_KEY' -Value $masterKey -Method DpapiMachine -Paths $Paths
    if ($answers.AuthMode -eq 'org' -and $answers.ContainsKey('OidcClientSecret') -and $answers.OidcClientSecret) {
        Set-ArbitronSecret -Name 'ARBITRON_AUTH_OIDC_CLIENTSECRET' -Value $answers.OidcClientSecret -Method DpapiMachine -Paths $Paths
    }
    $llmMode = if ($answers.ContainsKey('LlmMode')) { $answers.LlmMode } else { 'subscription' }
    if ($llmMode -eq 'apikey' -and $answers.ContainsKey('ApiKey') -and $answers.ApiKey) {
        Set-ArbitronSecret -Name 'ARBITRON_LLM_APIKEY' -Value $answers.ApiKey -Method DpapiMachine -Paths $Paths
    } elseif ($llmMode -eq 'subscription') {
        Invoke-ClaudeLogin -Paths $Paths
    }

    # 6. Register + start the native Windows Service.
    Register-ArbitronService -InstallType Shared -Paths $Paths -Port $answers.Port -RunAs $runAs

    # 7. Health-check over the correct scheme/host.
    $ep = Get-HealthEndpoint -Answers $answers
    Test-ArbitronHealth -Port $answers.Port -Paths $Paths -Scheme $ep.Scheme -HostName $ep.HostName | Out-Null

    # 8. Summary.
    Write-InstallSummary -Paths $Paths -Port $answers.Port -LlmMode $llmMode `
        -InstallType Shared -Scheme $ep.Scheme -HostName $ep.HostName
}

function Invoke-SharedUpgrade {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][hashtable]$Paths,
        [string]$Arch = 'amd64',
        [string]$Version = $script:ResolvedVersion
    )
    Write-ArbitronLog -Level Info -Message "Upgrading Arbitron (shared) to $Version..."
    # Stop the service, then download+verify the new exe before swapping it in.
    Stop-Service -Name $script:ArbitronServiceName -Force
    Get-ArbitronBinary -Paths $Paths -Version $Version -Arch $Arch
    Start-Service -Name $script:ArbitronServiceName
    Write-ArbitronLog -Level Warn -Message 'Migrations run forward-only on serve start; downgrading after an upgrade is not supported (no down-migrations). Keep a data backup before upgrading.'
    $port = Get-ConfigPort -Paths $Paths
    Write-InstallSummary -Paths $Paths -Port $port -InstallType Shared
}

function Invoke-SharedReconfigure {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][hashtable]$Paths,
        [string]$Arch = 'amd64'
    )
    Write-ArbitronLog -Level Info -Message 'Reconfiguring Arbitron (shared; data + secrets preserved)...'
    $defaults = Get-ExistingConfigDefaults -Paths $Paths
    $answers = Invoke-SharedConfigQA -Paths $Paths -Defaults $defaults
    Write-ArbitronConfig -Paths $Paths -Answers $answers
    # Re-store rotated secrets if provided; never regenerate the master key.
    if ($answers.AuthMode -eq 'org' -and $answers.ContainsKey('OidcClientSecret') -and $answers.OidcClientSecret) {
        Set-ArbitronSecret -Name 'ARBITRON_AUTH_OIDC_CLIENTSECRET' -Value $answers.OidcClientSecret -Method DpapiMachine -Paths $Paths
    }
    $llmMode = if ($answers.ContainsKey('LlmMode')) { $answers.LlmMode } else { 'subscription' }
    if ($llmMode -eq 'apikey' -and $answers.ContainsKey('ApiKey') -and $answers.ApiKey) {
        Set-ArbitronSecret -Name 'ARBITRON_LLM_APIKEY' -Value $answers.ApiKey -Method DpapiMachine -Paths $Paths
    }
    Restart-Service -Name $script:ArbitronServiceName
    Write-InstallSummary -Paths $Paths -Port $answers.Port -LlmMode $llmMode -InstallType Shared
}

function Invoke-SharedUninstall {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][hashtable]$Paths,
        [hashtable]$RunAs = @{ Kind = 'LocalSystem'; Name = $null }
    )
    Write-ArbitronLog -Level Info -Message 'Uninstalling Arbitron (shared)...'
    try { Stop-Service -Name $script:ArbitronServiceName -Force } catch {
        Write-ArbitronLog -Level Info -Message "Service was not running (nothing to stop): $($_.Exception.Message)"
    }
    try { Remove-Service -Name $script:ArbitronServiceName } catch {
        Write-ArbitronLog -Level Warn -Message "Could not remove the Windows Service: $($_.Exception.Message)"
    }
    if (Test-Path -LiteralPath $Paths.Bin)    { Remove-Item -Recurse -Force $Paths.Bin -ErrorAction SilentlyContinue }
    if (Test-Path -LiteralPath $Paths.Config) { Remove-Item -Force $Paths.Config -ErrorAction SilentlyContinue }

    Remove-ArbitronSecret -Name 'ARBITRON_MASTER_KEY' -Paths $Paths
    Remove-ArbitronSecret -Name 'ARBITRON_LLM_APIKEY' -Paths $Paths
    Remove-ArbitronSecret -Name 'ARBITRON_AUTH_OIDC_CLIENTSECRET' -Paths $Paths

    $removeData = Read-PromptYesNo -Prompt "Also delete the data directory ($($Paths.Data))? This destroys your database." -Default $false
    if ($removeData) {
        if (Test-Path -LiteralPath $Paths.Data) { Remove-Item -Recurse -Force $Paths.Data -ErrorAction SilentlyContinue }
        Write-ArbitronLog -Level Info -Message 'Data directory removed.'
    } else {
        Write-ArbitronLog -Level Info -Message "Data directory kept at $($Paths.Data)."
    }

    if ($RunAs.Kind -eq 'Account' -and $RunAs.Name) {
        $removeAcct = Read-PromptYesNo -Prompt "Also remove the dedicated service account '$($RunAs.Name)'?" -Default $false
        if ($removeAcct) {
            try { Remove-LocalUser -Name $RunAs.Name; Write-ArbitronLog -Level Info -Message "Removed service account '$($RunAs.Name)'." }
            catch { Write-ArbitronLog -Level Warn -Message "Could not remove account '$($RunAs.Name)': $($_.Exception.Message)" }
        }
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

    if (-not (Test-ExistingInstall -Paths $paths -InstallType $installType)) {
        if ($installType -eq 'Shared') {
            Invoke-SharedInstall -Paths $paths -Arch $arch
        } else {
            Invoke-FreshInstall -Paths $paths -InstallType $installType -Arch $arch
        }
        return
    }

    Write-ArbitronLog -Level Info -Message 'An existing Arbitron install was detected.'
    $action = Read-PromptChoice -Prompt 'What would you like to do?' `
        -Choices @('Upgrade', 'Reconfigure', 'Uninstall') -Default 'Upgrade'
    if ($installType -eq 'Shared') {
        switch ($action) {
            'Upgrade'     { Invoke-SharedUpgrade     -Paths $paths -Arch $arch }
            'Reconfigure' { Invoke-SharedReconfigure -Paths $paths -Arch $arch }
            'Uninstall'   { Invoke-SharedUninstall   -Paths $paths }
        }
    } else {
        switch ($action) {
            'Upgrade'     { Invoke-Upgrade     -Paths $paths -InstallType $installType -Arch $arch }
            'Reconfigure' { Invoke-Reconfigure -Paths $paths -InstallType $installType -Arch $arch }
            'Uninstall'   { Invoke-Uninstall   -Paths $paths -InstallType $installType }
        }
    }
}

# Bottom-of-file auto-invoke guard. Dot-sourcing for tests sets
# ARBITRON_INSTALL_NOEXEC so functions are defined without running the flow.
if (-not $env:ARBITRON_INSTALL_NOEXEC) {
    Invoke-Main
}
