# Changelog

All notable changes to dabs are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.2.0] - 2026-07-06

### Added
- **`docker` sandbox driver** — run boxes as plain docker containers, selectable
  from a manifest with `"target": "docker"`. Unprivileged by default.
- **`INTERNAL-docker-privileged-for-nested-sandboxing` driver** — the docker
  driver's privileged variant (`--privileged` + a non-overlay `/tmp` volume), for
  running a *nested* dabs sandbox (bwrap) inside a docker box. Internal/opt-in.
- **`dabs install [pi|claude]` and `dabs uninstall <harness>`** — install or
  remove the dabash harness integrations (a Claude skill, a pi extension). The
  integration files are embedded in the binary (`//go:embed`), so install works
  from a downloaded release, not only a source checkout.
- **`DABS_NAME` in every box** — dabs now sets `DABS_NAME=<instance>` in the box
  environment across drivers, so a program can detect it is sandboxed (the dabash
  guard keys on this).
- **Driver-agnostic e2e CLI test suite** (`test/e2e`, behind `//go:build e2e`)
  and `run_e2e.sh`, which drive the real `dabs` CLI inside a dabs box.

### Changed
- **The bwrap driver no longer requires docker to run prebuilt images.** docker
  is now checked only in `Build` (image building); `up`/`run`/`down`/`ls` need
  only `bwrap`. A host that only runs prebuilt images needs no docker.

## [0.1.0] - 2026-07-02

Initial release. Minimum to bootstrap dabs.

### Added
- Core CLI: `build`, `up`, `run`, `down`, `ls`, `mcp`, `servers`.
- Drivers: `apple` (Apple `container` micro-VMs, macOS), `bwrap`
  (bubblewrap + overlay, Linux), and `ssh` servers.
- `dabs.json` manifest (`name`, `workdir`, `env`, `dockerfile`, `context`,
  `target`) + Dockerfile-based images.
- `dabash` MCP tool, curried to a single instance via `dabs mcp <instance>`.

[0.2.0]: https://github.com/jjmerino/dabs/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/jjmerino/dabs/releases/tag/v0.1.0
