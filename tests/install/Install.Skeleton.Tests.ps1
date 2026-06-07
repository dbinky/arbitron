# Pester suite for install.ps1 skeleton (P3 Task 1).
#
# Run: Invoke-Pester ./tests/install
#
# install.ps1 is dot-sourced with $env:ARBITRON_INSTALL_NOEXEC set so sourcing
# defines functions without running Invoke-Main.

BeforeAll {
    $script:ScriptPath = Join-Path $PSScriptRoot '..' '..' 'install.ps1'
    $script:ScriptPath = (Resolve-Path $script:ScriptPath).Path
    $env:ARBITRON_INSTALL_NOEXEC = '1'
    . $script:ScriptPath
}

AfterAll {
    Remove-Item Env:\ARBITRON_INSTALL_NOEXEC -ErrorAction SilentlyContinue
}

Describe 'Assert-SupportedPlatform' {
    It 'throws a clear error when not running on Windows' {
        Mock -CommandName Test-IsWindows -MockWith { $false }
        { Assert-SupportedPlatform } | Should -Throw -ExpectedMessage '*Windows*'
    }

    It 'succeeds on Windows' {
        Mock -CommandName Test-IsWindows -MockWith { $true }
        { Assert-SupportedPlatform } | Should -Not -Throw
    }
}

Describe 'Get-Architecture' {
    It 'returns amd64 for AMD64' {
        Get-Architecture -Raw 'AMD64' | Should -Be 'amd64'
    }

    It 'returns arm64 for ARM64' {
        Get-Architecture -Raw 'ARM64' | Should -Be 'arm64'
    }

    It 'throws on an unknown architecture' {
        { Get-Architecture -Raw 'x86' } | Should -Throw -ExpectedMessage '*architecture*'
    }
}

Describe 'Get-InstallType' {
    It 'returns Local for P3' {
        Get-InstallType | Should -Be 'Local'
    }
}

Describe 'Get-ArbitronPaths' {
    BeforeAll {
        $env:LOCALAPPDATA = (Join-Path ([System.IO.Path]::GetTempPath()) 'ArbTestLAD')
        $script:Paths = Get-ArbitronPaths -InstallType Local
    }

    It 'roots all paths under %LOCALAPPDATA%\Arbitron' {
        $root = Join-Path $env:LOCALAPPDATA 'Arbitron'
        foreach ($k in 'Bin', 'Config', 'Data', 'Workspaces', 'Logs') {
            $script:Paths[$k] | Should -BeLike "$root*"
        }
    }

    It 'returns absolute (rooted) paths' {
        foreach ($k in 'Bin', 'BinaryExe', 'Config', 'Data', 'Workspaces', 'Logs') {
            [System.IO.Path]::IsPathRooted($script:Paths[$k]) | Should -BeTrue
        }
    }

    It 'Config ends in config.yaml' {
        $script:Paths.Config | Should -BeLike '*config.yaml'
    }

    It 'BinaryExe ends in arbitron.exe under Bin' {
        # Separator-agnostic so the suite is runnable on non-Windows dev hosts;
        # on Windows Join-Path yields backslashes.
        $norm = $script:Paths.BinaryExe -replace '/', '\'
        $norm | Should -BeLike '*\bin\arbitron.exe'
    }
}

Describe 'Test-ExistingInstall' {
    BeforeEach {
        $script:tmp = Join-Path ([System.IO.Path]::GetTempPath()) ("ArbTI_" + [guid]::NewGuid())
        New-Item -ItemType Directory -Path $script:tmp -Force | Out-Null
        $script:p = @{ Config = (Join-Path $script:tmp 'config.yaml') }
    }
    AfterEach {
        Remove-Item -Recurse -Force $script:tmp -ErrorAction SilentlyContinue
    }

    It 'returns false when config absent' {
        Mock -CommandName Get-ScheduledTask -MockWith { [pscustomobject]@{ TaskName = 'Arbitron' } }
        Test-ExistingInstall -Paths $script:p | Should -BeFalse
    }

    It 'returns false when config present but task missing' {
        Set-Content -Path $script:p.Config -Value 'x'
        Mock -CommandName Get-ScheduledTask -MockWith { throw 'no task' }
        Test-ExistingInstall -Paths $script:p | Should -BeFalse
    }

    It 'returns true when config present AND task registered' {
        Set-Content -Path $script:p.Config -Value 'x'
        Mock -CommandName Get-ScheduledTask -MockWith { [pscustomobject]@{ TaskName = 'Arbitron' } }
        Test-ExistingInstall -Paths $script:p | Should -BeTrue
    }
}

Describe 'Invoke-Main dispatch' {
    BeforeEach {
        Mock -CommandName Assert-SupportedPlatform -MockWith { }
        Mock -CommandName Get-Architecture -MockWith { 'amd64' }
        # Get-InstallType now prompts (Local | Shared); pin it to Local so these
        # cases exercise the local dispatch path. (Shared dispatch is covered by
        # Shared.* suites.)
        Mock -CommandName Get-InstallType -MockWith { 'Local' }
        Mock -CommandName Invoke-FreshInstall -MockWith { }
        Mock -CommandName Invoke-Upgrade -MockWith { }
        Mock -CommandName Invoke-Reconfigure -MockWith { }
        Mock -CommandName Invoke-Uninstall -MockWith { }
    }

    It 'calls Invoke-FreshInstall when no existing install' {
        Mock -CommandName Test-ExistingInstall -MockWith { $false }
        Invoke-Main
        Should -Invoke Invoke-FreshInstall -Times 1 -Exactly
        Should -Invoke Invoke-Upgrade -Times 0 -Exactly
    }

    It 'routes Upgrade to Invoke-Upgrade' {
        Mock -CommandName Test-ExistingInstall -MockWith { $true }
        Mock -CommandName Read-PromptChoice -MockWith { 'Upgrade' }
        Invoke-Main
        Should -Invoke Invoke-Upgrade -Times 1 -Exactly
    }

    It 'routes Reconfigure to Invoke-Reconfigure' {
        Mock -CommandName Test-ExistingInstall -MockWith { $true }
        Mock -CommandName Read-PromptChoice -MockWith { 'Reconfigure' }
        Invoke-Main
        Should -Invoke Invoke-Reconfigure -Times 1 -Exactly
    }

    It 'routes Uninstall to Invoke-Uninstall' {
        Mock -CommandName Test-ExistingInstall -MockWith { $true }
        Mock -CommandName Read-PromptChoice -MockWith { 'Uninstall' }
        Invoke-Main
        Should -Invoke Invoke-Uninstall -Times 1 -Exactly
    }
}

Describe 'Invoke-Main dispatch: Shared install type' {
    # These tests verify that Invoke-Main routes to the Shared lifecycle functions
    # (not the Local ones) when Get-InstallType returns 'Shared'. The Shared.*
    # suites test those functions directly; these tests close the routing seam.
    BeforeEach {
        $env:ProgramFiles = Join-Path ([System.IO.Path]::GetTempPath()) ('ArbDisp_PF_' + [guid]::NewGuid())
        $env:ProgramData  = Join-Path ([System.IO.Path]::GetTempPath()) ('ArbDisp_PD_' + [guid]::NewGuid())
        Mock -CommandName Assert-SupportedPlatform -MockWith { }
        Mock -CommandName Get-Architecture -MockWith { 'amd64' }
        Mock -CommandName Get-InstallType -MockWith { 'Shared' }
        # Local lifecycle stubs -- asserted NOT called for the Shared path.
        Mock -CommandName Invoke-FreshInstall -MockWith { }
        Mock -CommandName Invoke-Upgrade -MockWith { }
        Mock -CommandName Invoke-Reconfigure -MockWith { }
        Mock -CommandName Invoke-Uninstall -MockWith { }
        # Shared lifecycle stubs -- asserted called for the Shared path.
        Mock -CommandName Invoke-SharedInstall -MockWith { }
        Mock -CommandName Invoke-SharedUpgrade -MockWith { }
        Mock -CommandName Invoke-SharedReconfigure -MockWith { }
        Mock -CommandName Invoke-SharedUninstall -MockWith { }
    }

    It 'fresh Shared install calls Invoke-SharedInstall not Invoke-FreshInstall' {
        Mock -CommandName Test-ExistingInstall -MockWith { $false }
        Invoke-Main
        Should -Invoke Invoke-SharedInstall -Times 1 -Exactly
        Should -Invoke Invoke-FreshInstall -Times 0 -Exactly
    }

    It 'existing Shared Upgrade calls Invoke-SharedUpgrade not Invoke-Upgrade' {
        Mock -CommandName Test-ExistingInstall -MockWith { $true }
        Mock -CommandName Read-PromptChoice -MockWith { 'Upgrade' }
        Invoke-Main
        Should -Invoke Invoke-SharedUpgrade -Times 1 -Exactly
        Should -Invoke Invoke-Upgrade -Times 0 -Exactly
    }

    It 'existing Shared Reconfigure calls Invoke-SharedReconfigure not Invoke-Reconfigure' {
        Mock -CommandName Test-ExistingInstall -MockWith { $true }
        Mock -CommandName Read-PromptChoice -MockWith { 'Reconfigure' }
        Invoke-Main
        Should -Invoke Invoke-SharedReconfigure -Times 1 -Exactly
        Should -Invoke Invoke-Reconfigure -Times 0 -Exactly
    }

    It 'existing Shared Uninstall calls Invoke-SharedUninstall not Invoke-Uninstall' {
        Mock -CommandName Test-ExistingInstall -MockWith { $true }
        Mock -CommandName Read-PromptChoice -MockWith { 'Uninstall' }
        Invoke-Main
        Should -Invoke Invoke-SharedUninstall -Times 1 -Exactly
        Should -Invoke Invoke-Uninstall -Times 0 -Exactly
    }
}

Describe 'Write-ArbitronLog redaction' {
    It 'never emits a -Secret value to output' {
        $secret = 'SUPER-SECRET-VALUE-123'
        $out = Write-ArbitronLog -Level Info -Message 'storing key' -Secret $secret 6>&1 |
            Out-String
        $out | Should -Not -BeLike "*$secret*"
    }

    It 'emits the non-secret message' {
        $out = Write-ArbitronLog -Level Info -Message 'hello world' 6>&1 | Out-String
        $out | Should -BeLike '*hello world*'
    }
}

Describe 'Read-Prompt helpers honor non-interactive mode' {
    It 'Read-PromptDefault returns the default without prompting when -NonInteractive' {
        $script:ArbitronNonInteractive = $true
        Mock -CommandName Read-Host -MockWith { 'SHOULD-NOT-BE-CALLED' }
        Read-PromptDefault -Prompt 'Port' -Default '8080' | Should -Be '8080'
        Should -Invoke Read-Host -Times 0 -Exactly
        $script:ArbitronNonInteractive = $false
    }

    It 'Read-PromptYesNo returns the default without prompting when -NonInteractive' {
        $script:ArbitronNonInteractive = $true
        Mock -CommandName Read-Host -MockWith { 'y' }
        Read-PromptYesNo -Prompt 'Continue?' -Default $false | Should -BeFalse
        Should -Invoke Read-Host -Times 0 -Exactly
        $script:ArbitronNonInteractive = $false
    }

    It 'Read-PromptChoice returns the default without prompting when -NonInteractive' {
        $script:ArbitronNonInteractive = $true
        Mock -CommandName Read-Host -MockWith { 'Upgrade' }
        Read-PromptChoice -Prompt 'Action' -Choices @('Upgrade', 'Uninstall') -Default 'Upgrade' |
            Should -Be 'Upgrade'
        Should -Invoke Read-Host -Times 0 -Exactly
        $script:ArbitronNonInteractive = $false
    }
}

Describe 'Test-Sha256' {
    BeforeEach {
        $script:f = Join-Path ([System.IO.Path]::GetTempPath()) ("ArbHash_" + [guid]::NewGuid() + '.bin')
        Set-Content -Path $script:f -Value 'arbitron' -NoNewline
        $script:hash = (Get-FileHash -Path $script:f -Algorithm SHA256).Hash
    }
    AfterEach {
        Remove-Item -Force $script:f -ErrorAction SilentlyContinue
    }

    It 'returns true when the hash matches (case-insensitive)' {
        Test-Sha256 -Path $script:f -ExpectedHash $script:hash.ToLower() | Should -BeTrue
    }

    It 'returns false on a mismatch' {
        Test-Sha256 -Path $script:f -ExpectedHash ('0' * 64) | Should -BeFalse
    }

    It 'returns false on a missing file' {
        Test-Sha256 -Path (Join-Path $script:tmp 'nope.bin') -ExpectedHash $script:hash |
            Should -BeFalse
    }
}
