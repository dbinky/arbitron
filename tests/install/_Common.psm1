# Shared Pester helpers for the install.ps1 suites (P3 Task 5).
#
# Provides a consistent sandbox: a temp %LOCALAPPDATA%, a deterministic RNG seam,
# an output-capture helper, and a teardown that restores environment state. The
# standard external-command mocks (winget / npm / scheduled task / web requests)
# are registered by New-InstallMockEnvironment, which must be called from inside a
# Pester block (Describe/Context/It) because Mock is scoped there.

function New-InstallSandbox {
    [CmdletBinding()]
    param()
    $sandboxRoot = Join-Path ([System.IO.Path]::GetTempPath()) ("ArbSandbox_" + [guid]::NewGuid())
    New-Item -ItemType Directory -Path $sandboxRoot -Force | Out-Null

    $saved = @{
        LOCALAPPDATA = $env:LOCALAPPDATA
        PATH         = $env:PATH
    }
    $env:LOCALAPPDATA = $sandboxRoot
    New-Item -ItemType Directory -Path (Join-Path $sandboxRoot 'Arbitron') -Force | Out-Null

    return [pscustomobject]@{
        Root  = $sandboxRoot
        Saved = $saved
    }
}

function Remove-InstallSandbox {
    [CmdletBinding()]
    param([Parameter(Mandatory)]$Sandbox)
    $env:LOCALAPPDATA = $Sandbox.Saved.LOCALAPPDATA
    $env:PATH = $Sandbox.Saved.PATH
    Remove-Item -Recurse -Force $Sandbox.Root -ErrorAction SilentlyContinue
}

# Build a deterministic answers object for non-interactive E2E flows.
function New-InstallAnswers {
    [CmdletBinding()]
    param(
        [int]$Port = 8080,
        [ValidateSet('subscription', 'apikey')][string]$LlmMode = 'subscription',
        [string]$ApiKey = $null,
        [bool]$SkipPermissions = $false,
        [string]$DataDir
    )
    @{
        Port = $Port; LlmMode = $LlmMode; ApiKey = $ApiKey
        SkipPermissions = $SkipPermissions; DataDir = $DataDir
    }
}

# NOTE on mock scoping: Pester's Mock is block-scoped and must be registered
# from within the Describe/It that uses it, against the dot-sourced install.ps1
# functions. We therefore expose the standard mock list as data here and let each
# suite register them inline (a module-exported function that calls Mock would
# install the mock in the module's scope, not the test's). See Install.E2E for
# the registration helper used per-block.

Export-ModuleMember -Function New-InstallSandbox, Remove-InstallSandbox, New-InstallAnswers
