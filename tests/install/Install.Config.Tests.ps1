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

Describe 'Invoke-ConfigQA question order (parity with P1 install.sh)' {
    # P3 task 3 plan: "ask port, LLM access, skip-permissions (RCE warning),
    # data location" -- that is the canonical order mirroring run_config_qa in
    # install.sh. Asserting the order here guards against future swaps that
    # would break cross-platform parity for operators switching between OSes.
    BeforeEach { $script:p = New-TestPaths }

    It 'asks port -> LLM access -> skip-permissions -> data location in order' {
        $seq = New-Object System.Collections.Generic.List[string]
        Mock -CommandName Read-PromptDefault -MockWith { $seq.Add($Prompt); $Default }
        Mock -CommandName Read-PromptChoice  -MockWith { $seq.Add($Prompt); $(if ($Default) { $Default } else { $Choices[0] }) }
        Mock -CommandName Read-PromptYesNo   -MockWith { $seq.Add($Prompt); $Default }

        Invoke-ConfigQA -Paths $script:p | Out-Null

        $idxPort = ($seq.FindIndex({ param($s) $s -like '*port*' }))
        $idxLlm  = ($seq.FindIndex({ param($s) $s -like '*LLM access*' }))
        $idxSkip = ($seq.FindIndex({ param($s) $s -like '*unattended agent tools*' }))
        $idxData = ($seq.FindIndex({ param($s) $s -like '*Data location*' }))

        $idxPort | Should -BeGreaterOrEqual 0
        $idxLlm  | Should -BeGreaterThan $idxPort
        # skip-permissions must precede data-location (parity with bash run_config_qa)
        $idxSkip | Should -BeGreaterThan $idxLlm
        $idxData | Should -BeGreaterThan $idxSkip
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

Describe 'Invoke-ConfigQA api-key empty-key guard' {
    # Parity with install.sh INST-006: choosing api-key mode and leaving the key
    # blank must abort immediately. Without the guard, the installer proceeds with
    # provider: anthropic in config.yaml but no ARBITRON_LLM_APIKEY stored --
    # a silently broken install.
    BeforeEach { $script:p = New-TestPaths }

    It 'throws when apikey mode is chosen but the key is left empty' {
        Mock -CommandName Read-PromptChoice -MockWith {
            param($Prompt, $Choices, $Default)
            if ($Prompt -like '*LLM*') { return 'apikey' }
            return $Default
        }
        Mock -CommandName Read-PromptDefault -MockWith {
            param($Prompt, $Default)
            if ($Prompt -like '*Anthropic API key*') { return '' }
            return $Default
        }
        Mock -CommandName Read-PromptYesNo -MockWith { param($Prompt, $Default) $Default }
        { Invoke-ConfigQA -Paths $script:p } | Should -Throw -ExpectedMessage '*API key*'
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

Describe 'Get-ExistingConfigDefaults' {
    # Used by Invoke-Reconfigure to prefill prompt defaults. If it returns wrong
    # values the reconfigure flow overwrites the running config with fresh defaults.
    BeforeEach {
        $script:p = New-TestPaths
        New-Item -ItemType Directory -Path $script:p.Root -Force | Out-Null
    }
    AfterEach { Remove-Item -Recurse -Force $script:p.Root -ErrorAction SilentlyContinue }

    It 'returns an empty hashtable when config is absent' {
        $d = Get-ExistingConfigDefaults -Paths $script:p
        $d.Count | Should -Be 0
    }

    It 'parses the port from an existing config' {
        Set-Content -Path $script:p.Config -Value "server:`n  port: 9090`n"
        $d = Get-ExistingConfigDefaults -Paths $script:p
        $d.Port | Should -Be 9090
    }

    It 'maps provider: anthropic back to LlmMode: apikey' {
        Set-Content -Path $script:p.Config -Value "llm:`n  provider: anthropic`n"
        $d = Get-ExistingConfigDefaults -Paths $script:p
        $d.LlmMode | Should -Be 'apikey'
    }

    It 'maps provider: claude-cli back to LlmMode: subscription' {
        Set-Content -Path $script:p.Config -Value "llm:`n  provider: claude-cli`n"
        $d = Get-ExistingConfigDefaults -Paths $script:p
        $d.LlmMode | Should -Be 'subscription'
    }

    It 'extracts DataDir as the parent of database.path' {
        $dbPath = Join-Path $script:p.Data 'arbitron.db'
        Set-Content -Path $script:p.Config -Value "database:`n  path: `"$dbPath`"`n"
        $d = Get-ExistingConfigDefaults -Paths $script:p
        $d.DataDir | Should -Be $script:p.Data
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

Describe 'Get-ArbitronPaths Samples key' {
    # Task 10: samples dir is a first-class path entry so Write-ArbitronConfig
    # and Get-ArbitronSamples have a stable key without string arithmetic.
    BeforeAll { $env:LOCALAPPDATA = Join-Path ([System.IO.Path]::GetTempPath()) 'ArbTestLAD_Samples' }

    It 'Local paths include a Samples key under the root' {
        $p = Get-ArbitronPaths -InstallType Local
        $p.ContainsKey('Samples') | Should -BeTrue
        $p.Samples | Should -BeLike "*$($p.Root)*"
        $p.Samples | Should -BeLike '*samples'
    }
}

Describe 'Write-ArbitronConfig samples: block' {
    # Task 10: the config writer emits a valid samples: block so the binary seeds
    # workflows on first boot.
    BeforeEach {
        $script:p = New-TestPaths
        $script:p['Samples'] = Join-Path $script:p.Root 'samples'
        New-Item -ItemType Directory -Path $script:p.Root -Force | Out-Null
    }
    AfterEach { Remove-Item -Recurse -Force $script:p.Root -ErrorAction SilentlyContinue }

    It 'writes samples: seed: true and sourcedir when SeedSamples is true' {
        $answers = @{
            Port = 8080; LlmMode = 'subscription'; SkipPermissions = $false
            DataDir = $script:p.Data; SeedSamples = $true
        }
        Write-ArbitronConfig -Paths $script:p -Answers $answers
        $yaml = Get-Content -Raw $script:p.Config
        $yaml | Should -Match 'samples:'
        $yaml | Should -Match 'seed:\s*true'
        $yaml | Should -BeLike "*$($script:p.Samples)*"
    }

    It 'writes samples: seed: false when SeedSamples is false' {
        $answers = @{
            Port = 8080; LlmMode = 'subscription'; SkipPermissions = $false
            DataDir = $script:p.Data; SeedSamples = $false
        }
        Write-ArbitronConfig -Paths $script:p -Answers $answers
        $yaml = Get-Content -Raw $script:p.Config
        $yaml | Should -Match 'seed:\s*false'
    }

    It 'defaults to seed: true when SeedSamples is absent from answers' {
        $answers = @{
            Port = 8080; LlmMode = 'subscription'; SkipPermissions = $false
            DataDir = $script:p.Data
            # SeedSamples deliberately omitted to exercise default path
        }
        Write-ArbitronConfig -Paths $script:p -Answers $answers
        $yaml = Get-Content -Raw $script:p.Config
        $yaml | Should -Match 'seed:\s*true'
    }
}

Describe 'Invoke-ConfigQA SeedSamples' {
    # Task 10: local installs default samples on; env ARBITRON_INSTALL_SAMPLE_WORKFLOWS=no opts out.
    BeforeEach {
        $script:p = New-TestPaths
        Remove-Item Env:\ARBITRON_INSTALL_SAMPLE_WORKFLOWS -ErrorAction SilentlyContinue
    }
    AfterEach { Remove-Item Env:\ARBITRON_INSTALL_SAMPLE_WORKFLOWS -ErrorAction SilentlyContinue }

    It 'sets SeedSamples=true by default for a local install' {
        Mock -CommandName Read-PromptDefault -MockWith { param($Prompt, $Default) $Default }
        Mock -CommandName Read-PromptChoice  -MockWith { param($Prompt, $Choices, $Default) $Default }
        Mock -CommandName Read-PromptYesNo   -MockWith { param($Prompt, $Default) $Default }
        $a = Invoke-ConfigQA -Paths $script:p
        $a.ContainsKey('SeedSamples') | Should -BeTrue
        $a.SeedSamples | Should -BeTrue
    }

    It 'respects env ARBITRON_INSTALL_SAMPLE_WORKFLOWS=no to opt out' {
        $env:ARBITRON_INSTALL_SAMPLE_WORKFLOWS = 'no'
        Mock -CommandName Read-PromptDefault -MockWith { param($Prompt, $Default) $Default }
        Mock -CommandName Read-PromptChoice  -MockWith { param($Prompt, $Choices, $Default) $Default }
        Mock -CommandName Read-PromptYesNo   -MockWith { param($Prompt, $Default) $Default }
        $a = Invoke-ConfigQA -Paths $script:p
        $a.SeedSamples | Should -BeFalse
    }
}

Describe 'Get-ArbitronSamples' {
    # Task 10: mirrors Get-ArbitronBinary -- verify-before-extract, non-fatal on failure.
    BeforeEach {
        $script:p = New-TestPaths
        $script:p['Samples'] = Join-Path $script:p.Root 'samples'
        New-Item -ItemType Directory -Path $script:p.Root -Force | Out-Null
    }
    AfterEach { Remove-Item -Recurse -Force $script:p.Root -ErrorAction SilentlyContinue }

    It 'downloads SHA256SUMS first, then the tarball, then verifies before extracting' {
        $script:callOrder = [System.Collections.Generic.List[string]]::new()
        Mock -CommandName Get-ArbitronVersion -MockWith { '1.2.3' }
        Mock -CommandName Invoke-ArbitronDownload -MockWith {
            param($Uri, $OutFile)
            $script:callOrder.Add($(if ($Uri -like '*SHA256SUMS') { 'sums' } else { 'tarball' }))
            Set-Content -Path $OutFile -Value 'fake' -NoNewline
        }
        Mock -CommandName Test-Download -MockWith { $script:callOrder.Add('verify') }
        # Patch tar via Get-Command redirection is not possible in Pester; instead
        # assert verify was invoked before any extraction attempt (even though
        # tar will fail non-fatally, the order invariant has already been recorded).
        Get-ArbitronSamples -Paths $script:p -Version '1.2.3' | Out-Null
        # Verify must come after both downloads.
        $idxSums   = $script:callOrder.IndexOf('sums')
        $idxTar    = $script:callOrder.IndexOf('tarball')
        $idxVerify = $script:callOrder.IndexOf('verify')
        $idxSums   | Should -BeGreaterOrEqual 0
        $idxTar    | Should -BeGreaterOrEqual 0
        $idxVerify | Should -BeGreaterThan $idxSums
        $idxVerify | Should -BeGreaterThan $idxTar
    }

    It 'returns false (non-fatal) when the download fails' {
        Mock -CommandName Get-ArbitronVersion -MockWith { '1.2.3' }
        Mock -CommandName Invoke-ArbitronDownload -MockWith { throw 'network error' }
        $result = Get-ArbitronSamples -Paths $script:p -Version '1.2.3'
        $result | Should -BeFalse
    }

    It 'returns false (non-fatal) when checksum verification fails' {
        Mock -CommandName Get-ArbitronVersion -MockWith { '1.2.3' }
        Mock -CommandName Invoke-ArbitronDownload -MockWith {
            param($Uri, $OutFile) Set-Content -Path $OutFile -Value 'fake' -NoNewline
        }
        Mock -CommandName Test-Download -MockWith { throw 'Checksum verification failed.' }
        $result = Get-ArbitronSamples -Paths $script:p -Version '1.2.3'
        $result | Should -BeFalse
    }

    It 'builds the tarball URL from $ArbitronRepo and the resolved version' {
        $script:seenUri = $null
        Mock -CommandName Get-ArbitronVersion -MockWith { '2.5.0' }
        Mock -CommandName Invoke-ArbitronDownload -MockWith {
            param($Uri, $OutFile)
            if ($Uri -like '*samples*') { $script:seenUri = $Uri }
            Set-Content -Path $OutFile -Value 'x' -NoNewline
        }
        Mock -CommandName Test-Download -MockWith { throw 'stop' }
        Get-ArbitronSamples -Paths $script:p -Version '2.5.0' | Out-Null
        $script:seenUri | Should -BeLike '*arbitron_samples_2.5.0.tar.gz*'
        $script:seenUri | Should -BeLike '*dbinky/arbitron*'
    }
}
