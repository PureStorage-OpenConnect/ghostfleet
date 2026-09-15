# Security Policy

## Supported versions

GhostFleet is maintained on a best-effort, community-supported basis. Security
fixes are applied to the latest released version.

## Reporting a vulnerability

Please report suspected security vulnerabilities **privately** rather than in a
public issue. Use GitHub's private vulnerability reporting — the **"Report a
vulnerability"** button under the repository's
[Security tab](https://github.com/PureStorage-OpenConnect/ghostfleet/security/advisories/new).

Include a description of the issue, steps to reproduce, and the affected version.
You'll get an acknowledgement as time permits; there is no guaranteed response
time (see the Support section of the [README](README.md)).

## Scope and deployment notes

GhostFleet is a lab/test tool designed to run on a **trusted management network**.
The following are deliberate design choices for that environment, not
vulnerabilities:

- The controller serves plain **HTTP** by design. Terminate TLS at a reverse
  proxy (nginx / Caddy / Traefik) if you expose it beyond a trusted network.
- The isolated boot network is IPv6-only with static ULA addressing and is meant
  to be a dedicated, non-routed segment.
- API authentication is optional: protect the controller with
  `GHOSTFLEET_PASSWORD` and/or `GHOSTFLEET_API_KEYS`. With neither set, all API
  endpoints are open.

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for the full network and
component design.
