# Pester suite for install.ps1 Shared-path skeleton (P4 Task 1).
#
# Run: Invoke-Pester ./tests/install
#
# install.ps1 is dot-sourced with $env:ARBITRON_INSTALL_NOEXEC set so sourcing
# defines functions without running Invoke-Main. The Shared (system-level)
# branch is Windows-only; the LocalAccounts / icacls / service cmdlets it uses
# are shimmed in install.ps1 so this suite runs on non-Windows CI/dev hosts.

BeforeAll {
    $script:ScriptPath = (Resolve-Path (Join-Path $PSScriptRoot '..' '..' 'install.ps1')).Path
    $env:ARBITRON_INSTALL_NOEXEC = '1'
    . $script:ScriptPath
}

AfterAll { Remove-Item Env:\ARBITRON_INSTALL_NOEXEC -ErrorAction SilentlyContinue }

Describe 'Test-IsElevated' {
    It 'exists as a mockable seam' {
        Get-Command -Name Test-IsElevated -ErrorAction SilentlyContinue | Should -Not -BeNullOrEmpty
    }
}

Describe 'Get-ArbitronPaths -InstallType Shared' {
    BeforeAll {
        $env:ProgramFiles = (Join-Path ([System.IO.Path]::GetTempPath()) 'ArbPF')
        $env:ProgramData  = (Join-Path ([System.IO.Path]::GetTempPath()) 'ArbPD')
        $script:Shared = Get-ArbitronPaths -InstallType Shared
    }

    It 'roots the binary under %ProgramFiles%\Arbitron\arbitron.exe' {
        $norm = $script:Shared.BinaryExe -replace '/', '\'
        $expected = (Join-Path (Join-Path $env:ProgramFiles 'Arbitron') 'arbitron.exe') -replace '/', '\'
        $norm | Should -Be $expected
    }

    It 'roots config/data/workspaces/logs under %ProgramData%\Arbitron' {
        $root = Join-Path $env:ProgramData 'Arbitron'
        $script:Shared.Config     | Should -BeLike "$root*"
        $script:Shared.Data       | Should -BeLike "$root*"
        $script:Shared.Workspaces | Should -BeLike "$root*"
        $script:Shared.Logs       | Should -BeLike "$root*"
    }

    It 'Config ends in config.yaml' {
        $script:Shared.Config | Should -BeLike '*config.yaml'
    }

    It 'returns absolute (rooted) paths' {
        foreach ($k in 'Bin', 'BinaryExe', 'Config', 'Data', 'Workspaces', 'Logs') {
            [System.IO.Path]::IsPathRooted($script:Shared[$k]) | Should -BeTrue
        }
    }
}

Describe 'Get-ArbitronPaths -InstallType Local (no regression)' {
    It 'still roots under %LOCALAPPDATA%\Arbitron' {
        $env:LOCALAPPDATA = (Join-Path ([System.IO.Path]::GetTempPath()) 'ArbLAD')
        $p = Get-ArbitronPaths -InstallType Local
        $root = Join-Path $env:LOCALAPPDATA 'Arbitron'
        $p.Config | Should -BeLike "$root*"
        $p.Data   | Should -BeLike "$root*"
    }
}

Describe 'Invoke-SharedInstall elevation gate' {
    BeforeEach {
        $env:ProgramFiles = (Join-Path ([System.IO.Path]::GetTempPath()) 'ArbPF')
        $env:ProgramData  = (Join-Path ([System.IO.Path]::GetTempPath()) 'ArbPD')
        $script:p = Get-ArbitronPaths -InstallType Shared
        Mock -CommandName New-Item -MockWith { } -ParameterFilter { $ItemType -eq 'Directory' }
        Mock -CommandName Resolve-AllDependencies -MockWith { @() }
        Mock -CommandName Get-ArbitronBinary -MockWith { }
        Mock -CommandName Get-RunAsChoice -MockWith { @{ Kind = 'LocalSystem'; Name = $null } }
        Mock -CommandName New-SharedDirectoryTree -MockWith { }
    }

    It 'fails fast when not elevated and performs no side effects' {
        Mock -CommandName Test-IsElevated -MockWith { $false }
        { Invoke-SharedInstall -Paths $script:p -Arch amd64 -Version '1.0.0' } |
            Should -Throw -ExpectedMessage '*dminist*'
        Should -Invoke New-SharedDirectoryTree -Times 0 -Exactly
        Should -Invoke Get-ArbitronBinary -Times 0 -Exactly
        Should -Invoke Resolve-AllDependencies -Times 0 -Exactly
    }

    It 'proceeds past the gate when elevated' {
        Mock -CommandName Test-IsElevated -MockWith { $true }
        Mock -CommandName Invoke-SharedConfigQA -MockWith { @{ Port = 8080; Host = '127.0.0.1'; AuthMode = 'local'; TlsKind = 'proxy'; SkipPermissions = $false; DataDir = $script:p.Data; ServiceAccount = 'arbitron' } }
        Mock -CommandName Write-ArbitronConfig -MockWith { }
        Mock -CommandName New-MasterKey -MockWith { 'AAAA' }
        Mock -CommandName Set-ArbitronSecret -MockWith { }
        Mock -CommandName Register-ArbitronService -MockWith { }
        Mock -CommandName Test-ArbitronHealth -MockWith { $true }
        Mock -CommandName Write-InstallSummary -MockWith { }
        Mock -CommandName Invoke-ClaudeLogin -MockWith { }
        { Invoke-SharedInstall -Paths $script:p -Arch amd64 -Version '1.0.0' } | Should -Not -Throw
        Should -Invoke New-SharedDirectoryTree -Times 1 -Exactly
    }

    It 'provisions the dedicated service account when Kind=Account is chosen' {
        # When the operator selects a dedicated account (not LocalSystem),
        # Invoke-SharedInstall must call New-ArbitronServiceAccount before
        # writing config or starting the service.
        Mock -CommandName Test-IsElevated -MockWith { $true }
        Mock -CommandName Get-RunAsChoice -MockWith { @{ Kind = 'Account'; Name = 'arbitron' } }
        Mock -CommandName New-ArbitronServiceAccount -MockWith { }
        Mock -CommandName Invoke-SharedConfigQA -MockWith { @{ Port = 8080; Host = '0.0.0.0'; AuthMode = 'local'; TlsKind = 'proxy'; SkipPermissions = $false; DataDir = $script:p.Data; ServiceAccount = 'arbitron' } }
        Mock -CommandName Write-ArbitronConfig -MockWith { }
        Mock -CommandName New-MasterKey -MockWith { 'AAAA' }
        Mock -CommandName Set-ArbitronSecret -MockWith { }
        Mock -CommandName Register-ArbitronService -MockWith { }
        Mock -CommandName Test-ArbitronHealth -MockWith { $true }
        Mock -CommandName Write-InstallSummary -MockWith { }
        Mock -CommandName Invoke-ClaudeLogin -MockWith { }
        Invoke-SharedInstall -Paths $script:p -Arch amd64 -Version '1.0.0'
        Should -Invoke New-ArbitronServiceAccount -Times 1 -Exactly `
            -ParameterFilter { $Name -eq 'arbitron' }
    }
}

Describe 'New-SharedDirectoryTree' {
    It 'creates data, workspaces, and logs under the data root' {
        $env:ProgramData = (Join-Path ([System.IO.Path]::GetTempPath()) 'ArbPD')
        $p = Get-ArbitronPaths -InstallType Shared
        $created = New-Object System.Collections.Generic.List[string]
        Mock -CommandName New-Item -MockWith { $created.Add($Path) } -ParameterFilter { $ItemType -eq 'Directory' }
        New-SharedDirectoryTree -Paths $p
        $created | Should -Contain $p.Data
        $created | Should -Contain $p.Workspaces
        $created | Should -Contain $p.Logs
    }
}

Describe 'Get-RunAsChoice' {
    BeforeEach { $script:ArbitronNonInteractive = $false }
    AfterEach  { $script:ArbitronNonInteractive = $false }

    It 'defaults to LocalSystem and creates no account' {
        Mock -CommandName Read-PromptChoice -MockWith { 'LocalSystem' }
        $choice = Get-RunAsChoice
        $choice.Kind | Should -Be 'LocalSystem'
    }

    It 'captures the default account name arbitron when a dedicated account is chosen' {
        Mock -CommandName Read-PromptChoice -MockWith { 'Account' }
        Mock -CommandName Read-PromptDefault -MockWith { 'arbitron' }
        $choice = Get-RunAsChoice
        $choice.Kind | Should -Be 'Account'
        $choice.Name | Should -Be 'arbitron'
    }

    It 'captures an overridden account name' {
        Mock -CommandName Read-PromptChoice -MockWith { 'Account' }
        Mock -CommandName Read-PromptDefault -MockWith { 'svc_arb' }
        $choice = Get-RunAsChoice
        $choice.Name | Should -Be 'svc_arb'
    }
}

Describe 'New-ArbitronServiceAccount' {
    BeforeEach {
        $env:ProgramData = (Join-Path ([System.IO.Path]::GetTempPath()) 'ArbPD')
        $script:p = Get-ArbitronPaths -InstallType Shared
        Mock -CommandName Set-ArbitronDataAcl -MockWith { }
    }

    It 'creates the local account with a no-interactive-logon posture when absent' {
        Mock -CommandName Get-LocalUser -MockWith { throw 'NotFound' }
        Mock -CommandName New-LocalUser -MockWith { }
        New-ArbitronServiceAccount -Name 'arbitron' -Paths $script:p
        Should -Invoke New-LocalUser -Times 1 -Exactly -ParameterFilter { $Name -eq 'arbitron' }
    }

    It 'does not recreate the account when it already exists' {
        Mock -CommandName Get-LocalUser -MockWith { [pscustomobject]@{ Name = 'arbitron' } }
        Mock -CommandName New-LocalUser -MockWith { }
        New-ArbitronServiceAccount -Name 'arbitron' -Paths $script:p
        Should -Invoke New-LocalUser -Times 0 -Exactly
    }

    It 'applies data/logs ACLs for the account' {
        Mock -CommandName Get-LocalUser -MockWith { [pscustomobject]@{ Name = 'arbitron' } }
        Mock -CommandName New-LocalUser -MockWith { }
        New-ArbitronServiceAccount -Name 'arbitron' -Paths $script:p
        Should -Invoke Set-ArbitronDataAcl -Times 1 -Exactly -ParameterFilter { $Account -eq 'arbitron' }
    }
}

Describe 'Set-ArbitronDataAcl' {
    It 'grants the account modify rights on data and logs via icacls' {
        $env:ProgramData = (Join-Path ([System.IO.Path]::GetTempPath()) 'ArbPD')
        $p = Get-ArbitronPaths -InstallType Shared
        $calls = New-Object System.Collections.Generic.List[object]
        Mock -CommandName Invoke-Icacls -MockWith { $calls.Add($Arguments) }
        Set-ArbitronDataAcl -Account 'arbitron' -Paths $p
        $flat = ($calls | ForEach-Object { $_ -join ' ' }) -join ' || '
        $flat | Should -BeLike "*$($p.Data)*"
        $flat | Should -BeLike "*$($p.Logs)*"
        $flat | Should -BeLike '*arbitron*'
    }
}

Describe 'Get-InstallType routes Shared from the prompt' {
    It 'returns Shared when the operator chooses Shared' {
        Mock -CommandName Read-PromptChoice -MockWith { 'Shared' }
        Get-InstallType | Should -Be 'Shared'
    }

    It 'returns Local when the operator chooses Local' {
        Mock -CommandName Read-PromptChoice -MockWith { 'Local' }
        Get-InstallType | Should -Be 'Local'
    }
}

Describe 'Test-ExistingInstall -InstallType Shared' {
    # The Shared path consults Get-Service (not Get-ScheduledTask) to determine
    # whether a Windows Service exists alongside a config.yaml. This gates the
    # entire Shared lifecycle dispatch: Fresh install vs Upgrade/Reconfigure/Uninstall.
    BeforeEach {
        $script:tmp = Join-Path ([System.IO.Path]::GetTempPath()) ("ArbTIS_" + [guid]::NewGuid())
        New-Item -ItemType Directory -Path $script:tmp -Force | Out-Null
        $script:p = @{ Config = (Join-Path $script:tmp 'config.yaml') }
    }
    AfterEach {
        Remove-Item -Recurse -Force $script:tmp -ErrorAction SilentlyContinue
    }

    It 'returns false when config is absent even if the service is registered' {
        Mock -CommandName Get-Service -MockWith { [pscustomobject]@{ Name = $script:ArbitronServiceName } }
        Test-ExistingInstall -Paths $script:p -InstallType Shared | Should -BeFalse
        Should -Invoke Get-Service -Times 0 -Exactly  # short-circuits before service check
    }

    It 'returns false when config present but the service is not registered' {
        Set-Content -Path $script:p.Config -Value 'x'
        Mock -CommandName Get-Service -MockWith { throw 'service not found' }
        Test-ExistingInstall -Paths $script:p -InstallType Shared | Should -BeFalse
    }

    It 'returns true when config is present AND the Windows Service is registered' {
        Set-Content -Path $script:p.Config -Value 'x'
        Mock -CommandName Get-Service -MockWith { [pscustomobject]@{ Name = $script:ArbitronServiceName } }
        Test-ExistingInstall -Paths $script:p -InstallType Shared | Should -BeTrue
    }

    It 'uses Get-Service (not Get-ScheduledTask) for the Shared detection path' {
        # Verify the right seam is called so a ScheduledTask-only regression is caught.
        Set-Content -Path $script:p.Config -Value 'x'
        Mock -CommandName Get-Service -MockWith { [pscustomobject]@{ Name = $script:ArbitronServiceName } }
        Mock -CommandName Get-ScheduledTask -MockWith { throw 'should not be called for Shared' }
        Test-ExistingInstall -Paths $script:p -InstallType Shared | Should -BeTrue
        Should -Invoke Get-Service -Times 1 -Exactly
        Should -Invoke Get-ScheduledTask -Times 0 -Exactly
    }
}
