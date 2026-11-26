# bojolib/bojo — Agent Instructions

Repository for **bojo** (`https://github.com/bojolib/bojo`), a package manager
and build driver for Mojo.

## Read first

1. `DESIGN.md` — the specification, normative. Decisions are §16 (`D<n>`).
2. `REVIEW-FINDINGS.md` — review findings and their disposition.
3. `POST-V0.1.md` — everything deliberately deferred, with reasons.
4. `skill/bojo.md` — embedded agent skill emitted by `bojo skill`.

## Language

Everything in this repository is written in **English**: documentation, code
comments, commit messages, CLI output, and issue text. Code identifiers are
English.

## Development & verification commands

Standard Go 1.24+ workflow (stdlib + `github.com/BurntSushi/toml` only — no
extra external dependencies):

```sh
go vet ./...
go test -race ./...
go build ./cmd/bojo
```

- Run `go vet ./...` and `go test -race ./...` before finishing any Go change.
- Never commit generated binaries or local caches (`/bojo`, `/.bojo/`, `/.out/`, `/.vendor/`).

## Rules that protect the design

- **The spec is the source of truth.** Any semantic change to `DESIGN.md`
  requires a new or amended decision in §16, including the rejected alternative.
  No silent behaviour changes.
- **Every new file or CLI command must appear** in the tables of `DESIGN.md`
  (§5 files, §13 commands). Untracked surface area is a bug.
- **Hooks are never automatic.** bojo must not execute a hook on `get`,
  `install`, `run`, `vendor`, or `build` without a matching line in `bojo.allow`.
  No `-y`, no "allow all", no Enter-to-confirm. `bojo.allow` is written only
  through the `bojo allow` code path.
- **No built-in trust for any build system.** `cargo`, `cmake`, `meson`, `make`
  are treated exactly like an arbitrary hook command: one explicit approval per
  (repo, commit, pkg, hook, command).
- **Zero foreign code execution on pull.** Automatic checks during `bojo get` /
  `bojo vendor` must remain static-only (D90); tests and build probes run only
  when explicitly invoked (`bojo test`, `bojo lib verify`).
- **Never install prebuilt binaries.** `install` and `run` fetch source at a
  pinned commit and build locally. No `.so`/`.dylib`/`.dll` downloads.
- **No static-linking claims.** Mojo cannot do it today (`#1317`). Keep the
  `output` seam, document the reason, never ship a fake `static`.
- **Do not break consumer import paths.** bojo materialises the module root in
  `.bojo/cache/mojo/modules/` and passes `-I` (D87). A consumer's Mojo source
  must never need edits because of bojo.

## Cache and filesystem invariants

- Every cache write is atomic: temp path + `rename` (§8, D26).
- Store checkouts under `.bojo/cache/mojo/src/` are verified once at fetch time
  and writable afterwards for approved `vendor` hooks (D33, D84).
- `.vendor/` holds only `.vendor.json` metadata; the materialised module root
  lives under `.bojo/cache/mojo/modules/` (D87).
- Never persist credentials: userinfo is stripped and rejected (§7, D25).
- Case-insensitive filesystems (APFS) are first-class: routes are compared
  case-folded (§9, R1); path segments use short hashes and lowercased slugs (§8).

## Output style

CLI output is read by a human on a terminal and parsed by agents:

- one fact per line, no dense tables, no spinners, no required colour;
- complete sentences for errors, each naming the exact next command;
- `--json` on every command that produces data;
- distinct exit codes (§13) so an agent can tell "failed" from "blocked by a
  hook, nothing executed" (exit 2).

## Tests

- Table-driven Go tests.
- Golden tests for manifest parsing, `import` resolution, URL normalisation, and
  the resolver.
- No network in `go test`. Fixtures are local git repositories in `t.TempDir()`,
  committed with fixed dates so hashes are stable.
- Every hook/allow rule needs a test proving it does **not** run by default.
- Every finding marked *high* in `REVIEW-FINDINGS.md` needs a regression test
  before it is closed.

## Scope guard

bojo targets **Mojo**. The core is language-agnostic on purpose (an internal
`Driver` struct), but no second language driver is built before v0.2. Other
languages appear only as hook commands (project `pre-build`/`post-build` or
dependency `vendor` hooks).
