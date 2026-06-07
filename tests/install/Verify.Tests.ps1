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
    AfterEach { Remove-Item Env:\ARBITRON_VERSION -ErrorAction SilentlyContinue }

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
}

Describe 'No checksum bypass' {
    It 'install.ps1 has no skip-verify / insecure / no-verify checksum bypass' {
        $content = Get-Content -Raw $script:ScriptPath
        $content | Should -Not -Match '(?i)skip[-_ ]*verif'
        $content | Should -Not -Match '(?i)no[-_]verify'
    }
}
