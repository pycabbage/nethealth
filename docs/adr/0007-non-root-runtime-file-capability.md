# 0007. Minimal scratch runtime, run as root with only NET_RAW (not non-root on alpine)

## Status

Accepted (2026-07). Supersedes the initial non-root-on-alpine choice.

## Context

The pinger opens raw ICMP sockets (needs NET_RAW) and builds to a single static
(`CGO_ENABLED=0`) binary. The runtime base image and the user/capability model are the
decision here.

A raw socket is usable only if NET_RAW is in the process's *effective* set. Two ways to get
there:

- run as **root**: root gets NET_RAW effective directly from the container's granted caps;
- run as a **non-root** uid: needs a file capability on the binary (`setcap cap_net_raw+ep`)
  or ambient caps. `setcap` needs libcap and cannot run in a scratch final stage (and the
  `security.capability` xattr does not survive `COPY --from`), so non-root forces a distro
  base (alpine) carrying a shell, package manager, and libcap in the image.

## Decision

Use `FROM scratch` and run as **root**, and confine the container rather than the process:

- compose `cap_drop: [ALL]` then `cap_add: [NET_RAW]` — the only capability the pinger needs;
- `security_opt: ["no-new-privileges:true"]`;
- `read_only: true` (the pinger writes nothing to disk).

## Consequences

- Smallest attack surface: the image is the static binary and nothing else — no shell, no
  busybox, no package manager, no libc — so a compromised process has no userland to pivot
  with, and the image carries no distro CVEs. This is a *smaller* attack surface than
  non-root-on-alpine, where obtaining a non-root uid meant importing a whole alpine userland
  (the earlier decision had this trade backwards).
- "root" here is container-root confined to a single capability (NET_RAW), with
  no-new-privileges and a read-only rootfs — a much smaller target than the previous default
  Docker cap set + NET_RAW.
- The static binary needs nothing else at runtime: it reaches `alloy:4317` over plaintext
  gRPC (no TLS → no CA certs), and DNS resolves via Go's pure resolver over Docker's injected
  `/etc/resolv.conf`.
- Trade-off accepted: the process runs as uid 0 inside the container. Given the confinement
  above and a single static binary, that is preferred over adding a distro userland solely to
  obtain a non-root uid.
