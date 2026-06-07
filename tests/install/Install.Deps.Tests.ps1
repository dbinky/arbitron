# Pester suite for install.ps1 dependency resolution (P3 Task 2).
#
# Run: Invoke-Pester ./tests/install

BeforeAll {
    $script:ScriptPath = (Resolve-Path (Join-Path $PSScriptRoot '..' '..' 'install.ps1')).Path
    $env:ARBITRON_INSTALL_NOEXEC = '1'
    . $script:ScriptPath
    $script:Paths = @{
        Bin       = Join-Path ([System.IO.Path]::GetTempPath()) 'ArbDeps\bin'
        NpmPrefix = Join-Path ([System.IO.Path]::GetTempPath()) 'ArbDeps\npm'
        Runtime   = Join-Path ([System.IO.Path]::GetTempPath()) 'ArbDeps\runtime'
    }
}

AfterAll {
    Remove-Item Env:\ARBITRON_INSTALL_NOEXEC -ErrorAction SilentlyContinue
}

Describe 'Get-DependencySpec' {
    It 'maps package ids: node->OpenJS.NodeJS, git->Git.Git, gh->GitHub.cli' {
        $specs = Get-DependencySpec
        ($specs | Where-Object Name -eq 'node').WingetId | Should -Be 'OpenJS.NodeJS'
        ($specs | Where-Object Name -eq 'git').WingetId  | Should -Be 'Git.Git'
        ($specs | Where-Object Name -eq 'gh').WingetId   | Should -Be 'GitHub.cli'
    }

    It 'marks opencode as optional and others required' {
        $specs = Get-DependencySpec
        ($specs | Where-Object Name -eq 'opencode').Optional | Should -BeTrue
        ($specs | Where-Object Name -eq 'node').Optional | Should -BeFalse
    }
}

Describe 'Test-DependencyPresent' {
    It 'returns true when command present and version >= min' {
        Mock -CommandName Get-Command -MockWith { [pscustomobject]@{ Name = 'node' } } `
            -ParameterFilter { $Name -eq 'node' }
        Mock -CommandName Get-DependencyVersion -MockWith { [version]'20.0.0' }
        $spec = [pscustomobject]@{ Name = 'node'; Command = 'node'; MinVersion = '18.0.0' }
        Test-DependencyPresent -Spec $spec | Should -BeTrue
    }

    It 'returns false when version below min' {
        Mock -CommandName Get-Command -MockWith { [pscustomobject]@{ Name = 'node' } } `
            -ParameterFilter { $Name -eq 'node' }
        Mock -CommandName Get-DependencyVersion -MockWith { [version]'16.0.0' }
        $spec = [pscustomobject]@{ Name = 'node'; Command = 'node'; MinVersion = '18.0.0' }
        Test-DependencyPresent -Spec $spec | Should -BeFalse
    }

    It 'returns false when command absent' {
        Mock -CommandName Get-Command -MockWith { $null } -ParameterFilter { $Name -eq 'node' }
        $spec = [pscustomobject]@{ Name = 'node'; Command = 'node'; MinVersion = '18.0.0' }
        Test-DependencyPresent -Spec $spec | Should -BeFalse
    }
}

Describe 'Resolve-Dependency presence short-circuit' {
    It 'does nothing (no winget) when already present' {
        Mock -CommandName Test-DependencyPresent -MockWith { $true }
        Mock -CommandName Install-ViaWinget -MockWith { }
        Resolve-Dependency -Name node -Paths $script:Paths | Out-Null
        Should -Invoke Install-ViaWinget -Times 0 -Exactly
    }

    It 'proceeds to install when too old' {
        Mock -CommandName Test-DependencyPresent -MockWith { $false }
        Mock -CommandName Test-WingetPresent -MockWith { $true }
        Mock -CommandName Install-ViaWinget -MockWith { }
        Resolve-Dependency -Name node -Paths $script:Paths | Out-Null
        Should -Invoke Install-ViaWinget -Times 1 -Exactly
    }
}

Describe 'Install-ViaWinget' {
    It 'invokes winget with correct id and silent/agreement flags' {
        $script:captured = $null
        Mock -CommandName Invoke-Winget -MockWith { $script:captured = $Arguments; 0 }
        $spec = [pscustomobject]@{ Name = 'node'; WingetId = 'OpenJS.NodeJS' }
        Install-ViaWinget -Spec $spec
        $joined = ($script:captured -join ' ')
        $joined | Should -BeLike '*install*'
        $joined | Should -BeLike '*OpenJS.NodeJS*'
        $joined | Should -BeLike '*--accept-source-agreements*'
        $joined | Should -BeLike '*--accept-package-agreements*'
    }
}

Describe 'Resolve-Dependency winget-absent ladder' {
    It 'offers App Installer bootstrap then retries winget on accept' {
        Mock -CommandName Test-DependencyPresent -MockWith { $false }
        $script:wingetState = $false
        Mock -CommandName Test-WingetPresent -MockWith { $script:wingetState }
        Mock -CommandName Install-AppInstaller -MockWith { $script:wingetState = $true; $true }
        Mock -CommandName Install-ViaWinget -MockWith { }
        Mock -CommandName Install-ViaDirectDownload -MockWith { }
        Resolve-Dependency -Name git -Paths $script:Paths | Out-Null
        Should -Invoke Install-AppInstaller -Times 1 -Exactly
        Should -Invoke Install-ViaWinget -Times 1 -Exactly
        Should -Invoke Install-ViaDirectDownload -Times 0 -Exactly
    }

    It 'falls back to direct download (checksum-gated) when bootstrap declined' {
        Mock -CommandName Test-DependencyPresent -MockWith { $false }
        Mock -CommandName Test-WingetPresent -MockWith { $false }
        Mock -CommandName Install-AppInstaller -MockWith { $false }
        Mock -CommandName Install-ViaWinget -MockWith { }
        Mock -CommandName Install-ViaDirectDownload -MockWith { }
        Resolve-Dependency -Name git -Paths $script:Paths | Out-Null
        Should -Invoke Install-ViaWinget -Times 0 -Exactly
        Should -Invoke Install-ViaDirectDownload -Times 1 -Exactly
    }
}

Describe 'Install-ViaDirectDownload checksum gate' {
    It 'aborts (throws) and does not place the artifact on checksum failure' {
        Mock -CommandName Invoke-ArbitronDownload -MockWith { }
        Mock -CommandName Test-Sha256 -MockWith { $false }
        Mock -CommandName Expand-Archive -MockWith { }
        Mock -CommandName Move-Item -MockWith { }
        $spec = [pscustomobject]@{
            Name = 'git'; DownloadUrl = 'https://example/git.zip'; DownloadSha256 = ('a' * 64)
        }
        { Install-ViaDirectDownload -Spec $spec -Prefix $script:Paths.Runtime } |
            Should -Throw -ExpectedMessage '*checksum*'
        Should -Invoke Expand-Archive -Times 0 -Exactly
        Should -Invoke Move-Item -Times 0 -Exactly
    }

    It 'downloads then verifies before placing (order)' {
        $script:order = @()
        Mock -CommandName Invoke-ArbitronDownload -MockWith { $script:order += 'download' }
        Mock -CommandName Test-Sha256 -MockWith { $script:order += 'verify'; $true }
        Mock -CommandName Expand-Archive -MockWith { $script:order += 'extract' }
        Mock -CommandName Move-Item -MockWith { }
        $spec = [pscustomobject]@{
            Name = 'git'; DownloadUrl = 'https://example/git.zip'; DownloadSha256 = ('a' * 64)
        }
        Install-ViaDirectDownload -Spec $spec -Prefix $script:Paths.Runtime | Out-Null
        $script:order[0] | Should -Be 'download'
        $script:order[1] | Should -Be 'verify'
    }
}

Describe 'Install-ClaudeCli' {
    It 'installs @anthropic-ai/claude-code via npm into the Arbitron-owned prefix' {
        Mock -CommandName Resolve-Dependency -MockWith { } -ParameterFilter { $Name -eq 'node' }
        $script:npmArgs = $null
        Mock -CommandName Invoke-Npm -MockWith { $script:npmArgs = ($Arguments -join ' '); 0 }
        Install-ClaudeCli -Paths $script:Paths
        $script:npmArgs | Should -BeLike '*@anthropic-ai/claude-code*'
        $script:npmArgs | Should -BeLike "*$($script:Paths.NpmPrefix)*"
        # Must NOT target a system prefix.
        $script:npmArgs | Should -Not -BeLike '*Program Files*'
    }

    It 'ensures node is resolved before the npm -g call' {
        $script:seq = @()
        Mock -CommandName Resolve-Dependency -MockWith { $script:seq += 'node' } `
            -ParameterFilter { $Name -eq 'node' }
        Mock -CommandName Invoke-Npm -MockWith { $script:seq += 'npm'; 0 }
        Install-ClaudeCli -Paths $script:Paths
        $script:seq[0] | Should -Be 'node'
    }

    It 'prepends the npm prefix bin to the process PATH' {
        Mock -CommandName Resolve-Dependency -MockWith { } -ParameterFilter { $Name -eq 'node' }
        Mock -CommandName Invoke-Npm -MockWith { 0 }
        $orig = $env:PATH
        try {
            Install-ClaudeCli -Paths $script:Paths
            $env:PATH | Should -BeLike "*$($script:Paths.NpmPrefix)*"
        } finally {
            $env:PATH = $orig
        }
    }
}

Describe 'Resolve-Dependency opencode optional' {
    It 'logs optional and continues (no throw) when opencode missing' {
        Mock -CommandName Test-DependencyPresent -MockWith { $false }
        Mock -CommandName Test-WingetPresent -MockWith { $false }
        Mock -CommandName Install-AppInstaller -MockWith { $false }
        Mock -CommandName Install-ViaDirectDownload -MockWith { throw 'should not download optional' }
        $script:result = $null
        { $script:result = Resolve-Dependency -Name opencode -Paths $script:Paths } | Should -Not -Throw
        $script:result.Status | Should -Be 'skipped'
    }
}

Describe 'Resolve-AllDependencies' {
    It 'returns a summary across all deps' {
        Mock -CommandName Resolve-Dependency -MockWith {
            param($Name, $Paths)
            [pscustomobject]@{ Name = $Name; Status = 'resolved' }
        }
        $summary = Resolve-AllDependencies -Paths $script:Paths
        $summary.Count | Should -BeGreaterThan 3
    }
}

Describe 'Resolve-Dependency ViaNpm routing (claude)' {
    # claude has ViaNpm = $true and WingetId = $null. Resolve-Dependency must
    # route it to Install-ClaudeCli -- never through the winget ladder.
    # If this seam breaks, claude falls through to Install-ViaWinget with a null
    # WingetId and the install silently fails or errors in an uncontrolled way.
    BeforeEach {
        Mock -CommandName Test-DependencyPresent -MockWith { $false }
        Mock -CommandName Install-ClaudeCli -MockWith { }
        Mock -CommandName Install-ViaWinget -MockWith { }
        Mock -CommandName Test-WingetPresent -MockWith { $false }
        Mock -CommandName Install-AppInstaller -MockWith { $false }
    }

    It 'routes claude to Install-ClaudeCli (not winget) when missing' {
        Resolve-Dependency -Name claude -Paths $script:Paths | Out-Null
        Should -Invoke Install-ClaudeCli -Times 1 -Exactly
        Should -Invoke Install-ViaWinget -Times 0 -Exactly
    }

    It 'returns resolved status after Install-ClaudeCli' {
        $result = Resolve-Dependency -Name claude -Paths $script:Paths
        $result.Status | Should -Be 'resolved'
    }
}

Describe 'Install-ViaWinget failure path' {
    It 'throws with the exit code when winget returns non-zero' {
        Mock -CommandName Invoke-Winget -MockWith { 1603 }
        $spec = [pscustomobject]@{ Name = 'git'; WingetId = 'Git.Git' }
        { Install-ViaWinget -Spec $spec } | Should -Throw -ExpectedMessage '*1603*'
    }
}
