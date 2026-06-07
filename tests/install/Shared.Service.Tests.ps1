# Pester suite for install.ps1 Shared service registration + lifecycle (P4 Task 3).
#
# Run: Invoke-Pester ./tests/install
#
# Native Windows Service registration (the binary is service-aware via P4 Task 0,
# so no WinSW/nssm shim). The Service / LocalAccounts cmdlets are shimmed in
# install.ps1 so this suite runs on non-Windows CI/dev hosts.

BeforeAll {
    $script:ScriptPath = (Resolve-Path (Join-Path $PSScriptRoot '..' '..' 'install.ps1')).Path
    $env:ARBITRON_INSTALL_NOEXEC = '1'
    . $script:ScriptPath

    function New-SharedSvcPaths {
        $env:ProgramData = (Join-Path ([System.IO.Path]::GetTempPath()) ("ArbPD_" + [guid]::NewGuid()))
        $env:ProgramFiles = (Join-Path ([System.IO.Path]::GetTempPath()) ("ArbPF_" + [guid]::NewGuid()))
        Get-ArbitronPaths -InstallType Shared
    }
}

AfterAll { Remove-Item Env:\ARBITRON_INSTALL_NOEXEC -ErrorAction SilentlyContinue }

Describe 'Register-ArbitronService -InstallType Shared' {
    BeforeEach {
        $script:p = New-SharedSvcPaths
        Mock -CommandName New-Service -MockWith { }
        Mock -CommandName Set-Service -MockWith { }
        Mock -CommandName Get-Service -MockWith { $null }
        Mock -CommandName Start-Service -MockWith { }
        Mock -CommandName Grant-LogonAsService -MockWith { }
        Mock -CommandName Invoke-ScExe -MockWith { 0 }
    }

    It 'registers an auto-start service named arbitron' {
        Register-ArbitronService -InstallType Shared -Paths $script:p -Port 8080 -RunAs @{ Kind = 'LocalSystem'; Name = $null }
        Should -Invoke New-Service -Times 1 -Exactly -ParameterFilter {
            $Name -eq 'arbitron' -and $StartupType -eq 'Automatic'
        }
    }

    It 'BinaryPathName runs the ProgramFiles exe with serve --config under ProgramData' {
        $expectedExe = $script:p.BinaryExe
        $expectedCfg = $script:p.Config
        Register-ArbitronService -InstallType Shared -Paths $script:p -Port 8080 -RunAs @{ Kind = 'LocalSystem'; Name = $null }
        Should -Invoke New-Service -Times 1 -Exactly -ParameterFilter {
            ($BinaryPathName -like "*$expectedExe*") -and
            ($BinaryPathName -like '*serve*') -and
            ($BinaryPathName -like '*--config*') -and
            ($BinaryPathName -like "*$expectedCfg*")
        }
    }

    It 'registers as LocalSystem with no credential by default' {
        Register-ArbitronService -InstallType Shared -Paths $script:p -Port 8080 -RunAs @{ Kind = 'LocalSystem'; Name = $null }
        Should -Invoke New-Service -Times 1 -Exactly -ParameterFilter { -not $PSBoundParameters.ContainsKey('Credential') }
    }

    It 'registers with the dedicated account credential and grants logon-as-service' {
        $cred = [pscredential]::new('arbitron', (ConvertTo-SecureString 'pw' -AsPlainText -Force))
        Mock -CommandName Get-ArbitronServiceCredential -MockWith { $cred }
        Register-ArbitronService -InstallType Shared -Paths $script:p -Port 8080 -RunAs @{ Kind = 'Account'; Name = 'arbitron' }
        Should -Invoke New-Service -Times 1 -Exactly -ParameterFilter { $Credential -ne $null }
        Should -Invoke Grant-LogonAsService -Times 1 -Exactly -ParameterFilter { $Account -eq 'arbitron' }
    }

    It 'starts the service after registration' {
        Register-ArbitronService -InstallType Shared -Paths $script:p -Port 8080 -RunAs @{ Kind = 'LocalSystem'; Name = $null }
        Should -Invoke Start-Service -Times 1 -Exactly -ParameterFilter { $Name -eq 'arbitron' }
    }

    It 'references no WinSW / nssm shim (relies on the native service-aware binary)' {
        $src = Get-Content -Raw -LiteralPath $script:ScriptPath
        $src | Should -Not -Match '(?i)winsw'
        $src | Should -Not -Match '(?i)nssm'
    }

    It 'BinaryPathName starts with an absolute (rooted) exe path so SCM can find the binary' {
        # Windows Service Manager resolves BinaryPathName at service start with
        # no CWD guarantee. A relative path silently breaks on reboot.
        $script:capturedBpn = $null
        Mock -CommandName New-Service -MockWith { $script:capturedBpn = $PSBoundParameters['BinaryPathName'] }
        Register-ArbitronService -InstallType Shared -Paths $script:p -Port 8080 -RunAs @{ Kind = 'LocalSystem'; Name = $null }
        # The BinaryPathName starts with a quoted or unquoted absolute exe path.
        # Extract the first token (the exe) and verify it is rooted.
        $exeToken = ($script:capturedBpn -replace '^"([^"]+)".*', '$1' -replace '^(\S+).*', '$1')
        [System.IO.Path]::IsPathRooted($exeToken) | Should -BeTrue
    }
}

Describe 'Test-ArbitronHealth scheme/host awareness' {
    It 'probes https when TLS is enabled' {
        $p = New-SharedSvcPaths
        Mock -CommandName Invoke-HealthRequest -MockWith { $script:capturedUri = $Uri; 200 }
        Test-ArbitronHealth -Port 443 -Paths $p -Scheme 'https' -HostName 'arb.example.com' -TimeoutSeconds 2 | Should -BeTrue
        $script:capturedUri | Should -BeLike 'https://arb.example.com:443*'
    }

    It 'defaults to http on loopback when no scheme is given (P3 behavior preserved)' {
        $p = New-SharedSvcPaths
        Mock -CommandName Invoke-HealthRequest -MockWith { $script:capturedUri2 = $Uri; 200 }
        Test-ArbitronHealth -Port 8080 -Paths $p -TimeoutSeconds 2 | Should -BeTrue
        $script:capturedUri2 | Should -BeLike 'http://127.0.0.1:8080*'
    }

    It 'throws pointing at the logs dir on timeout' {
        $p = New-SharedSvcPaths
        Mock -CommandName Invoke-HealthRequest -MockWith { 0 }
        Mock -CommandName Start-Sleep -MockWith { }
        { Test-ArbitronHealth -Port 8080 -Paths $p -TimeoutSeconds 1 } |
            Should -Throw -ExpectedMessage "*$($p.LogFile)*"
    }
}

Describe 'Invoke-SharedUpgrade' {
    BeforeEach {
        $script:p = New-SharedSvcPaths
        $script:order = New-Object System.Collections.Generic.List[string]
        Mock -CommandName Stop-Service -MockWith { $script:order.Add('stop') }
        Mock -CommandName Get-ArbitronBinary -MockWith { $script:order.Add('binary') }
        Mock -CommandName Start-Service -MockWith { $script:order.Add('start') }
        Mock -CommandName Write-ArbitronConfig -MockWith { }
        Mock -CommandName Set-ArbitronSecret -MockWith { }
        Mock -CommandName Get-ConfigPort -MockWith { 8080 }
        Mock -CommandName Write-InstallSummary -MockWith { }
        Mock -CommandName Test-ArbitronHealth -MockWith { $true }
    }

    It 'stops -> downloads/verifies -> starts; never touches config or secrets' {
        Invoke-SharedUpgrade -Paths $script:p -Arch amd64 -Version '2.0.0'
        ($script:order.IndexOf('binary')) | Should -BeGreaterThan ($script:order.IndexOf('stop'))
        ($script:order.IndexOf('start'))  | Should -BeGreaterThan ($script:order.IndexOf('binary'))
        Should -Invoke Write-ArbitronConfig -Times 0 -Exactly
        Should -Invoke Set-ArbitronSecret -Times 0 -Exactly
    }

    It 'surfaces the forward-only migration caveat' {
        $out = Invoke-SharedUpgrade -Paths $script:p -Arch amd64 -Version '2.0.0' 6>&1 | Out-String
        $out | Should -Match '(?i)forward'
        $out | Should -Match '(?i)migrat'
    }
}

Describe 'Invoke-SharedReconfigure' {
    BeforeEach {
        $script:p = New-SharedSvcPaths
        New-Item -ItemType Directory -Path $script:p.Root -Force | Out-Null
        Set-Content -Path $script:p.Config -Value "server:`n  port: 8080`n"
        Mock -CommandName Get-ArbitronBinary -MockWith { }
        Mock -CommandName Invoke-SharedConfigQA -MockWith { @{ Port = 8080; Host = '127.0.0.1'; AuthMode = 'local'; TlsEnabled = $false; SkipPermissions = $false; DataDir = $script:p.Data; LlmMode = 'subscription' } }
        Mock -CommandName Write-ArbitronConfig -MockWith { }
        Mock -CommandName New-MasterKey -MockWith { 'SHOULD-NOT-CALL' }
        Mock -CommandName Set-ArbitronSecret -MockWith { }
        Mock -CommandName Restart-Service -MockWith { }
        Mock -CommandName Write-InstallSummary -MockWith { }
        Mock -CommandName Test-ArbitronHealth -MockWith { $true }
        Mock -CommandName Get-ExistingConfigDefaults -MockWith { @{ Port = 8080 } }
    }
    AfterEach { Remove-Item -Recurse -Force $script:p.Root -ErrorAction SilentlyContinue }

    It 'rewrites config, restarts; no binary re-download, no master-key regen' {
        Invoke-SharedReconfigure -Paths $script:p -Arch amd64
        Should -Invoke Write-ArbitronConfig -Times 1 -Exactly
        Should -Invoke Get-ArbitronBinary -Times 0 -Exactly
        Should -Invoke New-MasterKey -Times 0 -Exactly
        Should -Invoke Restart-Service -Times 1 -Exactly
    }

    It 'stores ARBITRON_AUTH_OIDC_CLIENTSECRET via DpapiMachine when switching to OIDC mode' {
        # Switching to OIDC during reconfigure must persist the client secret.
        # Without the test, a code path error would leave mode:org in config.yaml
        # but no ARBITRON_AUTH_OIDC_CLIENTSECRET in DPAPI -- a silently broken reconfigure.
        Mock -CommandName Invoke-SharedConfigQA -MockWith {
            @{ Port = 8080; Host = '0.0.0.0'; AuthMode = 'org'
               OidcClientSecret = 'top-secret-value'
               TlsEnabled = $false; SkipPermissions = $false; DataDir = $script:p.Data; LlmMode = 'subscription' }
        }
        Invoke-SharedReconfigure -Paths $script:p -Arch amd64
        Should -Invoke Set-ArbitronSecret -Times 1 -Exactly `
            -ParameterFilter { $Name -eq 'ARBITRON_AUTH_OIDC_CLIENTSECRET' -and $Value -eq 'top-secret-value' -and $Method -eq 'DpapiMachine' }
    }
}

Describe 'Invoke-SharedUninstall' {
    BeforeEach {
        $script:p = New-SharedSvcPaths
        New-Item -ItemType Directory -Path $script:p.Bin -Force | Out-Null
        New-Item -ItemType Directory -Path $script:p.Data -Force | Out-Null
        New-Item -ItemType Directory -Path (Split-Path -Parent $script:p.Config) -Force | Out-Null
        Set-Content -Path $script:p.Config -Value 'x'
        Set-Content -Path $script:p.BinaryExe -Value 'bin'
        Mock -CommandName Stop-Service -MockWith { }
        Mock -CommandName Remove-Service -MockWith { }
        Mock -CommandName Get-Service -MockWith { [pscustomobject]@{ Name = 'arbitron' } }
        Mock -CommandName Remove-ArbitronSecret -MockWith { }
        Mock -CommandName Remove-LocalUser -MockWith { }
    }
    AfterEach { Remove-Item -Recurse -Force $script:p.Root -ErrorAction SilentlyContinue }

    It 'removes service + binary + config, keeps data by default' {
        Mock -CommandName Read-PromptYesNo -MockWith { $false }
        Invoke-SharedUninstall -Paths $script:p -RunAs @{ Kind = 'LocalSystem'; Name = $null }
        Should -Invoke Remove-Service -Times 1 -Exactly
        Test-Path $script:p.Config | Should -BeFalse
        Test-Path $script:p.Data | Should -BeTrue
    }

    It 'removes data when confirmed' {
        Mock -CommandName Read-PromptYesNo -MockWith { $true }
        Invoke-SharedUninstall -Paths $script:p -RunAs @{ Kind = 'LocalSystem'; Name = $null }
        Test-Path $script:p.Data | Should -BeFalse
    }

    It 'removes the dedicated account only when confirmed' {
        # First prompt = remove data (no), second = remove account (yes).
        $script:promptN = 0
        Mock -CommandName Read-PromptYesNo -MockWith {
            $script:promptN++
            if ($Prompt -like '*account*') { return $true }
            return $false
        }
        Invoke-SharedUninstall -Paths $script:p -RunAs @{ Kind = 'Account'; Name = 'arbitron' }
        Should -Invoke Remove-LocalUser -Times 1 -Exactly -ParameterFilter { $Name -eq 'arbitron' }
    }

    It 'never removes the account when run-as was LocalSystem' {
        Mock -CommandName Read-PromptYesNo -MockWith { $false }
        Invoke-SharedUninstall -Paths $script:p -RunAs @{ Kind = 'LocalSystem'; Name = $null }
        Should -Invoke Remove-LocalUser -Times 0 -Exactly
    }

    It 'clears all three DPAPI secrets including ARBITRON_AUTH_OIDC_CLIENTSECRET' {
        # Shared install stores 3 secrets: master key, api key, and OIDC client secret.
        # All must be wiped on uninstall so a re-install starts with a clean DPAPI slate.
        Mock -CommandName Read-PromptYesNo -MockWith { $false }
        Invoke-SharedUninstall -Paths $script:p -RunAs @{ Kind = 'LocalSystem'; Name = $null }
        Should -Invoke Remove-ArbitronSecret -Times 3 -Exactly
        Should -Invoke Remove-ArbitronSecret -ParameterFilter { $Name -eq 'ARBITRON_AUTH_OIDC_CLIENTSECRET' }
    }
}
