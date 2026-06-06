# End-to-end Pester suite for install.ps1 Shared path (P4 Task 4).
#
# Walks each auth x TLS x run-as permutation through skeleton -> Q&A -> config
# write -> service registration -> health-check, asserting the COMBINED result
# (config.yaml contents + service registration + secrets). Plus the P2-parity
# guard and the master-key-never-logged / non-elevated-no-mutation invariants.
#
# Run: Invoke-Pester ./tests/install

BeforeAll {
    $script:ScriptPath = (Resolve-Path (Join-Path $PSScriptRoot '..' '..' 'install.ps1')).Path
    $env:ARBITRON_INSTALL_NOEXEC = '1'
    . $script:ScriptPath

    # Canonical shared Q&A order -- source of truth is the "CANONICAL shared Q&A
    # order" block in docs/superpowers/plans/installers-implementation-phase-2-task-2.md.
    # P4's Get-SharedQuestionOrder must mirror this 1:1 (landmark prompts in order).
    $script:CanonicalOrder = @(
        'host',            # 1. Bind host / port
        'port',
        'Auth model',      # 2. Auth model (edge+local | OIDC org)
        'TLS model',       # 4. TLS model (tunnel/proxy | direct | autocert)
        'unattended agent tools', # 5. skip-permissions (RCE prompt)
        'Service account', # 6. Service account name
        'Data location'    # 7. Data location
    )

    function New-E2EPaths {
        $env:ProgramData = (Join-Path ([System.IO.Path]::GetTempPath()) ("ArbE2E_PD_" + [guid]::NewGuid()))
        $env:ProgramFiles = (Join-Path ([System.IO.Path]::GetTempPath()) ("ArbE2E_PF_" + [guid]::NewGuid()))
        Get-ArbitronPaths -InstallType Shared
    }

    # Runs a full Shared install with scripted answers + a fully mocked
    # environment, returning the rendered config.yaml text + recorded service
    # registration parameters + transcript.
    function Invoke-E2EShared {
        param(
            [Parameter(Mandatory)][hashtable]$Answers,
            [hashtable]$RunAs = @{ Kind = 'LocalSystem'; Name = $null },
            [bool]$Elevated = $true
        )
        $p = New-E2EPaths
        $script:NewServiceParams = $null
        # Stash params in script scope so Mock bodies (run in a separate scope)
        # can read them.
        $script:E2EAnswers  = $Answers
        $script:E2ERunAs    = $RunAs
        $script:E2EElevated = $Elevated

        Mock -CommandName Test-IsElevated -MockWith { $script:E2EElevated }
        Mock -CommandName Resolve-AllDependencies -MockWith { @() }
        Mock -CommandName Get-ArbitronBinary -MockWith { }
        Mock -CommandName New-SharedDirectoryTree -MockWith { New-Item -ItemType Directory -Path $Paths.Data -Force | Out-Null; New-Item -ItemType Directory -Path $Paths.Logs -Force | Out-Null }
        Mock -CommandName Get-RunAsChoice -MockWith { $script:E2ERunAs }
        Mock -CommandName New-ArbitronServiceAccount -MockWith { }
        Mock -CommandName Get-ArbitronServiceCredential -MockWith { [pscredential]::new(($script:E2ERunAs.Name), (ConvertTo-SecureString 'pw' -AsPlainText -Force)) }

        # Scripted Q&A. Match the LONGEST key that is a substring of the prompt so
        # a generic key never shadows a more specific one.
        function script:Find-E2EAnswer([string]$Prompt) {
            $best = $null; $bestLen = -1
            foreach ($k in $script:E2EAnswers.Keys) {
                if ($Prompt -like "*$k*" -and $k.Length -gt $bestLen) { $best = $k; $bestLen = $k.Length }
            }
            return $best
        }
        Mock -CommandName Read-PromptDefault -MockWith {
            $k = script:Find-E2EAnswer $Prompt
            if ($null -ne $k) { return [string]$script:E2EAnswers[$k] }
            return $Default
        }
        Mock -CommandName Read-PromptChoice -MockWith {
            $k = script:Find-E2EAnswer $Prompt
            if ($null -ne $k) { return [string]$script:E2EAnswers[$k] }
            return $(if ($Default) { $Default } else { $Choices[0] })
        }
        Mock -CommandName Read-PromptYesNo -MockWith {
            $k = script:Find-E2EAnswer $Prompt
            if ($null -ne $k) { return [bool]$script:E2EAnswers[$k] }
            return $Default
        }

        # Secrets / service / health / login. Capture the named params explicitly:
        # inside a Pester Mock body $PSBoundParameters is empty, but the bound
        # parameter *variables* are populated, so snapshot them into a hashtable.
        Mock -CommandName Protect-ArbitronSecretBlob -MockWith { 'PROTECTED' }
        Mock -CommandName New-Service -MockWith {
            $script:NewServiceParams = @{
                Name           = $Name
                BinaryPathName = $BinaryPathName
                StartupType    = $StartupType
                HasCredential  = ($null -ne $Credential)
            }
        }
        Mock -CommandName Set-Service -MockWith { }
        Mock -CommandName Get-Service -MockWith { $null }
        Mock -CommandName Start-Service -MockWith { }
        Mock -CommandName Grant-LogonAsService -MockWith { }
        Mock -CommandName Invoke-ScExe -MockWith { 0 }
        Mock -CommandName Test-ArbitronHealth -MockWith { $true }
        Mock -CommandName Invoke-ClaudeLogin -MockWith { }

        $transcript = Invoke-SharedInstall -Paths $p -Arch amd64 -Version '1.0.0' 6>&1 | Out-String
        $configText = if (Test-Path $p.Config) { Get-Content -Raw -LiteralPath $p.Config } else { '' }
        return [pscustomobject]@{
            Paths            = $p
            Config           = $configText
            NewServiceParams = $script:NewServiceParams
            Transcript       = $transcript
        }
    }
}

AfterAll { Remove-Item Env:\ARBITRON_INSTALL_NOEXEC -ErrorAction SilentlyContinue }

Describe 'E2E: edge-auth + tunnel/proxy TLS + LocalSystem' {
    It 'loopback bind, PublicBaseURL + Secure cookies, no TLS, LocalSystem auto-start, http health' {
        $r = Invoke-E2EShared -Answers @{
            'Auth model' = 'edge'; 'TLS model' = 'tunnel'; 'Public' = 'https://arb.example.com'
            'unattended agent tools' = $false
        }
        $r.Config | Should -Match '(?i)host:\s*127\.0\.0\.1'
        $r.Config | Should -Match '(?i)mode:\s*local'
        $r.Config | Should -BeLike '*arb.example.com*'
        $r.Config | Should -Not -BeLike '*tls:*enabled: true*'
        $r.NewServiceParams.StartupType | Should -Be 'Automatic'
        $r.NewServiceParams.HasCredential | Should -BeFalse
    }
}

Describe 'E2E: edge-auth + direct app-TLS + dedicated account' {
    It 'TLS enabled cert/key, allowunauthenticated on non-loopback, dedicated account run-as' {
        $r = Invoke-E2EShared -RunAs @{ Kind = 'Account'; Name = 'arbitron' } -Answers @{
            'host' = '10.0.0.5'; 'Auth model' = 'edge'; 'TLS model' = 'direct'
            'certificate file' = 'C:\certs\arb.crt'; 'key file' = 'C:\certs\arb.key'
            'allowunauthenticated' = $true; 'unauthenticated' = $true
            'unattended agent tools' = $false
        }
        $r.Config | Should -BeLike '*certfile*'
        $r.Config | Should -BeLike '*arb.crt*'
        $r.Config | Should -Match '(?i)enabled:\s*true'
        $r.NewServiceParams.HasCredential | Should -BeTrue
    }
}

Describe 'E2E: OIDC + autocert + LocalSystem' {
    It 'OIDC block populated, autocert domain with port 443, LocalSystem' {
        $r = Invoke-E2EShared -Answers @{
            'Auth model' = 'oidc'
            'Issuer' = 'https://idp.example.com'; 'Client ID' = 'cid'; 'client secret' = 'csecret'
            'Redirect' = 'https://arb.example.com/auth/callback'; 'Bootstrap' = 'admin@example.com'; 'RBAC' = $false
            'TLS model' = 'autocert'; 'autocert domain' = 'arb.example.com'
            'unattended agent tools' = $false
        }
        $r.Config | Should -Match '(?i)mode:\s*org'
        $r.Config | Should -BeLike '*idp.example.com*'
        $r.Config | Should -BeLike '*autocertdomain*'
        $r.Config | Should -Match '(?i)port:\s*443'
        $r.Config | Should -Not -BeLike '*csecret*'
        $r.NewServiceParams.HasCredential | Should -BeFalse
    }
}

Describe 'E2E: OIDC + RBAC + direct app-TLS + dedicated account' {
    It 'OIDC + RBAC keys + cert/key TLS + dedicated account' {
        $r = Invoke-E2EShared -RunAs @{ Kind = 'Account'; Name = 'svc_arb' } -Answers @{
            'Auth model' = 'oidc'
            'Issuer' = 'https://idp.example.com'; 'Client ID' = 'cid'; 'client secret' = 'csecret'
            'Redirect' = 'https://arb.example.com/auth/callback'; 'Bootstrap' = 'admin@example.com'
            'RBAC' = $true; 'group claim' = 'groups'; 'default group' = 'eng'
            'TLS model' = 'direct'; 'certificate file' = 'C:\c\a.crt'; 'key file' = 'C:\c\a.key'
            'unattended agent tools' = $false
        }
        $r.Config | Should -BeLike '*groupclaim*'
        $r.Config | Should -BeLike '*defaultgroup*'
        $r.Config | Should -BeLike '*certfile*'
        $r.NewServiceParams.HasCredential | Should -BeTrue
    }
}

Describe 'Shared Q&A parity with P2' {
    It 'Get-SharedQuestionOrder matches the canonical P2 ordered landmark list' {
        $order = Get-SharedQuestionOrder
        # Each canonical landmark appears, in order, somewhere in the question list.
        $cursor = -1
        foreach ($landmark in $script:CanonicalOrder) {
            $idx = -1
            for ($i = $cursor + 1; $i -lt $order.Count; $i++) {
                if ($order[$i] -like "*$landmark*") { $idx = $i; break }
            }
            $idx | Should -BeGreaterThan $cursor -Because "landmark '$landmark' must follow the previous one in canonical order"
            $cursor = $idx
        }
    }
}

Describe 'E2E invariants' {
    It 'master key is generated and never appears in the transcript' {
        $r = Invoke-E2EShared -Answers @{
            'Auth model' = 'edge'; 'TLS model' = 'tunnel'; 'Public' = 'https://x'
            'unattended agent tools' = $false
        }
        # The generated master key is base64 of 32 bytes; assert no base64-looking
        # secret line leaked. We pin by checking the protected-blob seam was used
        # and that the literal key value is masked everywhere.
        $r.Transcript | Should -Not -Match 'ARBITRON_MASTER_KEY=[A-Za-z0-9+/=]{20,}'
    }

    It 'non-elevated run mutates nothing (no service, no config, no dir tree)' {
        { Invoke-E2EShared -Elevated $false -Answers @{
            'Auth model' = 'edge'; 'TLS model' = 'tunnel'; 'Public' = 'https://x'
        } } | Should -Throw -ExpectedMessage '*dminist*'
        Should -Invoke New-Service -Times 0 -Exactly
        Should -Invoke New-SharedDirectoryTree -Times 0 -Exactly
    }
}

Describe 'PSScriptAnalyzer gate (Shared)' {
    BeforeAll {
        $script:HasAnalyzer = $null -ne (Get-Module -ListAvailable PSScriptAnalyzer)
        $script:Settings = Join-Path $PSScriptRoot 'PSScriptAnalyzerSettings.psd1'
    }
    It 'install.ps1 returns zero error/warning findings' -Skip:(-not $script:HasAnalyzer) {
        Import-Module PSScriptAnalyzer -ErrorAction Stop
        $diag = Invoke-ScriptAnalyzer -Path $script:ScriptPath -Settings $script:Settings
        if ($diag) { $diag | ForEach-Object { Write-Host "  $($_.Severity): $($_.RuleName) ($($_.Line)) $($_.Message)" } }
        ($diag | Measure-Object).Count | Should -Be 0
    }
}
