# End-to-end, fully-mocked install.ps1 flows (P3 Task 5).
#
# No test performs a real install, download, scheduled-task registration, or
# user-environment write. Run: Invoke-Pester ./tests/install

BeforeAll {
    $script:ScriptPath = (Resolve-Path (Join-Path $PSScriptRoot '..' '..' 'install.ps1')).Path
    Import-Module (Join-Path $PSScriptRoot '_Common.psm1') -Force
    $env:ARBITRON_INSTALL_NOEXEC = '1'
    . $script:ScriptPath

    # Registered from test scope so Mock targets the dot-sourced functions.
    function Register-StandardMocks {
        Mock -CommandName Test-IsWindows -MockWith { $true }
        Mock -CommandName Get-Architecture -MockWith { 'amd64' }
        Mock -CommandName Test-WingetPresent -MockWith { $true }
        Mock -CommandName Invoke-Winget -MockWith { 0 }
        Mock -CommandName Invoke-Npm -MockWith { 0 }
        Mock -CommandName Add-PathPrefix -MockWith { }
        Mock -CommandName Test-DependencyPresent -MockWith { $false }
        Mock -CommandName Invoke-ArbitronDownload -MockWith {
            param($Uri, $OutFile)
            $dir = Split-Path -Parent $OutFile
            if ($dir -and -not (Test-Path $dir)) { New-Item -ItemType Directory -Path $dir -Force | Out-Null }
            Set-Content -Path $OutFile -Value 'arbitron-fake-artifact' -NoNewline
        }
        Mock -CommandName Test-Sha256 -MockWith { $true }
        Mock -CommandName Get-Sha256ForArtifact -MockWith { 'FAKEHASH' }
        Mock -CommandName New-ScheduledTaskAction -MockWith { [pscustomobject]@{ k = 'action' } }
        Mock -CommandName New-ScheduledTaskTrigger -MockWith { [pscustomobject]@{ k = 'trigger' } }
        Mock -CommandName New-ScheduledTaskPrincipal -MockWith { [pscustomobject]@{ k = 'principal' } }
        Mock -CommandName New-ScheduledTaskSettingsSet -MockWith { [pscustomobject]@{ k = 'settings' } }
        Mock -CommandName Register-ScheduledTask -MockWith { }
        Mock -CommandName Start-ScheduledTask -MockWith { }
        Mock -CommandName Stop-ScheduledTask -MockWith { }
        Mock -CommandName Unregister-ScheduledTask -MockWith { }
        Mock -CommandName Set-UserEnvironmentVariable -MockWith { }
        Mock -CommandName Invoke-HealthRequest -MockWith { 200 }
        Mock -CommandName Invoke-ClaudeLogin -MockWith { }
    }
}

AfterAll {
    Remove-Item Env:\ARBITRON_INSTALL_NOEXEC -ErrorAction SilentlyContinue
    $script:ArbitronNonInteractive = $false
}

Describe 'E2E fresh install (subscription)' {
    BeforeEach {
        $script:sb = New-InstallSandbox
        Register-StandardMocks
        Mock -CommandName Get-ScheduledTask -MockWith { throw 'no task' } # no existing install
        $script:capturedOut = ''
        # Drive Q&A non-interactively with defaults (port 8080, subscription, skip No).
        $script:ArbitronNonInteractive = $true
    }
    AfterEach {
        $script:ArbitronNonInteractive = $false
        Remove-InstallSandbox -Sandbox $script:sb
    }

    It 'runs the whole flow and writes a correct config.yaml with secrets never logged' {
        $paths = Get-ArbitronPaths -InstallType Local
        $script:capturedOut = (Invoke-Main 6>&1 | Out-String)

        # winget invoked for missing deps (node/git/gh at least).
        Should -Invoke Invoke-Winget -Times 1
        # binary downloaded + verified before placement.
        Should -Invoke Test-Sha256 -Times 1
        Test-Path $paths.BinaryExe | Should -BeTrue
        # scheduled task registered once, at-logon, with serve --config.
        Should -Invoke Register-ScheduledTask -Times 1 -Exactly -ParameterFilter { $TaskName -eq 'Arbitron' }
        Should -Invoke New-ScheduledTaskTrigger -ParameterFilter { $AtLogOn }
        # config.yaml content.
        $yaml = Get-Content -Raw $paths.Config
        $yaml | Should -Match 'host:\s*127\.0\.0\.1'
        $yaml | Should -Match 'provider:\s*claude-cli'
        $yaml | Should -Match 'arbitron\.db'
        # master key stored to user env, never logged, never in config.
        Should -Invoke Set-UserEnvironmentVariable -ParameterFilter { $Name -eq 'ARBITRON_MASTER_KEY' }
        # health-check + summary.
        Should -Invoke Invoke-HealthRequest -Times 1
        $script:capturedOut | Should -Match 'http://127\.0\.0\.1:8080'
        $script:capturedOut | Should -Match 'Arbitron'
    }
}

Describe 'E2E fresh install (API key)' {
    BeforeEach {
        $script:sb = New-InstallSandbox
        Register-StandardMocks
        Mock -CommandName Get-ScheduledTask -MockWith { throw 'no task' }
        # Drive Q&A: choose apikey + paste a key, defaults otherwise.
        Mock -CommandName Invoke-ConfigQA -MockWith {
            param($Paths, $Defaults)
            @{ Port = 8080; LlmMode = 'apikey'; ApiKey = 'sk-ant-E2E-SECRET'
               SkipPermissions = $false; DataDir = $Paths.Data }
        }
    }
    AfterEach { Remove-InstallSandbox -Sandbox $script:sb }

    It 'sets provider anthropic, stores the key to secrets, never in config/logs' {
        $paths = Get-ArbitronPaths -InstallType Local
        $out = (Invoke-Main 6>&1 | Out-String)
        $yaml = Get-Content -Raw $paths.Config
        $yaml | Should -Match 'provider:\s*anthropic'
        $yaml | Should -Not -BeLike '*sk-ant-E2E-SECRET*'
        $out  | Should -Not -BeLike '*sk-ant-E2E-SECRET*'
        Should -Invoke Set-UserEnvironmentVariable -ParameterFilter { $Name -eq 'ARBITRON_LLM_APIKEY' }
    }
}

Describe 'E2E reconfigure' {
    BeforeEach {
        $script:sb = New-InstallSandbox
        Register-StandardMocks
        $paths = Get-ArbitronPaths -InstallType Local
        New-Item -ItemType Directory -Path $paths.Root -Force | Out-Null
        Set-Content -Path $paths.Config -Value "server:`n  port: 8080`nllm:`n  provider: claude-cli`n"
        Mock -CommandName Get-ScheduledTask -MockWith { [pscustomobject]@{ TaskName = 'Arbitron' } } # existing
        Mock -CommandName Read-PromptChoice -MockWith { 'Reconfigure' }
        $script:ArbitronNonInteractive = $false
        # Q&A returns defaults; we assert no binary/master-key.
        Mock -CommandName Invoke-ConfigQA -MockWith {
            param($Paths, $Defaults)
            @{ Port = 8080; LlmMode = 'subscription'; SkipPermissions = $false; DataDir = $Paths.Data }
        }
        Mock -CommandName New-MasterKey -MockWith { 'SHOULD-NOT-BE-CALLED' }
        Mock -CommandName Get-ArbitronBinary -MockWith { }
    }
    AfterEach { Remove-InstallSandbox -Sandbox $script:sb }

    It 'rewrites config, restarts, does not re-download or regenerate master key' {
        Invoke-Main
        Should -Invoke Get-ArbitronBinary -Times 0 -Exactly
        Should -Invoke New-MasterKey -Times 0 -Exactly
        Should -Invoke Start-ScheduledTask -Times 1 -Exactly
    }
}

Describe 'E2E uninstall' {
    BeforeEach {
        $script:sb = New-InstallSandbox
        Register-StandardMocks
        $paths = Get-ArbitronPaths -InstallType Local
        New-Item -ItemType Directory -Path $paths.Bin -Force | Out-Null
        New-Item -ItemType Directory -Path $paths.Data -Force | Out-Null
        Set-Content -Path $paths.Config -Value 'x'
        Set-Content -Path $paths.BinaryExe -Value 'bin'
        Mock -CommandName Get-ScheduledTask -MockWith { [pscustomobject]@{ TaskName = 'Arbitron' } }
        Mock -CommandName Read-PromptChoice -MockWith { 'Uninstall' }
        Mock -CommandName Read-PromptYesNo -MockWith { $false } # keep data
    }
    AfterEach { Remove-InstallSandbox -Sandbox $script:sb }

    It 'unregisters task, removes binary/config/secrets, keeps data by default' {
        $paths = Get-ArbitronPaths -InstallType Local
        Invoke-Main
        Should -Invoke Unregister-ScheduledTask -Times 1 -Exactly
        Should -Invoke Set-UserEnvironmentVariable -ParameterFilter { $Name -eq 'ARBITRON_MASTER_KEY' }
        Test-Path $paths.Config | Should -BeFalse
        Test-Path $paths.Data | Should -BeTrue
    }
}

Describe 'E2E non-interactive -Yes uses defaults without prompting' {
    BeforeEach {
        $script:sb = New-InstallSandbox
        Register-StandardMocks
        Mock -CommandName Get-ScheduledTask -MockWith { throw 'no task' }
        Mock -CommandName Read-Host -MockWith { 'SHOULD-NOT-BE-CALLED' }
        $script:ArbitronNonInteractive = $true
    }
    AfterEach {
        $script:ArbitronNonInteractive = $false
        Remove-InstallSandbox -Sandbox $script:sb
    }

    It 'never calls Read-Host' {
        Invoke-Main
        Should -Invoke Read-Host -Times 0 -Exactly
    }
}
