# PSScriptAnalyzer lint gate for install.ps1 and the install test suite (P3 Task 5).
#
# Asserts zero Error/Warning diagnostics. Run: Invoke-Pester ./tests/install

# Detection runs at discovery time so the -Skip expressions resolve correctly
# (Pester v5 evaluates -Skip during discovery, before BeforeAll runs).
$script:HasAnalyzer = $false
try {
    Import-Module PSScriptAnalyzer -ErrorAction Stop
    $script:HasAnalyzer = $true
} catch {
    Write-Warning "PSScriptAnalyzer not available; lint tests will be skipped. ($($_.Exception.Message))"
}

BeforeAll {
    $script:RepoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..' '..')).Path
    $script:ScriptPath = Join-Path $script:RepoRoot 'install.ps1'
    $script:Settings = Join-Path $PSScriptRoot 'PSScriptAnalyzerSettings.psd1'
}

Describe 'PSScriptAnalyzer' {
    It 'is available (skipped with a note otherwise)' -Skip:(-not $script:HasAnalyzer) {
        $true | Should -BeTrue
    }

    It 'reports zero Error/Warning diagnostics for install.ps1' -Skip:(-not $script:HasAnalyzer) {
        Import-Module PSScriptAnalyzer -ErrorAction Stop
        $diag = Invoke-ScriptAnalyzer -Path $script:ScriptPath -Settings $script:Settings
        if ($diag) {
            $diag | ForEach-Object { Write-Host "  $($_.Severity): $($_.RuleName) ($($_.Line)) $($_.Message)" }
        }
        ($diag | Measure-Object).Count | Should -Be 0
    }

    It 'reports zero Error/Warning diagnostics for the test suite' -Skip:(-not $script:HasAnalyzer) {
        Import-Module PSScriptAnalyzer -ErrorAction Stop
        $files = Get-ChildItem -Path $PSScriptRoot -Filter '*.ps1'
        $diag = foreach ($f in $files) {
            Invoke-ScriptAnalyzer -Path $f.FullName -Settings $script:Settings
        }
        if ($diag) {
            $diag | ForEach-Object { Write-Host "  $($_.Severity): $($_.RuleName) ($($_.ScriptName):$($_.Line)) $($_.Message)" }
        }
        ($diag | Measure-Object).Count | Should -Be 0
    }
}
