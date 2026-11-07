# AGENTS.md — Working agreement & quality bar for dispatchq

This file binds any AI agent working in this repository. The maintainer does **not** review
code and is not interested in reading it. Full responsibility for code and software quality
rests with the agent. The maintainer wants to see **results** and discuss **process/details** —
never code walkthroughs.

See `PLAN.md` for the architecture and decisions (D1–D9, verification V1–V5).

## 1. Non-negotiable quality bar

Nothing is presented as "done" until **all** of the following hold:

- **Builds clean:** `go build ./...` with zero errors.
- **Formatted:** `gofmt`/`goimports` applied; no diffs.
- **Vetted & linted:** `go vet ./...` and `golangci-lint run` pass with zero findings.
- **Tested:** `go test ./... -race` passes. New/changed behavior has tests (see §2).
- **Self-reviewed:** the agent runs `/code-review` on its own diff and resolves every
  confirmed finding before showing results — the maintainer does not do this.
- **Behaviour-verified:** for anything with a runtime surface, run `/verify` (or an explicit
  end-to-end exercise) and observe it actually working — not just green tests.

If any gate fails, it is fixed before the work is reported — never reported with caveats like
"tests are red but…".

## 2. Testing standards

- **Unit tests** for all core logic: scheduler, trigger evaluation (asap/fixed/cron),
  priority ordering, parallelism rules, the reactive limit gate, store (TOML/JSONL), and
  rate-limit / auth output parsing. Prefer Go **table-driven tests**.
- **Integration tests** for the Claude CLI invocation layer using a **fake `claude`
  binary/stub** (never the real API in CI): assert flag construction, session-id reuse,
  resume-after-limit flow, and the failure/auth/rate-limit branches.
- **Determinism:** inject the clock and any randomness so scheduling/wake logic is testable;
  no reliance on wall-clock sleeps in tests.
- Coverage is a means, not a target — cover behaviour and edge cases, not lines for a number.

## 3. Engineering practices

- **Lightweight & few dependencies** (NFA-01): justify every third-party module; prefer the
  standard library. Keep the daemon's footprint minimal.
- **Clear errors:** wrap with context (`fmt.Errorf("...: %w", err)`); never swallow errors;
  no silent failure paths in the unattended runner.
- **Small, focused commits** with descriptive messages; the tree stays releasable on `main`.
- **Always branch, then PR.** Never change code directly on `main`. Whenever a change to
  code off `origin/main` is needed, **first** create a new branch off the latest
  `origin/main` — a `feature/<short-topic>` branch for features/enhancements or a
  `hotfix/<short-topic>` branch for urgent fixes — commit the work there, push it, and
  **open a pull request** against `main` afterwards. This holds for every code change, no
  matter how small; a one-line fix still gets its own branch and PR. Keep the branch current
  with `origin/main` and CI green while the PR is open.
- **GitFlow branch names.** All branches use GitFlow prefixes: `feature/<short-topic>` for
  features and planned enhancements, `hotfix/<short-topic>` for urgent production fixes,
  and `release/<version>` for release preparation. Do not create tool- or agent-specific
  prefixes such as `t3code/`, `claude/` or `codex/`.
- **Never merge a PR.** An agent may open, update, and push to feature branches and
  keep CI green, but **merging is exclusively the maintainer's decision and action** —
  never merge (squash, rebase, or otherwise), never push directly to `main`, and never
  auto-merge. Leave the PR ready and hand the decision to the maintainer.
- **No dead code, no TODO-as-shipping-plan, no commented-out blocks.**
- **Keep the downgrade floor current.** `update.DataFloor` is the oldest release that runs on
  the data the current code leaves behind; a development build offers to reinstall the latest
  release only at or above it. A change that migrates data one way (moves files, rewrites a
  format or a key older releases rely on, renames the LaunchAgent) raises it to the version
  the change will ship in, in the same PR.
- **Concurrency safety:** the daemon runs tasks in parallel — guard shared state, run tests
  with `-race`, avoid data races by design.
- **Secrets:** Pushover credentials and any tokens live in config/keychain, never in code,
  logs, or the repo. Local-only; nothing leaves the machine (NFA-04).

## 4. CI

- A GitHub Actions **CI workflow** runs on every PR and every push to `main`: build + `gofmt` check + `go vet` +
  `golangci-lint` + `go test ./... -race`. Red CI blocks a release.
- The **release workflow** (tag `v*`) is separate and builds the unsigned Apple silicon
  `.pkg` and both Windows beta installers, publishing only when all three pass (see
  `PLAN.md` §11.3); it depends on CI being green.

## 5. How results are reported to the maintainer

- Report **outcomes**: what works, CI status, a demo/screenshot or a short recorded run,
  and a plain-language changelog. Not code.
- Surface **process/design choices and trade-offs** for discussion — the maintainer wants
  those. Keep them non-code-level (behaviour, UX, reliability, install experience).
- Be honest about limitations and anything skipped or deferred.
- **Every PR link comes with screenshots.** Whenever a message to the maintainer contains a
  PR link (new PR, follow-up push, status report), the same message embeds screenshots of the
  changed behaviour as images (`![…](/absolute/path.png)`), not a description of them. Show the
  real app or the dev UI with real data, cropped to what changed. A change without a visual
  surface shows its observable result instead (CLI output, a notification, a log excerpt) as a
  screenshot. A PR link without screenshots is an incomplete report; if a screenshot truly
  cannot be produced, say why in that message. If the preview tool hangs, headless Chrome works:
  run `npm run dev` in `web/` against the daemon and take
  `"Google Chrome" --headless=new --window-size=1300,1650 --virtual-time-budget=8000 --screenshot=<file> <url>`.
- **No dev builds for the maintainer.** Do not build, offer or open a dev installer package
  (`scripts/build-pkg.sh`) for a PR or after a merge. The maintainer installs only released
  versions (see the `dispatchq-release` skill). After a merge, fast-forward the local
  `/Users/dm/Documents/Private/dispatchq` checkout to `origin/main`; nothing else.

## 6. Keep the plan's phase status current

`PLAN.md` §12 holds the build-phase table with a status column (✅ / 🔄 / ⏳ / ⏸). It is a
living record and must stay accurate:

- Set a phase to 🔄 when work on it starts, ✅ only when it meets the §1 quality bar in full.
- Never mark a phase ✅ while any of its acceptance work is incomplete — note residual items
  explicitly instead (honesty over green checkmarks).
- Update the table in the **same change** that moves the work, so `main` never shows a stale
  status. If the phase breakdown itself changes, update the table and note why.

## 7. Keep the README current

`README.md` is the user-facing documentation and must always match the shipped behaviour —
never let it drift:

- Any change that adds, removes, or alters something a user can see or do (a feature, a view,
  a CLI command/flag, a setting, an install/uninstall step, an on-disk file, default
  behaviour) updates `README.md` in the **same change/PR**, so `main` never documents behaviour
  that no longer matches the code.
- Keep every part consistent: the feature list, the table of contents, the app-views section,
  the CLI/command reference, the settings table, and the "Data on disk" table.
- Before reporting work as done, re-read the relevant README sections and confirm they reflect
  reality — treat a stale README as a failed quality gate, exactly like a red test.
- A platform or release change updates that platform's install section as well as the README
  introduction. End-user install steps describe the implemented normal release flow (Releases
  page, the platform's release asset, its installer); CI artifacts and local builds belong in a
  separate developer build section, never in the install steps.
