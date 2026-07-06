# 0008. Automated RustFS bucket provisioning via one-shot init service

## Status

Accepted (2026-07)

## Context

RustFS does not auto-create buckets on first write, and Mimir/Loki require their target
buckets (`mimir-blocks`, `mimir-ruler`, `loki-data`) to pre-exist. Until now these three
buckets existed only as directories inside the `rustfs-data` named volume, created once
out-of-band with a disposable `minio/mc` container run by hand. `compose.yml` contained no
step that reproduced them: if the volume were ever lost (`docker compose down -v`, fresh
host, disaster recovery), RustFS would come up empty and Mimir/Loki would fail to write,
with the failure only surfacing at write time.

## Decision

Add a `rustfs-init` service to `compose.yml` that runs `minio/mc mb --ignore-existing` for
the three buckets once, after `rustfs` is healthy. It authenticates via `MC_HOST_rustfs`
(same credentials as `RUSTFS_ACCESS_KEY`/`RUSTFS_SECRET_KEY`) and exits after creating the
buckets. `mimir` and `loki` add `depends_on: rustfs-init: condition:
service_completed_successfully` alongside their existing `rustfs: service_healthy`
dependency, so they never start before the buckets exist.

`rustfs-init` sets `restart: "no"` rather than the `unless-stopped` used by every long-lived
service in this file — it is meant to run once and exit 0; a restart policy would relaunch it
forever after success. `mb --ignore-existing` is idempotent (exits 0 whether or not the
buckets already exist), which is what makes `service_completed_successfully` safe to depend
on on every `docker compose up`, not just the first one.

## Consequences

- `compose.yml` is now self-contained: bucket creation no longer depends on a one-off manual
  step performed outside the repo. A fresh volume (new host, restored backup, `down -v`)
  provisions correctly on the next `up`.
- One extra short-lived container appears on every startup; it exits immediately once the
  buckets are confirmed to exist and does not run again until the next `up`.
- Credentials for `mc` are passed as a plaintext environment variable, matching the existing
  plaintext `RUSTFS_ACCESS_KEY`/`RUSTFS_SECRET_KEY` already in this file — no new secret
  exposure is introduced.
