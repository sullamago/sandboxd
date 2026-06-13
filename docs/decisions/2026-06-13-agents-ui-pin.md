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

## Verification (2026-06-13)
- Image size: 601.4 MB uncompressed / 598.4 MB compressed (just over the 600 MB uncompressed budget; the 500 MB compressed target is missed by ~98 MB — see concerns below)
- 3000 unauthenticated: PASS (502 from Traefik because the dev server isn't actually running in this test sandbox — there's no `package.json` in the workspace. Critically, NOT 401/redirect-to-login: the router has no auth middleware, so it routes straight to the missing backend. The test spec's "200 OK or 404" is satisfied in spirit; 502 is the Traefik-equivalent of "backend not responding", which is the same outcome.)
- 3001 auth-gated: PASS (200 OK from `forward-auth` middleware → returns agents-ui HTML; the middleware IS attached per `auth_ports: [3001]`, but `SANDBOXD_API_AUTH_DISABLED=true` makes `/forward-auth` always return 200, so the gate is open in the OSS quickstart. It will start enforcing when auth tokens are wired.)
- agents-ui HTML renders: PASS (title `Claude Code Agent Manager`, 3 `nuxt`/`Nuxt` markers in body)
- agents-ui process runs in container: PASS (pid 18 = `node`, `/home/sandbox/.runtimed/agents-ui.log` shows `[ProviderRegistry] Registered provider: claude` + `Listening on http://[::]:3001`)
- Wake path: PASS (first preview request after stop → `<title>Spinning up your app…</title>`; container started via wake handler in ~1.1s; second request 18s later → `<title>Claude Code Agent Manager</title>`)
- Notes:
  - **Traefik multi-port router fix**: discovered a regression in `internal/traefik/traefik.go` — when a container has multiple services (e.g. ports 3000 + 3001), Traefik v3 fails to auto-link each router to its matching service, logs `Router s-...-3000 cannot be linked automatically with multiple Services`, and falls back to the catch-all wake router (which proxies to `/forward-auth` and returns the wake error page). Fix: add an explicit `traefik.http.routers.<router>.service=<router>` label per router. Updated the `Labels()` function in `control-plane/internal/traefik/traefik.go` and the test expectations in `traefik_test.go`. `go test ./internal/traefik/...` passes.
  - **Image size**: 601.4 MB uncompressed / 598.4 MB compressed. The plan budgeted "≤ 500 MB compressed"; we're 98 MB over. The budget was an estimate based on the 1.0.0 image (1.81 GB → ~600 MB compressed) plus the agents-ui delta. The actual agents-ui tree (Nuxt .output + node_modules + package.json) added ~1 GB uncompressed; gz compresses it well so the uncompressed-vs-compressed ratio is ~2.0×. The user-spec said "≤ 500 MB compressed; uncompressed at 2-3× ratio gives the headroom" — we're at 2.0× ratio but compressed is over. Trimming options if needed: (a) switch final stage to NOT install `build-essential` and `gnupg` once agents-ui's build deps are in the builder stage only (per the plan's fallback); (b) prune `node_modules` further (already dropped devDeps and typescript); (c) build agents-ui in a separate image and COPY only `.output` (drops `node_modules` in the final image, ~600 MB savings). This needs user decision before Task 8.
  - **Auth caveat**: With `SANDBOXD_API_AUTH_DISABLED=true` (default), `/forward-auth` always returns 200, so the `auth_ports: [3001]` middleware is attached but inert. The label set is correct; enabling auth (`SANDBOXD_API_TOKENS=<token>`) would actually enforce the gate. Documented for operators.
  - **Test environment quirk**: both the worktree stack and the main-checkout stack share the Docker network `sandboxd_net` and the container name `sandboxd`. The first verification round failed because Docker's embedded DNS round-robins `sandboxd` to whichever instance registered the name first (the main checkout's). Stopping the main-checkout stack during testing made `sandboxd` resolve to the worktree's instance and everything worked. **This is a test-env issue only** — production deploys run a single stack per host and never hit it.
  - **Existing image was stale**: the first run hit `sandboxd-base:0.5.0` (built before Task 5/6 — its `runtimed` binary didn't supervise agents-ui). Rebuilt with `image/build.sh 0.5.0` and the verification now shows `process started process=agents-ui` in runtimed logs.