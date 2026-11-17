# Contributing to Arbitron

Thanks for your interest. Arbitron is maintained by one person, so the process
is deliberately light.

## Before you start

- **Bugs:** open an issue with what you ran, what you expected and what
  happened. Include the Arbitron version (`arbitron version`), your OS, and the
  relevant trace or log lines, with any credentials removed.
- **Features or larger changes:** open an issue first to discuss the idea.
  Unrequested large pull requests are hard to review and may be declined.
- **Security issues:** do not open a public issue; see [SECURITY.md](SECURITY.md).

## Making a change

1. Read [AGENTS.md](AGENTS.md). It explains the layout, the hexagonal
   architecture rules (no adapter imports in `domain/`) and the conventions the
   codebase follows. It is written for both people and coding agents.
2. Work test-first. Add tests next to the package you change, covering the
   happy path, failures and edge cases.
3. Run the gates before opening a pull request:

   ```bash
   gofmt -l .                    # must print nothing
   go test ./...
   cd dashboard && npx tsc --noEmit && npx vitest run   # if you touched the dashboard
   make test-all                 # the full CI gate
   ```

   If you change dashboard source, also run `make ui` so the embedded build in
   `adapter/dashboard/dist` stays in sync.
4. Keep pull requests focused, and explain the why in the description.

Bundled workflows under `samples/` are validated by tests. If you change a
sample's model profiles, update its `PROFILES.md`.

## Licensing

By contributing, you agree that your contributions are licensed under the
[Apache License, Version 2.0](LICENSE), the same license as the project.
