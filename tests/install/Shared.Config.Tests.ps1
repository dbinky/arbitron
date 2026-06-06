# Pester suite for install.ps1 Shared config Q&A + config.yaml + secrets (P4 Task 2).
#
# Run: Invoke-Pester ./tests/install
#
# The shared Q&A MUST stay parallel to the P2 (Linux shared) question set/order;
# see the "CANONICAL shared Q&A order" block in
# docs/superpowers/plans/installers-implementation-phase-2-task-2.md.

BeforeAll {
    $script:ScriptPath = (Resolve-Path (Join-Path $PSScriptRoot '..' '..' 'install.ps1')).Path
    $env:ARBITRON_INSTALL_NOEXEC = '1'
    . $script:ScriptPath

    function New-SharedTestPaths {
        $env:ProgramData = (Join-Path ([System.IO.Path]::GetTempPath()) ("ArbPD_" + [guid]::NewGuid()))
        $env:ProgramFiles = (Join-Path ([System.IO.Path]::GetTempPath()) ("ArbPF_" + [guid]::NewGuid()))
        Get-ArbitronPaths -InstallType Shared
    }

    # Drives Invoke-SharedConfigQA by feeding scripted answers through the P3
    # prompt helpers. Each prompt is matched by a substring of its prompt string
    # so the answer routing also pins the prompt wording. The map is stashed in a
    # script-scoped variable the Mock bodies read (Pester runs Mock bodies in a
    # scope where the function's local $Map is not visible).
    function Set-ScriptedSharedAnswers {
        param([Parameter(Mandatory)][hashtable]$Map)
        $script:AnswerMap = $Map
        # Match the LONGEST key that is a substring of the prompt so a generic key
        # (e.g. 'RBAC') never shadows a specific one (e.g. 'RBAC default group').
        function script:Find-AnswerForPrompt([string]$Prompt) {
            $best = $null; $bestLen = -1
            foreach ($k in $script:AnswerMap.Keys) {
                if ($Prompt -like "*$k*" -and $k.Length -gt $bestLen) { $best = $k; $bestLen = $k.Length }
            }
            return $best
        }
        Mock -CommandName Read-PromptDefault -MockWith {
            $k = script:Find-AnswerForPrompt $Prompt
            if ($null -ne $k) { return [string]$script:AnswerMap[$k] }
            return $Default
        }
        Mock -CommandName Read-PromptChoice -MockWith {
            $k = script:Find-AnswerForPrompt $Prompt
            if ($null -ne $k) { return [string]$script:AnswerMap[$k] }
            return $(if ($Default) { $Default } else { $Choices[0] })
        }
        Mock -CommandName Read-PromptYesNo -MockWith {
            $k = script:Find-AnswerForPrompt $Prompt
            if ($null -ne $k) { return [bool]$script:AnswerMap[$k] }
            return $Default
        }
    }
}

AfterAll { Remove-Item Env:\ARBITRON_INSTALL_NOEXEC -ErrorAction SilentlyContinue }

Describe 'Invoke-SharedConfigQA question order (parity with P2)' {
    BeforeEach { $script:p = New-SharedTestPaths }

    It 'asks host/port -> auth model -> TLS model -> skip-permissions -> service account -> data location in order' {
        $seq = New-Object System.Collections.Generic.List[string]
        Mock -CommandName Read-PromptDefault -MockWith { $seq.Add($Prompt); $Default }
        Mock -CommandName Read-PromptChoice  -MockWith { $seq.Add($Prompt); $(if ($Default) { $Default } else { $Choices[0] }) }
        Mock -CommandName Read-PromptYesNo   -MockWith { $seq.Add($Prompt); $Default }

        Invoke-SharedConfigQA -Paths $script:p | Out-Null

        # Indices of the canonical landmark prompts must be strictly increasing.
        $idxHost = ($seq.FindIndex({ param($s) $s -like '*host*' }))
        $idxPort = ($seq.FindIndex({ param($s) $s -like '*port*' }))
        $idxAuth = ($seq.FindIndex({ param($s) $s -like '*Auth model*' }))
        $idxTls  = ($seq.FindIndex({ param($s) $s -like '*TLS model*' }))
        $idxSkip = ($seq.FindIndex({ param($s) $s -like '*unattended agent tools*' }))
        $idxAcct = ($seq.FindIndex({ param($s) $s -like '*Service account*' }))
        $idxData = ($seq.FindIndex({ param($s) $s -like '*Data location*' }))

        $idxHost | Should -BeGreaterOrEqual 0
        $idxPort | Should -BeGreaterThan $idxHost
        $idxAuth | Should -BeGreaterThan $idxPort
        $idxTls  | Should -BeGreaterThan $idxAuth
        $idxSkip | Should -BeGreaterThan $idxTls
        $idxAcct | Should -BeGreaterThan $idxSkip
        $idxData | Should -BeGreaterThan $idxAcct
    }

    It 'exposes the canonical shared question list for the parity guard' {
        $list = Get-SharedQuestionOrder
        $list | Should -Not -BeNullOrEmpty
        ($list -join '|') | Should -BeLike '*host*'
        ($list -join '|') | Should -BeLike '*Auth model*'
        ($list -join '|') | Should -BeLike '*TLS model*'
        ($list -join '|') | Should -BeLike '*Service account*'
        ($list -join '|') | Should -BeLike '*Data location*'
    }
}

Describe 'Invoke-SharedConfigQA edge-auth + local mode' {
    BeforeEach { $script:p = New-SharedTestPaths }

    It 'forces loopback bind and local auth mode even if a public host is typed' {
        Set-ScriptedSharedAnswers @{
            'host'        = '0.0.0.0'
            'Auth model'  = 'edge'
            'TLS model'   = 'tunnel'
            'Public'      = 'https://arb.example.com'
        }
        $a = Invoke-SharedConfigQA -Paths $script:p
        $a.AuthMode | Should -Be 'local'
        $a.Host     | Should -Be '127.0.0.1'
    }

    It 'sets PublicBaseURL and Secure cookies under the tunnel/proxy TLS path' {
        Set-ScriptedSharedAnswers @{
            'Auth model' = 'edge'
            'TLS model'  = 'tunnel'
            'Public'     = 'https://arb.example.com'
        }
        $a = Invoke-SharedConfigQA -Paths $script:p
        $a.PublicBaseURL | Should -Be 'https://arb.example.com'
        $a.CookieSecure  | Should -BeTrue
        $a.TlsEnabled    | Should -BeFalse
    }
}

Describe 'Invoke-SharedConfigQA OIDC org mode' {
    BeforeEach { $script:p = New-SharedTestPaths }

    It 'collects issuer/clientid/secret/redirect/bootstrap admins' {
        Set-ScriptedSharedAnswers @{
            'Auth model'    = 'oidc'
            'Issuer'        = 'https://idp.example.com'
            'Client ID'     = 'abc123'
            'client secret' = 's3cr3t'
            'Redirect'      = 'https://arb.example.com/auth/callback'
            'Bootstrap'     = 'admin@example.com'
            'RBAC'          = $false
            'TLS model'     = 'tunnel'
            'Public'        = 'https://arb.example.com'
        }
        $a = Invoke-SharedConfigQA -Paths $script:p
        $a.AuthMode             | Should -Be 'org'
        $a.OidcIssuerUrl        | Should -Be 'https://idp.example.com'
        $a.OidcClientId         | Should -Be 'abc123'
        $a.OidcClientSecret     | Should -Be 's3cr3t'
        $a.OidcRedirectUrl      | Should -Be 'https://arb.example.com/auth/callback'
        ($a.BootstrapAdmins -join ',') | Should -BeLike '*admin@example.com*'
    }

    It 'includes RBAC keys only when RBAC is enabled' {
        Set-ScriptedSharedAnswers @{
            'Auth model'    = 'oidc'
            'Issuer'        = 'https://idp.example.com'
            'Client ID'     = 'abc'
            'client secret' = 'x'
            'Redirect'      = 'https://arb.example.com/auth/callback'
            'Bootstrap'     = 'admin@example.com'
            'RBAC'          = $true
            'group claim'   = 'groups'
            'default group' = 'engineering'
            'TLS model'     = 'tunnel'
            'Public'        = 'https://arb.example.com'
        }
        $a = Invoke-SharedConfigQA -Paths $script:p
        $a.RbacEnabled      | Should -BeTrue
        $a.RbacGroupClaim   | Should -Be 'groups'
        $a.RbacDefaultGroup | Should -Be 'engineering'
    }
}

Describe 'Invoke-SharedConfigQA TLS models' {
    BeforeEach { $script:p = New-SharedTestPaths }

    It 'direct app-TLS sets enabled + cert/key, no autocert' {
        Set-ScriptedSharedAnswers @{
            'Auth model' = 'oidc'
            'Issuer'     = 'https://idp.example.com'; 'Client ID' = 'a'; 'client secret' = 'x'
            'Redirect'   = 'https://arb.example.com/auth/callback'; 'Bootstrap' = 'a@b.com'; 'RBAC' = $false
            'TLS model'  = 'direct'
            'certificate file' = 'C:\certs\arb.crt'
            'key file'         = 'C:\certs\arb.key'
        }
        $a = Invoke-SharedConfigQA -Paths $script:p
        $a.TlsEnabled       | Should -BeTrue
        $a.TlsCertFile      | Should -Be 'C:\certs\arb.crt'
        $a.TlsKeyFile       | Should -Be 'C:\certs\arb.key'
        $a.TlsAutocertDomain | Should -BeNullOrEmpty
    }

    It 'autocert forces server.port to 443' {
        Set-ScriptedSharedAnswers @{
            'Auth model' = 'oidc'
            'Issuer'     = 'https://idp.example.com'; 'Client ID' = 'a'; 'client secret' = 'x'
            'Redirect'   = 'https://arb.example.com/auth/callback'; 'Bootstrap' = 'a@b.com'; 'RBAC' = $false
            'TLS model'  = 'autocert'
            'autocert domain' = 'arb.example.com'
            'port'       = '8080'
        }
        $a = Invoke-SharedConfigQA -Paths $script:p
        $a.TlsAutocertDomain | Should -Be 'arb.example.com'
        $a.Port              | Should -Be 443
    }

    It 'local-mode + non-loopback host + TLS sets allowunauthenticated after confirmation' {
        Set-ScriptedSharedAnswers @{
            'host'       = '10.0.0.5'
            'Auth model' = 'edge'
            'TLS model'  = 'direct'
            'certificate file' = 'C:\certs\arb.crt'
            'key file'         = 'C:\certs\arb.key'
            'allowunauthenticated' = $true
            'unauthenticated' = $true
        }
        $a = Invoke-SharedConfigQA -Paths $script:p
        # Edge keeps the bind loopback by default; but if the operator forces a
        # non-loopback host for direct TLS, the escape hatch must be set.
        if ($a.Host -notin @('127.0.0.1', 'localhost', '::1')) {
            $a.TlsAllowUnauthenticated | Should -BeTrue
        }
    }
}

Describe 'Invoke-SharedConfigQA skip-permissions' {
    BeforeEach { $script:p = New-SharedTestPaths }

    It 'shows the RCE warning and maps a yes answer' {
        Set-ScriptedSharedAnswers @{
            'Auth model' = 'edge'; 'TLS model' = 'tunnel'; 'Public' = 'https://x'
            'unattended agent tools' = $true
        }
        $out = Invoke-SharedConfigQA -Paths $script:p 6>&1 | Out-String
        $out | Should -Match '(?i)RCE'
    }

    It 'maps a no answer to disabled' {
        Set-ScriptedSharedAnswers @{
            'Auth model' = 'edge'; 'TLS model' = 'tunnel'; 'Public' = 'https://x'
            'unattended agent tools' = $false
        }
        $a = Invoke-SharedConfigQA -Paths $script:p
        $a.SkipPermissions | Should -BeFalse
    }
}

Describe 'Write-ArbitronConfig (shared)' {
    BeforeEach { $script:p = New-SharedTestPaths }
    AfterEach  { if (Test-Path $script:p.Root) { Remove-Item -Recurse -Force $script:p.Root -ErrorAction SilentlyContinue } }

    It 'writes config.yaml under %ProgramData% with absolute data/workspace/log paths' {
        $answers = @{
            Port = 8080; Host = '127.0.0.1'; AuthMode = 'local'
            PublicBaseURL = 'https://arb.example.com'; CookieSecure = $true
            TlsEnabled = $false; SkipPermissions = $false
            DataDir = $script:p.Data; LlmMode = 'subscription'
        }
        Write-ArbitronConfig -Paths $script:p -Answers $answers
        Test-Path $script:p.Config | Should -BeTrue
        $content = Get-Content -Raw -LiteralPath $script:p.Config
        $content | Should -BeLike "*$($script:p.Data)*"
        $content | Should -Match '(?i)host:\s*127\.0\.0\.1'
        $content | Should -Match '(?i)mode:\s*local'
        $content | Should -Match '(?i)publicbaseurl'
    }

    It 'renders the OIDC block and bootstrap admins, but never the client secret' {
        $answers = @{
            Port = 443; Host = '0.0.0.0'; AuthMode = 'org'
            OidcIssuerUrl = 'https://idp.example.com'; OidcClientId = 'abc'
            OidcClientSecret = 'SUPER-SECRET'; OidcRedirectUrl = 'https://arb/auth/callback'
            BootstrapAdmins = @('admin@example.com')
            TlsEnabled = $true; TlsAutocertDomain = 'arb.example.com'
            SkipPermissions = $false; DataDir = $script:p.Data; LlmMode = 'subscription'
        }
        Write-ArbitronConfig -Paths $script:p -Answers $answers
        $content = Get-Content -Raw -LiteralPath $script:p.Config
        $content | Should -Match '(?i)mode:\s*org'
        $content | Should -BeLike '*idp.example.com*'
        $content | Should -BeLike '*admin@example.com*'
        $content | Should -BeLike '*autocertdomain*'
        $content | Should -Not -BeLike '*SUPER-SECRET*'
    }

    It 'renders RBAC keys when present' {
        $answers = @{
            Port = 443; Host = '0.0.0.0'; AuthMode = 'org'
            OidcIssuerUrl = 'https://idp'; OidcClientId = 'a'; OidcRedirectUrl = 'https://arb/cb'
            BootstrapAdmins = @('a@b.com')
            RbacEnabled = $true; RbacGroupClaim = 'groups'; RbacDefaultGroup = 'eng'
            TlsEnabled = $false; SkipPermissions = $false; DataDir = $script:p.Data; LlmMode = 'subscription'
        }
        Write-ArbitronConfig -Paths $script:p -Answers $answers
        $content = Get-Content -Raw -LiteralPath $script:p.Config
        $content | Should -BeLike '*groupclaim*'
        $content | Should -BeLike '*defaultgroup*'
    }
}

Describe 'Set-ArbitronSecret -Method DpapiMachine' {
    BeforeEach { $script:p = New-SharedTestPaths }
    AfterEach  { if (Test-Path $script:p.Root) { Remove-Item -Recurse -Force $script:p.Root -ErrorAction SilentlyContinue } }

    It 'protects the value at machine scope and never logs it' {
        Mock -CommandName Protect-ArbitronSecretBlob -MockWith { 'PROTECTED-BLOB' }
        $out = Set-ArbitronSecret -Name 'ARBITRON_MASTER_KEY' -Value 'PLAINTEXT-KEY' -Method DpapiMachine -Paths $script:p 6>&1 | Out-String
        Should -Invoke Protect-ArbitronSecretBlob -Times 1 -Exactly -ParameterFilter { $Scope -eq 'LocalMachine' }
        $out | Should -Not -BeLike '*PLAINTEXT-KEY*'
        $blob = Join-Path $script:p.Data 'ARBITRON_MASTER_KEY.secret'
        Test-Path $blob | Should -BeTrue
        (Get-Content -Raw -LiteralPath $blob) | Should -Not -BeLike '*PLAINTEXT-KEY*'
    }
}

Describe 'Invoke-SharedConfigQA reconfigure pre-fill' {
    BeforeEach { $script:p = New-SharedTestPaths }
    AfterEach  { if (Test-Path $script:p.Root) { Remove-Item -Recurse -Force $script:p.Root -ErrorAction SilentlyContinue } }

    It 'uses existing config values as prompt defaults' {
        New-Item -ItemType Directory -Path $script:p.Root -Force | Out-Null
        Set-Content -Path $script:p.Config -Value "server:`n  port: 9090`n  host: 0.0.0.0`nauth:`n  mode: local`n"
        $seenDefaults = @{}
        Mock -CommandName Read-PromptDefault -MockWith { $seenDefaults[$Prompt] = $Default; $Default }
        Mock -CommandName Read-PromptChoice  -MockWith { $(if ($Default) { $Default } else { $Choices[0] }) }
        Mock -CommandName Read-PromptYesNo   -MockWith { $Default }
        $defaults = Get-ExistingConfigDefaults -Paths $script:p
        Invoke-SharedConfigQA -Paths $script:p -Defaults $defaults | Out-Null
        $portPrompt = $seenDefaults.Keys | Where-Object { $_ -like '*port*' } | Select-Object -First 1
        $seenDefaults[$portPrompt] | Should -Be '9090'
    }
}
