# 0007. Non-root runtime with a file-capability raw socket (alpine, not scratch)

## Status
Accepted (2026-07)

## Context
The pinger opens raw ICMP sockets, which require the NET_RAW capability. The build produces
a static (`CGO_ENABLED=0`) binary that would run on `scratch`. The open question is which
runtime base image to use.

## Decision
Use `alpine` for the runtime stage and run the pinger as a non-root user:
- `apk add libcap` provides `setcap`; `setcap cap_net_raw+ep` on the binary makes NET_RAW
  *effective* for a non-root process.
- `adduser` creates the unprivileged `pinger` user (uid 10001); `USER pinger` drops root.
- The container is granted NET_RAW via the compose `cap_add`, which puts the capability in
  the permitted set; the file capability is what lets the non-root process actually use it.

## Consequences / why not scratch
- `scratch` (and distroless) have no shell, package manager, `setcap`, or `adduser`, so the
  non-root + file-capability setup cannot be performed in the final stage. Setting the cap in
  an earlier stage does not survive `COPY --from` (BuildKit does not preserve the
  `security.capability` xattr).
- To use `scratch` you would instead run the process as **root** and rely solely on the
  container's NET_RAW, dropping the non-root hardening. The chosen trade-off keeps non-root
  at the cost of a small (~8 MB) alpine base plus libcap.
- The static binary needs nothing from alpine at runtime — DNS for `alloy:4317` resolves via
  Go's pure resolver over Docker's injected `/etc/resolv.conf`. Alpine exists only for the
  non-root + capability setup.
