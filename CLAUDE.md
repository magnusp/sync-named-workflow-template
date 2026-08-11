# Project conventions

## Commit messages

This repo uses [Conventional Commits](https://www.conventionalcommits.org/) for all commit messages.

- Format: `<type>[optional scope]: <description>`
- Common types: `feat`, `fix`, `docs`, `style`, `refactor`, `perf`, `test`, `build`, `ci`, `chore`, `revert`
- Breaking changes: append `!` after the type/scope (e.g., `feat!:`) or include a `BREAKING CHANGE:` footer
- GoReleaser's changelog grouping depends on this format. PRs are checked automatically by the `Commitlint` workflow, which runs [siderolabs/conform](https://github.com/siderolabs/conform) against every commit on the PR (policy in `.conform.yaml`), and this check is required before merging.
- The same check runs locally as a `commit-msg` git hook (via `pre-commit`, see `.pre-commit-config.yaml`), installed automatically by `mise install`.
