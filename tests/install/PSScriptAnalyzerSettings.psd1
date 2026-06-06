@{
    # Severities that fail the lint gate. The meta-tests assert zero
    # Error/Warning diagnostics across install.ps1 and the test suite.
    Severity = @('Error', 'Warning')

    # Rules excluded with justification. Each is in genuine conflict with the
    # installer's design, not a code smell to be papered over.
    ExcludeRules = @(
        # Write-Host is the deliberate user-facing console channel for an
        # interactive installer (colored status lines, prompts, summary). The
        # output is for a human at a terminal, never a pipeline consumer.
        'PSAvoidUsingWriteHost'

        # ConvertTo-SecureString -AsPlainText is required to wrap a freshly
        # generated/pasted secret before DPAPI protection (the documented
        # Windows secrets-at-rest mechanism). The plaintext exists only
        # transiently in-process and is never persisted unprotected.
        'PSAvoidUsingConvertToSecureStringWithPlainText'

        # The canonical helper names are fixed by the cross-phase contract in
        # the implementation plans (Get-ArbitronPaths, Resolve-AllDependencies,
        # New-MasterKey/New-RandomBytes); P4 reuses them verbatim. Renaming to
        # singular nouns would break that contract.
        'PSUseSingularNouns'

        # New-/Set-/Remove- helpers here are internal install steps, not
        # reusable cmdlets; ShouldProcess scaffolding (-WhatIf/-Confirm) would
        # add ceremony with no caller that uses it. The top-level installer is
        # the confirmation surface.
        'PSUseShouldProcessForStateChangingFunctions'

        # The Windows-cmdlet shims declare the parameter contracts of the real
        # cmdlets (so Pester mocks bind identically on/off Windows) and the
        # install-type seam params are kept for the P4 Shared branch; both are
        # intentionally "unused" in the local-only P3 bodies.
        'PSReviewUnusedParameter'
    )
}
