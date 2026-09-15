# Third-Party Notices

GhostFleet is distributed under the Apache License 2.0 (see [LICENSE](LICENSE)).
It builds on, and its published container images bundle, third-party open-source
software that remains under its own license. This file acknowledges those
components; nothing here modifies their terms.

Licenses below are listed to the best of our knowledge — consult each project for
authoritative and complete license text. The exact, versioned dependency sets are
recorded in `go.mod` / `go.sum` (Go) and `web/package.json` /
`web/package-lock.json` (web UI).

## Go modules (compiled into the controller and agent binaries)

| Component | Purpose | License |
|---|---|---|
| [govmomi](https://github.com/vmware/govmomi) | vSphere API client | Apache-2.0 |
| [modernc.org/sqlite](https://gitlab.com/cznic/sqlite) | Pure-Go SQLite | BSD-3-Clause |
| [modernc.org/libc](https://gitlab.com/cznic/libc), [mathutil](https://gitlab.com/cznic/mathutil), [memory](https://gitlab.com/cznic/memory) | SQLite runtime support | BSD-3-Clause |
| [github.com/google/uuid](https://github.com/google/uuid) | UUID generation | BSD-3-Clause |
| [github.com/dustin/go-humanize](https://github.com/dustin/go-humanize) | Human-readable sizes | MIT |
| [github.com/mattn/go-isatty](https://github.com/mattn/go-isatty) | TTY detection | MIT |
| [github.com/ncruces/go-strftime](https://github.com/ncruces/go-strftime) | strftime formatting | MIT |
| [github.com/remyoudompheng/bigfft](https://github.com/remyoudompheng/bigfft) | Big-integer FFT | BSD-3-Clause |
| [golang.org/x/sys](https://pkg.go.dev/golang.org/x/sys) | Low-level OS APIs | BSD-3-Clause |

## Bundled in the published container images

These ship as separate programs inside the images and are invoked as processes;
they are **not** linked into the GhostFleet binaries.

### netboot image

| Component | Purpose | License |
|---|---|---|
| [dnsmasq](https://thekelleys.org.uk/dnsmasq/doc.html) | DHCPv6 / RA / TFTP | GPL-2.0 or GPL-3.0 |
| [iPXE](https://ipxe.org/) (pinned commit) | Network bootloader | GPL-2.0 (with additional permissions) |

### temp-OS image (in-memory, served to booted VMs)

| Component | Purpose | License |
|---|---|---|
| [Alpine Linux](https://alpinelinux.org/) (`linux-virt` kernel + base) | Minimal boot OS | GPL-2.0 (kernel) and others |
| [BusyBox](https://busybox.net/) | Core userland | GPL-2.0 |
| [fio](https://github.com/axboe/fio) | Write engine | GPL-2.0 |
| [xfsprogs](https://xfs.wiki.kernel.org/) | XFS tooling | GPL-2.0 / LGPL-2.1 |

## Web UI (build-time only, `web/`)

The compiled single-page app is served as static assets by the controller; these
tools run at build time and are not shipped as-is at runtime.

| Component | License |
|---|---|
| [Vue 3](https://vuejs.org/) | MIT |
| [Vite](https://vitejs.dev/) | MIT |
| [TypeScript](https://www.typescriptlang.org/) | Apache-2.0 |
| [Tailwind CSS](https://tailwindcss.com/) | MIT |

---

If you believe an entry here is inaccurate or incomplete, please open an issue.
