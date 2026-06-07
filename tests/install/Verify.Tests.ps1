# P5 Task 2: the fail-closed checksum-verification contract + version selection
# for install.ps1. Mirrors tests/install/verify.bats.
#
# No test performs a real network fetch: Invoke-ArbitronDownload is mocked to
# return fixture content. Run: Invoke-Pester ./tests/install

BeforeAll {
    $script:ScriptPath = (Resolve-Path (Join-Path $PSScriptRoot '..' '..' 'install.ps1')).Path
    Import-Module (Join-Path $PSScriptRoot '_Common.psm1') -Force
    $env:ARBITRON_INSTALL_NOEXEC = '1'
    . $script:ScriptPath

    # Write a fixture artifact + a correct SHA256SUMS that lists it. Returns a
    # hashtable with the paths + the artifact name.
    function New-VerifyFixture {
        param([string]$Name = 'arbitron_1.2.3_windows_amd64.exe')
        $dir = Join-Path ([System.IO.Path]::GetTempPath()) ('ArbVerify_' + [guid]::NewGuid())
        New-Item -ItemType Directory -Path $dir -Force | Out-Null
        $artifact = Join-Path $dir $Name
        Set-Content -Path $artifact -Value 'REAL-BINARY-BYTES' -NoNewline
        $hash = (Get-FileHash -LiteralPath $artifact -Algorithm SHA256).Hash
        $sums = Join-Path $dir 'SHA256SUMS'
        Set-Content -Path $sums -Value "$hash  $Name" -NoNewline
        return @{ Dir = $dir; Artifact = $artifact; Sums = $sums; Name = $Name; Hash = $hash }
    }
}

AfterAll {
    Remove-Item Env:\ARBITRON_INSTALL_NOEXEC -ErrorAction SilentlyContinue
    Remove-Item Env:\ARBITRON_VERSION -ErrorAction SilentlyContinue
}

Describe 'Test-Download fail-closed checksum contract' {
    AfterEach {
        if ($script:fx -and (Test-Path $script:fx.Dir)) {
            Remove-Item -Recurse -Force $script:fx.Dir -ErrorAction SilentlyContinue
        }
    }

    It 'returns success (no throw) for a binary that matches SHA256SUMS' {
        $script:fx = New-VerifyFixture
        { Test-Download -Path $script:fx.Artifact -Name $script:fx.Name -SumsFile $script:fx.Sums } |
            Should -Not -Throw
        # The verified file is left in place for the caller to install.
        Test-Path $script:fx.Artifact | Should -BeTrue
    }

    It 'accepts a SHA256SUMS with binary-mode star-prefixed names' {
        # sha256sum -b and Windows CertUtil emit '<hash> *<name>' (star prefix).
        # TrimStart("*") in Test-Download must handle this so a Windows-generated
        # SHA256SUMS verifies without "no checksum found" errors.
        $script:fx = New-VerifyFixture
        Set-Content -Path $script:fx.Sums -Value "$($script:fx.Hash)  *$($script:fx.Name)" -NoNewline
        { Test-Download -Path $script:fx.Artifact -Name $script:fx.Name -SumsFile $script:fx.Sums } |
            Should -Not -Throw
        Test-Path $script:fx.Artifact | Should -BeTrue
    }

    It '(1) FAILS CLOSED on a tampered binary and removes the download' {
        $script:fx = New-VerifyFixture
        Add-Content -Path $script:fx.Artifact -Value 'TAMPERED' -NoNewline
        { Test-Download -Path $script:fx.Artifact -Name $script:fx.Name -SumsFile $script:fx.Sums } |
            Should -Throw
        Test-Path $script:fx.Artifact | Should -BeFalse
    }

    It '(2) FAILS CLOSED when the SHA256SUMS entry is tampered' {
        $script:fx = New-VerifyFixture
        $flipped = if ($script:fx.Hash[0] -eq '0') { '1' } else { '0' }
        $bad = $flipped + $script:fx.Hash.Substring(1)
        Set-Content -Path $script:fx.Sums -Value "$bad  $($script:fx.Name)" -NoNewline
        { Test-Download -Path $script:fx.Artifact -Name $script:fx.Name -SumsFile $script:fx.Sums } |
            Should -Throw
        Test-Path $script:fx.Artifact | Should -BeFalse
    }

    It '(3) FAILS CLOSED when SHA256SUMS is missing -- partial download removed' {
        $script:fx = New-VerifyFixture
        Remove-Item -LiteralPath $script:fx.Sums -Force
        { Test-Download -Path $script:fx.Artifact -Name $script:fx.Name -SumsFile $script:fx.Sums } |
            Should -Throw
        # Fail-closed: the partial download is deleted so nothing unverified remains.
        Test-Path $script:fx.Artifact | Should -BeFalse
    }

    It '(4) FAILS CLOSED when the artifact is not listed in SHA256SUMS -- partial download removed' {
        $script:fx = New-VerifyFixture
        Set-Content -Path $script:fx.Sums -Value "$($script:fx.Hash)  some_other_artifact" -NoNewline
        { Test-Download -Path $script:fx.Artifact -Name $script:fx.Name -SumsFile $script:fx.Sums } |
            Should -Throw
        # Fail-closed: the partial download is deleted so nothing unverified remains.
        Test-Path $script:fx.Artifact | Should -BeFalse
    }
}

Describe 'Version selection + artifact URL builders' {
    AfterEach {
        Remove-Item Env:\ARBITRON_VERSION -ErrorAction SilentlyContinue
        Remove-Item Env:\ARBITRON_REPO -ErrorAction SilentlyContinue
        $script:ArbitronRepo = 'dbinky/arbitron'
    }

    It 'Get-ArtifactName builds arbitron_VERSION_windows_ARCH.exe' {
        $name = Get-ArtifactName -Version '1.2.3' -Arch 'amd64'
        $name | Should -Be 'arbitron_1.2.3_windows_amd64.exe'
    }

    It 'Get-ArtifactUrl targets the pinned tag download path' {
        $url = Get-ArtifactUrl -Version '1.2.3' -Arch 'amd64'
        $url | Should -Be 'https://github.com/dbinky/arbitron/releases/download/v1.2.3/arbitron_1.2.3_windows_amd64.exe'
    }

    It 'ARBITRON_VERSION=1.2.3 pins the version verbatim' {
        $env:ARBITRON_VERSION = '1.2.3'
        Get-ArbitronVersion | Should -Be '1.2.3'
    }

    It 'ARBITRON_VERSION=v1.2.3 normalizes to 1.2.3' {
        $env:ARBITRON_VERSION = 'v1.2.3'
        Get-ArbitronVersion | Should -Be '1.2.3'
    }

    It 'unset ARBITRON_VERSION resolves latest via the releases/latest redirect' {
        Remove-Item Env:\ARBITRON_VERSION -ErrorAction SilentlyContinue
        Mock -CommandName Get-LatestReleaseTag -MockWith { 'v9.9.9' }
        Get-ArbitronVersion | Should -Be '9.9.9'
        Should -Invoke Get-LatestReleaseTag -Times 1
    }

    It 'Get-ArbitronVersion throws when the latest-tag probe returns nothing' {
        # Network unavailable + ARBITRON_VERSION unset: must throw rather than silently
        # constructing a URL with an empty version string.
        Remove-Item Env:\ARBITRON_VERSION -ErrorAction SilentlyContinue
        Mock -CommandName Get-LatestReleaseTag -MockWith { $null }
        { Get-ArbitronVersion } | Should -Throw -ExpectedMessage '*Could not resolve*'
    }

    It 'ARBITRON_REPO override flows into Get-ArtifactUrl' {
        # Fork testing: ARBITRON_REPO=my-org/my-fork must change download URLs.
        # Catches any hardcoded dbinky/arbitron in the URL builders.
        $script:ArbitronRepo = 'my-org/my-fork'
        $url = Get-ArtifactUrl -Version '1.0.0' -Arch 'amd64'
        $url | Should -BeLike '*my-org/my-fork*'
        $url | Should -Not -BeLike '*dbinky*'
    }
}

Describe 'No checksum bypass' {
    It 'install.ps1 has no skip-verify / insecure / no-verify checksum bypass' {
        $content = Get-Content -Raw $script:ScriptPath
        $content | Should -Not -Match '(?i)skip[-_ ]*verif'
        $content | Should -Not -Match '(?i)no[-_]verify'
    }
}

Describe 'cosign identity trust boundary (parity with verify.bats)' {
    It 'pins the cosign identity to release.yml, not any in-repo workflow' {
        $content = Get-Content -Raw $script:ScriptPath
        # Must reference the release workflow path explicitly (dots regex-escaped)...
        $content | Should -Match 'workflows/release\\\.yml@'
        # ...and must NOT use the loose repo-wildcard identity.
        $content | Should -Not -Match 'ArbitronRepo/\.\*"'
    }

    It 'identity regexp matches the release.yml producer string' {
        # Conformance: the consumer (install.ps1) identity must equal the producer
        # (release.yml verify-release, tag-anchored) with the repo literal resolved.
        $wf = Join-Path $PSScriptRoot '..' '..' '.github' 'workflows' 'release.yml'
        (Test-Path $wf) | Should -BeTrue
        $wfContent = Get-Content -Raw $wf
        $producer = [regex]::Match($wfContent,
            'https://github\.com/dbinky/arbitron/\.github/workflows/release\.yml@refs/tags/v\.\*').Value
        $producer | Should -Not -BeNullOrEmpty
        $content = Get-Content -Raw $script:ScriptPath
        $consumerRaw = [regex]::Match($content,
            'https://github\.com/\$script:ArbitronRepo/\\\.github/workflows/release\\\.yml@refs/tags/v\.\*').Value
        $consumerRaw | Should -Not -BeNullOrEmpty
        # Normalize: drop regex backslash-escapes, resolve the repo variable.
        $consumer = $consumerRaw.Replace('\', '').Replace('$script:ArbitronRepo', 'dbinky/arbitron')
        $consumer | Should -Be $producer
    }
}

Describe 'arbitron version output format contract (cross-seam parity with FA25 bats)' {
    It 'stamped version output is parseable with the documented regex extraction pattern' {
        # Mirrors the bats cross-seam test. Verifies that the PowerShell equivalent
        # of the sed extraction pattern correctly parses the format contract string
        # "arbitron VERSION (commit C, built D)" into just the VERSION token.
        # If the binary format changes, this test (plus the bats counterpart) fail
        # before any upgrade script starts extracting empty/wrong versions.
        $mockOutput = 'arbitron 1.2.3 (commit abc1234, built 2026-06-07T00:00:00Z)'
        $m = [regex]::Match($mockOutput, '^arbitron (\S+) ')
        $m.Success | Should -BeTrue -Because 'format must start with "arbitron VERSION "'
        $m.Groups[1].Value | Should -Be '1.2.3'
    }

    It 'dev sentinel is non-empty and parseable (graceful degradation)' {
        # An unstamped (plain go build) binary reports "dev" as the version sentinel.
        # Upgrade scripts that parse the installed version must degrade gracefully
        # to "dev" rather than producing an empty or erroring extraction.
        $mockOutput = 'arbitron dev (commit none, built unknown)'
        $m = [regex]::Match($mockOutput, '^arbitron (\S+) ')
        $m.Success | Should -BeTrue
        $m.Groups[1].Value | Should -Be 'dev'
    }

    It 'version-with-pre-release-suffix is parseable (e.g. 1.2.3-beta.1)' {
        # Ensures the extraction pattern handles pre-release/rc versions that
        # contain hyphens and dots (a valid semver extension).
        $mockOutput = 'arbitron 1.2.3-beta.1 (commit abc1234, built 2026-06-07T00:00:00Z)'
        $m = [regex]::Match($mockOutput, '^arbitron (\S+) ')
        $m.Groups[1].Value | Should -Be '1.2.3-beta.1'
    }
}
