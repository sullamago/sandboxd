# Decision: Pin claude-code-cli-ui commit SHA

**Date:** 2026-06-13
**Status:** Accepted
**Owner:** sandboxd maintainers

## Context
Integrating Ngxba/claude-code-cli-ui ("agents-ui") into the sandboxd-base
image. For supply-chain reproducibility and a deterministic build pipeline,
we pin a specific commit rather than tracking `main`.

## Decision
**Pinned commit SHA:** `46494279f514d7116b08311c32dd6a1c510a6f7a`
**License:** MIT
**Upgrade cadence:** Quarterly review; bumps require an image rebuild
(there is no runtime auto-update).

## Consequences
- Reproducible builds: anyone running `./install.sh` gets the same agents-ui.
- Security fixes require an image rebuild — operators must track upstream.
- A `docs/decisions/` entry now exists; future pins append below.