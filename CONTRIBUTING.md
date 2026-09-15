# Contributing to GhostFleet

Thanks for your interest in improving GhostFleet! Bug reports, feature requests
and pull requests are all welcome. The project is maintained on a best-effort,
community-supported basis by the maintainers listed in the
[README](README.md#maintainers), so please be patient with triage.

## Reporting bugs and requesting features

Open a [GitHub issue](https://github.com/PureStorage-OpenConnect/ghostfleet/issues). For a bug,
include enough to reproduce it: GhostFleet version, hypervisor and version, the
relevant profile/deployment settings, and controller/agent logs.

For anything security-sensitive, follow [SECURITY.md](SECURITY.md) instead of
opening a public issue.

## Pull requests

1. Fork the repository and create a topic branch.
2. Keep changes focused, and add or update tests where it makes sense.
3. Make sure the build and tests pass:
   ```sh
   make build
   make test
   ```
   CI runs `go vet ./...`, `go test ./...`, `go build ./...` and the web build.
4. Open a pull request describing what changed and why.

## Licensing of contributions

GhostFleet is licensed under the [Apache License 2.0](LICENSE). By submitting a
contribution you agree that it is provided under that same license — the standard
"inbound = outbound" model. This follows automatically from Section 5 of the
Apache-2.0 license and from GitHub's Terms of Service for public repositories, so
**no separate Contributor License Agreement (CLA) is required.**

Please only submit work that is yours to contribute, or that you otherwise have
the right to submit under this license.

## Coding notes

- The controller and agent are written in Go; the web UI is Vue 3 + TypeScript
  under `web/`.
- Run `gofmt`/`go vet` before pushing.
- See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for how the controller,
  netboot stack, temp OS and agent fit together, and
  [docs/TECH-STACK.md](docs/TECH-STACK.md) for the technology choices.
