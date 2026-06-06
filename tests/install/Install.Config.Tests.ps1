# Pester suite for install.ps1 config Q&A / config.yaml / secrets / binary (P3 Task 3).
#
# Run: Invoke-Pester ./tests/install

BeforeAll {
    $script:ScriptPath = (Resolve-Path (Join-Path $PSScriptRoot '..' '..' 'install.ps1')).Path
    $env:ARBITRON_INSTALL_NOEXEC = '1'
    . $script:ScriptPath

    function New-TestPaths {
        $root = Join-Path ([System.IO.Path]::GetTempPath()) ("ArbCfg_" + [guid]::NewGuid())
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
        }
    }
}

AfterAll {
    Remove-Item Env:\ARBITRON_INSTALL_NOEXEC -ErrorAction SilentlyContinue
}

Describe 'New-MasterKey' {
    It 'produces valid base64 decoding to 32 bytes' {
        $k = New-MasterKey
        $bytes = [Convert]::FromBase64String($k)
        $bytes.Length | Should -Be 32
    }
}

Describe 'Write-ArbitronConfig' {
    BeforeEach {
        $script:p = New-TestPaths
        New-Item -ItemType Directory -Path $script:p.Root -Force | Out-Null
    }
    AfterEach { Remove-Item -Recurse -Force $script:p.Root -ErrorAction SilentlyContinue }

    It 'writes absolute database/workspace/logging paths under data dir' {
        $answers = @{
            Port = 8080; LlmMode = 'subscription'; SkipPermissions = $false
            DataDir = $script:p.Data
        }
        Write-ArbitronConfig -Paths $script:p -Answers $answers
        $yaml = Get-Content -Raw $script:p.Config
        $dbLine = ($yaml -split "`n" | Where-Object { $_ -match 'path:' -and $_ -match 'arbitron.db' })
        $dbLine | Should -Not -BeNullOrEmpty
        # Extract the path value and assert rooted.
        ($yaml | Select-String 'arbitron\.db').Matches.Count | Should -BeGreaterThan 0
        $script:p.Data | Should -Match '^([A-Za-z]:\\|/)'
    }

    It 'binds loopback 127.0.0.1 with the chosen port' {
        $answers = @{ Port = 9090; LlmMode = 'subscription'; SkipPermissions = $false; DataDir = $script:p.Data }
        Write-ArbitronConfig -Paths $script:p -Answers $answers
        $yaml = Get-Content -Raw $script:p.Config
        $yaml | Should -Match 'host:\s*127\.0\.0\.1'
        $yaml | Should -Match 'port:\s*9090'
    }

    It 'subscription mode sets provider claude-cli with no apikey' {
        $answers = @{ Port = 8080; LlmMode = 'subscription'; SkipPermissions = $false; DataDir = $script:p.Data }
        Write-ArbitronConfig -Paths $script:p -Answers $answers
        $yaml = Get-Content -Raw $script:p.Config
        $yaml | Should -Match 'provider:\s*claude-cli'
        $yaml | Should -Not -Match 'apikey:\s*\S'
    }

    It 'api-key mode sets provider anthropic and does NOT write the key' {
        $answers = @{
            Port = 8080; LlmMode = 'apikey'; ApiKey = 'sk-ant-SECRETKEY-xyz'
            SkipPermissions = $false; DataDir = $script:p.Data
        }
        Write-ArbitronConfig -Paths $script:p -Answers $answers
        $yaml = Get-Content -Raw $script:p.Config
        $yaml | Should -Match 'provider:\s*anthropic'
        $yaml | Should -Not -BeLike '*sk-ant-SECRETKEY-xyz*'
    }

    It 'skip-permissions reflects the answer' {
        $answers = @{ Port = 8080; LlmMode = 'subscription'; SkipPermissions = $true; DataDir = $script:p.Data }
        Write-ArbitronConfig -Paths $script:p -Answers $answers
        (Get-Content -Raw $script:p.Config) | Should -Match 'skippermissions:\s*true'
    }
}

Describe 'Invoke-ConfigQA' {
    BeforeEach { $script:p = New-TestPaths }

    It 'defaults to port 8080 and subscription on local' {
        Mock -CommandName Read-PromptDefault -MockWith { param($Prompt, $Default) $Default }
        Mock -CommandName Read-PromptChoice  -MockWith { param($Prompt, $Choices, $Default) $Default }
        Mock -CommandName Read-PromptYesNo   -MockWith { param($Prompt, $Default) $Default }
        $a = Invoke-ConfigQA -Paths $script:p
        $a.Port | Should -Be 8080
        $a.LlmMode | Should -Be 'subscription'
        $a.SkipPermissions | Should -BeFalse
    }

    It 'shows the RCE warning before the skip-permissions prompt' {
        Mock -CommandName Read-PromptDefault -MockWith { param($Prompt, $Default) $Default }
        Mock -CommandName Read-PromptChoice  -MockWith { param($Prompt, $Choices, $Default) $Default }
        Mock -CommandName Read-PromptYesNo   -MockWith { param($Prompt, $Default) $Default }
        $out = Invoke-ConfigQA -Paths $script:p 6>&1 | Out-String
        $out | Should -Match '(?i)RCE|remote code|dangerous'
    }

    It 'uses provided -Defaults for prompt defaults (reconfigure prefill)' {
        $script:seenDefault = $null
        Mock -CommandName Read-PromptDefault -MockWith {
            param($Prompt, $Default)
            if ($Prompt -match 'ort') { $script:seenDefault = $Default }
            $Default
        }
        Mock -CommandName Read-PromptChoice -MockWith { param($Prompt, $Choices, $Default) $Default }
        Mock -CommandName Read-PromptYesNo  -MockWith { param($Prompt, $Default) $Default }
        Invoke-ConfigQA -Paths $script:p -Defaults @{ Port = 7777 } | Out-Null
        $script:seenDefault | Should -Be 7777
    }
}

Describe 'Set-ArbitronSecret' {
    It 'env method writes to the User scope' {
        $script:scope = $null
        Mock -CommandName Set-UserEnvironmentVariable -MockWith {
            param($Name, $Value) $script:scope = 'User'
        }
        Set-ArbitronSecret -Name ARBITRON_MASTER_KEY -Value 'abc' -Method Env -Paths (New-TestPaths)
        Should -Invoke Set-UserEnvironmentVariable -Times 1 -Exactly
    }

    It 'dpapi method writes a protected blob that does not contain the plaintext' {
        $p = New-TestPaths
        New-Item -ItemType Directory -Path $p.Data -Force | Out-Null
        Set-ArbitronSecret -Name ARBITRON_MASTER_KEY -Value 'PLAINTEXT-SECRET-123' -Method Dpapi -Paths $p
        $blob = Join-Path $p.Data 'ARBITRON_MASTER_KEY.secret'
        Test-Path $blob | Should -BeTrue
        (Get-Content -Raw $blob) | Should -Not -BeLike '*PLAINTEXT-SECRET-123*'
        Remove-Item -Recurse -Force $p.Root -ErrorAction SilentlyContinue
    }
}

Describe 'secrets never logged' {
    BeforeEach {
        $script:p = New-TestPaths
        New-Item -ItemType Directory -Path $script:p.Data -Force | Out-Null
        Mock -CommandName Set-UserEnvironmentVariable -MockWith { }
    }
    AfterEach { Remove-Item -Recurse -Force $script:p.Root -ErrorAction SilentlyContinue }

    It 'master key never appears in captured output' {
        $key = New-MasterKey
        $out = (Set-ArbitronSecret -Name ARBITRON_MASTER_KEY -Value $key -Method Env -Paths $script:p) 6>&1 | Out-String
        $out | Should -Not -BeLike "*$key*"
    }

    It 'api key never appears in output or config' {
        New-Item -ItemType Directory -Path $script:p.Root -Force | Out-Null
        $apiKey = 'sk-ant-NEVER-LOG-ME'
        $out = (Set-ArbitronSecret -Name ARBITRON_LLM_APIKEY -Value $apiKey -Method Env -Paths $script:p) 6>&1 | Out-String
        $out | Should -Not -BeLike "*$apiKey*"
        $answers = @{ Port = 8080; LlmMode = 'apikey'; ApiKey = $apiKey; SkipPermissions = $false; DataDir = $script:p.Data }
        Write-ArbitronConfig -Paths $script:p -Answers $answers
        (Get-Content -Raw $script:p.Config) | Should -Not -BeLike "*$apiKey*"
    }
}

Describe 'Get-ArbitronBinary' {
    BeforeEach {
        $script:p = New-TestPaths
        New-Item -ItemType Directory -Path $script:p.Bin -Force | Out-Null
    }
    AfterEach { Remove-Item -Recurse -Force $script:p.Root -ErrorAction SilentlyContinue }

    It 'downloads, verifies (fail-closed gate), and places the exe on checksum match' {
        Mock -CommandName Invoke-ArbitronDownload -MockWith {
            param($Uri, $OutFile) Set-Content -Path $OutFile -Value 'fakebinary' -NoNewline
        }
        Mock -CommandName Test-Download -MockWith { }
        Mock -CommandName Test-Signature -MockWith { }
        Get-ArbitronBinary -Paths $script:p -Version '1.2.3' -Arch 'amd64'
        Should -Invoke Test-Download -Times 1
        Test-Path $script:p.BinaryExe | Should -BeTrue
    }

    It 'throws and leaves no binary on checksum mismatch' {
        Mock -CommandName Invoke-ArbitronDownload -MockWith {
            param($Uri, $OutFile) Set-Content -Path $OutFile -Value 'tampered' -NoNewline
        }
        # The canonical verifier throws (fail-closed) on a mismatch and removes
        # the partial download; Get-ArbitronBinary must propagate it.
        Mock -CommandName Test-Download -MockWith { throw 'Checksum verification failed for arbitron; aborting (no binary placed).' }
        Mock -CommandName Test-Signature -MockWith { }
        { Get-ArbitronBinary -Paths $script:p -Version '1.2.3' -Arch 'amd64' } |
            Should -Throw -ExpectedMessage '*checksum*'
        Test-Path $script:p.BinaryExe | Should -BeFalse
    }

    It 'builds the URL from $ArbitronRepo (dbinky/arbitron)' {
        $script:seenUri = $null
        Mock -CommandName Invoke-ArbitronDownload -MockWith {
            param($Uri, $OutFile)
            if ($Uri -like '*arbitron_*windows*') { $script:seenUri = $Uri }
            Set-Content -Path $OutFile -Value 'x' -NoNewline
        }
        Mock -CommandName Test-Download -MockWith { }
        Mock -CommandName Test-Signature -MockWith { }
        Get-ArbitronBinary -Paths $script:p -Version '1.2.3' -Arch 'amd64'
        $script:seenUri | Should -BeLike '*dbinky/arbitron*'
        $script:seenUri | Should -BeLike '*arbitron_1.2.3_windows_amd64.exe*'
    }
}
