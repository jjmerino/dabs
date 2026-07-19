# The scribe box: the dabseption image (privileged-nesting-capable, bwrap, tmux,
# a staged `shell` rootfs) plus tuti and a RELEASED dabs binary — the machine the
# tuti walkthrough suite (walkthroughs/) drives to photograph dabs's TUI for the manual.
#
# Two deliberate choices about what lands in the box:
#
#   - dabs is the LATEST RELEASED binary, fetched by the official installer
#     (https://dabs.dev/install.sh), which always serves releases/latest. The
#     goldens document what a user who just ran that installer actually sees —
#     not an unreleased build from this tree. To document a different release,
#     re-point `releases/latest` (publish that version) and rebuild.
#
#   - tuti is installed from its official source (git), the only way it ships —
#     it is experimental and not on PyPI.
#
# Build order: `dabs build dabseption` (readies the dabs-dabseption base image),
# then `dabs build scribe`.
FROM dabs-dabseption

# APT::Sandbox::User=root: apt's unprivileged `_apt` fetch user cannot read the
# downloaded lists on some Docker filesystems, and apt reports the failure as a
# bogus "invalid signature". Fetching as root sidesteps it; harmless elsewhere.
RUN apt-get -o APT::Sandbox::User=root update \
    && apt-get install -y --no-install-recommends \
      python3 python3-pip git curl ca-certificates unzip \
    && rm -rf /var/lib/apt/lists/*

# bun runs the egress proxy engine — the box needs it to drive any recipe whose
# `egress` opens a proxy chain (allow/deny gate, http_proxy modules, broker).
# `egress: none` needs no engine; everything past it does.
RUN curl -fsSL https://bun.sh/install | BUN_INSTALL=/usr/local bash \
    && bun --version

# tuti — screenshot testing for terminal programs — from its official repo (the
# install path its README documents; it is not published to PyPI). pytest rides
# along as the runner.
RUN pip3 install --break-system-packages \
      "git+https://github.com/jjmerino/tuti" pytest

# The released dabs, via the official installer. DABS_INSTALL_DIR puts it on the
# box PATH; the installer always fetches releases/latest.
#
# The ADD fetches the latest-release metadata: its content changes on every
# release, so it invalidates the install layer below exactly when a new dabs
# ships — otherwise Docker would cache the install and the box would keep an
# outdated binary. It is a cache key, not used at runtime.
ADD https://api.github.com/repos/jjmerino/dabs/releases/latest /tmp/dabs-release.json
RUN DABS_INSTALL_DIR=/usr/local/bin \
      sh -c 'curl -fsSL https://dabs.dev/install.sh | sh' \
    && dabs --help >/dev/null

WORKDIR /work
