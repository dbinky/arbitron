# Pester suite for install.ps1 service registration / health / lifecycle (P3 Task 4).
#
# Run: Invoke-Pester ./tests/install

BeforeAll {
    $script:ScriptPath = (Resolve-Path (Join-Path $PSScriptRoot '..' '..' 'install.ps1')).Path
    $env:ARBITRON_INSTALL_NOEXEC = '1'
    . $script:ScriptPath

    function New-TestPaths {
        $root = Join-Path ([System.IO.Path]::GetTempPath()) ("ArbSvc_" + [guid]::NewGuid())
        @{
            Root      = $root
            Bin       = Join-Path $root 'bin'
            BinaryExe = Join-Path $root 'bin\arbitron.exe'
            Config    = Join-Path $root 'config.yaml'
            Data      = Join-Path $root 'data'
            Workspaces = Join-Path $root 'workspaces'
            Logs      = Join-Path $root 'logs'
            LogFile   = Join-Path $root 'logs\arbitron.log'
            ClaudeConfigDir = Join-Path $root 'claude'
            NpmPrefix = Join-Path $root 'npm'
            Runtime   = Join-Path $root 'runtime'
        }
    }
}

AfterAll { Remove-Item Env:\ARBITRON_INSTALL_NOEXEC -ErrorAction SilentlyContinue }

Describe 'Register-ArbitronService' {
    BeforeEach {
        $script:p = New-TestPaths
        Mock -CommandName New-ScheduledTaskTrigger -MockWith { [pscustomobject]@{ k = 'trig' } }
        Mock -CommandName New-ScheduledTaskAction -MockWith { [pscustomobject]@{ k = 'action' } }
        Mock -CommandName New-ScheduledTaskPrincipal -MockWith { [pscustomobject]@{ k = 'principal' } }
        Mock -CommandName New-ScheduledTaskSettingsSet -MockWith { [pscustomobject]@{ k = 'settings' } }
        Mock -CommandName Register-ScheduledTask -MockWith { }
        Mock -CommandName Start-ScheduledTask -MockWith { }
    }

    It 'builds an at-logon trigger' {
        Register-ArbitronService -InstallType Local -Paths $script:p -Port 8080
        Should -Invoke New-ScheduledTaskTrigger -Times 1 -Exactly -ParameterFilter { $AtLogOn }
    }

    It 'action runs the bin exe with serve and the config flag' {
        $expectedExe = $script:p.BinaryExe
        Register-ArbitronService -InstallType Local -Paths $script:p -Port 8080
        Should -Invoke New-ScheduledTaskAction -Times 1 -Exactly -ParameterFilter {
            ($Execute -eq $expectedExe) -and ($Argument -like '*serve*') -and
            ($Argument -like '*--config*') -and ($Argument -like '*config.yaml*')
        }
    }

    It 'principal is interactive current user, not SYSTEM or RunLevel Highest' {
        Register-ArbitronService -InstallType Local -Paths $script:p -Port 8080
        Should -Invoke New-ScheduledTaskPrincipal -Times 1 -Exactly -ParameterFilter {
            $LogonType -eq 'Interactive' -and $RunLevel -ne 'Highest' -and $UserId -ne 'SYSTEM'
        }
    }

    It 'registers with TaskName Arbitron' {
        Register-ArbitronService -InstallType Local -Paths $script:p -Port 8080
        Should -Invoke Register-ScheduledTask -Times 1 -Exactly -ParameterFilter { $TaskName -eq 'Arbitron' }
    }

    It 'starts the task after registration' {
        Register-ArbitronService -InstallType Local -Paths $script:p -Port 8080
        Should -Invoke Start-ScheduledTask -Times 1 -Exactly
    }
}

Describe 'Test-ArbitronHealth' {
    It 'returns success when health returns 200 within retries' {
        Mock -CommandName Invoke-HealthRequest -MockWith { 200 }
        Test-ArbitronHealth -Port 8080 -Paths (New-TestPaths) -TimeoutSeconds 2 | Should -BeTrue
    }

    It 'throws referencing the log path on timeout' {
        $p = New-TestPaths
        Mock -CommandName Invoke-HealthRequest -MockWith { 0 }
        Mock -CommandName Start-Sleep -MockWith { }
        { Test-ArbitronHealth -Port 8080 -Paths $p -TimeoutSeconds 1 } |
            Should -Throw -ExpectedMessage "*$($p.LogFile)*"
    }
}

Describe 'Write-InstallSummary' {
    It 'prints dashboard URL, task name, and log path' {
        $p = New-TestPaths
        $out = Write-InstallSummary -Paths $p -Port 8080 6>&1 | Out-String
        $out | Should -BeLike '*http://127.0.0.1:8080*'
        $out | Should -BeLike '*Arbitron*'
        $out | Should -BeLike "*$($p.LogFile)*"
    }
}

Describe 'Invoke-FreshInstall orchestration' {
    It 'calls deps -> Q&A -> binary -> config/secrets -> service -> health -> summary in order' {
        $script:order = @()
        $p = New-TestPaths
        Mock -CommandName Resolve-AllDependencies -MockWith { $script:order += 'deps'; @() }
        Mock -CommandName Invoke-ConfigQA -MockWith { $script:order += 'qa'; @{ Port = 8080; LlmMode = 'subscription'; SkipPermissions = $false; DataDir = $p.Data } }
        Mock -CommandName Get-ArbitronBinary -MockWith { $script:order += 'binary' }
        Mock -CommandName Write-ArbitronConfig -MockWith { $script:order += 'config' }
        Mock -CommandName New-MasterKey -MockWith { 'AAAA' }
        Mock -CommandName Set-ArbitronSecret -MockWith { $script:order += 'secret' }
        Mock -CommandName Register-ArbitronService -MockWith { $script:order += 'service' }
        Mock -CommandName Test-ArbitronHealth -MockWith { $script:order += 'health'; $true }
        Mock -CommandName Write-InstallSummary -MockWith { $script:order += 'summary' }
        Mock -CommandName Invoke-ClaudeLogin -MockWith { }
        Invoke-FreshInstall -Paths $p -InstallType Local -Arch amd64 -Version '1.0.0'
        ($script:order -join ',') | Should -BeLike 'deps,qa,binary,config*'
        $script:order[0] | Should -Be 'deps'
        $script:order[1] | Should -Be 'qa'
        $script:order[2] | Should -Be 'binary'
        ($script:order.IndexOf('service')) | Should -BeGreaterThan ($script:order.IndexOf('config'))
        ($script:order.IndexOf('health'))  | Should -BeGreaterThan ($script:order.IndexOf('service'))
        ($script:order.IndexOf('summary')) | Should -BeGreaterThan ($script:order.IndexOf('health'))
    }
}

Describe 'Invoke-Upgrade' {
    BeforeEach {
        $script:p = New-TestPaths
        Mock -CommandName Get-ArbitronBinary -MockWith { }
        Mock -CommandName Stop-ScheduledTask -MockWith { }
        Mock -CommandName Start-ScheduledTask -MockWith { }
        Mock -CommandName Write-ArbitronConfig -MockWith { }
        Mock -CommandName Set-ArbitronSecret -MockWith { }
        Mock -CommandName Write-InstallSummary -MockWith { }
        Mock -CommandName Get-ConfigPort -MockWith { 8080 }
        Mock -CommandName Test-ArbitronHealth -MockWith { $true }
    }

    It 'downloads/verifies, stops, restarts; does not touch config/secrets' {
        Invoke-Upgrade -Paths $script:p -InstallType Local -Arch amd64 -Version '2.0.0'
        Should -Invoke Get-ArbitronBinary -Times 1 -Exactly
        Should -Invoke Stop-ScheduledTask -Times 1 -Exactly
        Should -Invoke Start-ScheduledTask -Times 1 -Exactly
        Should -Invoke Write-ArbitronConfig -Times 0 -Exactly
        Should -Invoke Set-ArbitronSecret -Times 0 -Exactly
    }

    It 'surfaces the forward-only migration caveat' {
        $out = Invoke-Upgrade -Paths $script:p -InstallType Local -Arch amd64 -Version '2.0.0' 6>&1 | Out-String
        $out | Should -Match '(?i)migrat'
        $out | Should -Match '(?i)forward'
    }

    It 'downloads and verifies the binary BEFORE stopping the running task (fail-closed)' {
        # If the order were swapped (stop -> download) a download failure would
        # leave the service down with no binary upgrade. The binary must be
        # verified first so the existing service keeps running on failure.
        $script:order = @()
        Mock -CommandName Get-ArbitronBinary  -MockWith { $script:order += 'binary' }
        Mock -CommandName Stop-ScheduledTask  -MockWith { $script:order += 'stop' }
        Mock -CommandName Start-ScheduledTask -MockWith { $script:order += 'start' }
        Invoke-Upgrade -Paths $script:p -InstallType Local -Arch amd64 -Version '2.0.0'
        ($script:order.IndexOf('binary')) | Should -BeLessThan ($script:order.IndexOf('stop'))
        ($script:order.IndexOf('stop'))   | Should -BeLessThan ($script:order.IndexOf('start'))
    }
}

Describe 'Invoke-FreshInstall LLM-mode seams' {
    BeforeEach {
        $script:p = New-TestPaths
        Mock -CommandName Resolve-AllDependencies -MockWith { @() }
        Mock -CommandName Get-ArbitronBinary -MockWith { }
        Mock -CommandName Write-ArbitronConfig -MockWith { }
        Mock -CommandName New-MasterKey -MockWith { 'AAAA' }
        Mock -CommandName Set-ArbitronSecret -MockWith { }
        Mock -CommandName Register-ArbitronService -MockWith { }
        Mock -CommandName Test-ArbitronHealth -MockWith { $true }
        Mock -CommandName Write-InstallSummary -MockWith { }
        Mock -CommandName Invoke-ClaudeLogin -MockWith { }
    }

    It 'subscription mode calls Invoke-ClaudeLogin once' {
        Mock -CommandName Invoke-ConfigQA -MockWith {
            @{ Port = 8080; LlmMode = 'subscription'; SkipPermissions = $false; DataDir = $script:p.Data }
        }
        Invoke-FreshInstall -Paths $script:p -InstallType Local -Arch amd64 -Version '1.0.0'
        Should -Invoke Invoke-ClaudeLogin -Times 1 -Exactly
    }

    It 'api-key mode stores the key and does NOT call Invoke-ClaudeLogin' {
        Mock -CommandName Invoke-ConfigQA -MockWith {
            @{ Port = 8080; LlmMode = 'apikey'; ApiKey = 'sk-ant-TEST'; SkipPermissions = $false; DataDir = $script:p.Data }
        }
        Invoke-FreshInstall -Paths $script:p -InstallType Local -Arch amd64 -Version '1.0.0'
        Should -Invoke Set-ArbitronSecret -ParameterFilter { $Name -eq 'ARBITRON_LLM_APIKEY' }
        Should -Invoke Invoke-ClaudeLogin -Times 0 -Exactly
    }
}

Describe 'Invoke-Reconfigure' {
    BeforeEach {
        $script:p = New-TestPaths
        New-Item -ItemType Directory -Path $script:p.Root -Force | Out-Null
        Set-Content -Path $script:p.Config -Value "server:`n  port: 8080`n"
        Mock -CommandName Get-ArbitronBinary -MockWith { }
        Mock -CommandName Invoke-ConfigQA -MockWith { @{ Port = 8080; LlmMode = 'subscription'; SkipPermissions = $false; DataDir = $script:p.Data } }
        Mock -CommandName Write-ArbitronConfig -MockWith { }
        Mock -CommandName New-MasterKey -MockWith { 'SHOULD-NOT-CALL' }
        Mock -CommandName Set-ArbitronSecret -MockWith { }
        Mock -CommandName Stop-ScheduledTask -MockWith { }
        Mock -CommandName Start-ScheduledTask -MockWith { }
        Mock -CommandName Write-InstallSummary -MockWith { }
        Mock -CommandName Test-ArbitronHealth -MockWith { $true }
        Mock -CommandName Get-ExistingConfigDefaults -MockWith { @{ Port = 8080 } }
    }
    AfterEach { Remove-Item -Recurse -Force $script:p.Root -ErrorAction SilentlyContinue }

    It 'rewrites config, restarts, does not re-download binary or regenerate master key' {
        Invoke-Reconfigure -Paths $script:p -InstallType Local -Arch amd64
        Should -Invoke Write-ArbitronConfig -Times 1 -Exactly
        Should -Invoke Get-ArbitronBinary -Times 0 -Exactly
        Should -Invoke New-MasterKey -Times 0 -Exactly
        Should -Invoke Start-ScheduledTask -Times 1 -Exactly
    }

    It 'stores ARBITRON_LLM_APIKEY when switching to api-key mode during reconfigure' {
        # Switching LLM mode during reconfigure must persist the new key.
        # If skipped, provider: anthropic is written but the key is never stored --
        # a silently broken reconfigure.
        Mock -CommandName Invoke-ConfigQA -MockWith {
            @{ Port = 8080; LlmMode = 'apikey'; ApiKey = 'sk-ant-RECFG-KEY'
               SkipPermissions = $false; DataDir = $script:p.Data }
        }
        Invoke-Reconfigure -Paths $script:p -InstallType Local -Arch amd64
        Should -Invoke Set-ArbitronSecret -Times 1 -Exactly `
            -ParameterFilter { $Name -eq 'ARBITRON_LLM_APIKEY' -and $Value -eq 'sk-ant-RECFG-KEY' }
    }
}

Describe 'Invoke-Uninstall' {
    BeforeEach {
        $script:p = New-TestPaths
        New-Item -ItemType Directory -Path $script:p.Bin -Force | Out-Null
        New-Item -ItemType Directory -Path $script:p.Data -Force | Out-Null
        Set-Content -Path $script:p.Config -Value 'x'
        Set-Content -Path $script:p.BinaryExe -Value 'bin'
        Mock -CommandName Stop-ScheduledTask -MockWith { }
        Mock -CommandName Unregister-ScheduledTask -MockWith { }
        Mock -CommandName Remove-ArbitronSecret -MockWith { }
    }
    AfterEach { Remove-Item -Recurse -Force $script:p.Root -ErrorAction SilentlyContinue }

    It 'keeps the data dir by default' {
        Mock -CommandName Read-PromptYesNo -MockWith { $false }
        Invoke-Uninstall -Paths $script:p -InstallType Local
        Should -Invoke Unregister-ScheduledTask -Times 1 -Exactly
        Test-Path $script:p.Data | Should -BeTrue
        Test-Path $script:p.Config | Should -BeFalse
    }

    It 'removes the data dir on opt-in' {
        Mock -CommandName Read-PromptYesNo -MockWith { $true }
        Invoke-Uninstall -Paths $script:p -InstallType Local
        Test-Path $script:p.Data | Should -BeFalse
    }

    It 'clears user-env secrets' {
        Mock -CommandName Read-PromptYesNo -MockWith { $false }
        Invoke-Uninstall -Paths $script:p -InstallType Local
        Should -Invoke Remove-ArbitronSecret -Times 2 -Exactly
    }
}
