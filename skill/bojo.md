# bojo — Agent Skill

> `bojo skill` prints this text. Save it as `bojo skill > .agents/skills/bojo/SKILL.md`.
> It describes only what exists. If the CLI and this text disagree, the CLI wins —
> say so instead of guessing.

## When this skill applies

Any project with a `bojo.toml` or `bojo.pkg.toml`, any mention of `bojo build`,
`run`, `get`, `lib verify`, or any Mojo program spanning more than one file.

## What bojo is

A package manager and build driver for Mojo. It solves what `mojo build` cannot:
dependencies with identity and versions, native code (C/Rust) that has to be
compiled and linked, reproducible library verification and docs, and a result
that runs on another machine.

Deliberately absent: no precompiled `.mojoc` artefacts, no static linking, no
automatic hooks, no silent dependency upgrades.

## Files

| File | Role | Committed |
|---|---|---|
| `bojo.toml` | project manifest (`schema = 1`): deps, aliases, `[override]`/`[replace]`, hooks | yes |
| `bojo.pkg.toml` | repository root: `[repo]` plus at most one `[pkg]` and one `[cmd]` | yes |
| `bojo.lock` | every dependency by module path with repo, tag, full commit, tree | yes |
| `bojo.allow` | approved dependency hooks (JSONL) | yes |
| `API.md` | generated public API contract from `mojo doc` JSON (libraries) | yes |
| `.vendor/` | `.vendor.json` only: what was vendored and at which commit (`vendor --check`) | no |
| `.bojo/` | store (`cache/mojo/src/`), module root (`cache/mojo/modules/`, `-I`), compiler cache, Python env | no |
| `.out/` | build result | no |

## Import paths

```mojo
from gh.bojolib.tls import TLSContext          # v0.x and v1.x
from v2.gh.bojolib.socket import Socket        # v2.0.0+
```

- `v0.x` and `v1.x`: `<host>.<owner>.<repo>`
- `v2.0.0+`: `v<N>.<host>.<owner>.<repo>`

A repository is its package. The package name is the repository name as a Mojo
identifier (`mojo-tls` → `mojo_tls`); it is never declared. The host is a short
alias: `gh` (github.com), `gl` (gitlab.com), `cb` (codeberg.org), `sr`
(git.sr.ht); any other host keeps its literal name.

Git repositories stay flat (never `v1/` or `v2/` subdirectories): `main` tracks
the latest major version, and an optional `v1` branch carries `v1.x` maintenance
tags. Because `v1` and `v2` map to distinct paths in `.bojo/cache/mojo/modules/`,
both can coexist in the same project without collision.

A repository holds one library (`[pkg]`, `__init__.mojo` at the root) and
optionally one command (`[cmd]`, default `cmd/main.mojo`). A command is never
imported and has no module path.

Libraries always write the full import path; only the root project may alias,
and only in `bojo.toml`, keyed by module path:

```toml
[alias]
"gh.bojolib.tls" = "tls"
"v2.gh.bojolib.socket" = "socket"
```

```mojo
from tls import TLSContext         # bojo generates .bojo/cache/mojo/modules/tls/__init__.mojo
from socket import Socket          # bojo generates .bojo/cache/mojo/modules/socket/__init__.mojo
```

## Local dependencies

Develop a library next to the project before it is published:

```toml
[deps.local_socket]
path = "../socket"
```

The import path comes from that directory's own `[repo] url`, so nothing changes
when it is pushed and pinned. A local dependency is never written to `bojo.lock`
and may not declare hooks.

## Versions, conflicts, `[override]` and `[replace]`

One version per repository, as a tag `vX.Y.Z` (`v0.1.0`). It covers library and
command. A prefixed tag (`pkg/vX`, `pkg@vX`) is ignored. `"latest"` or a missing
value means the highest tag.

By default, two different commits for the same module path are an error — bojo
never upgrades silently. Only the root consumer's `bojo.toml` (never
`bojo.pkg.toml`) may resolve a diamond conflict or redirect to a fork/local
checkout:

```toml
[override."gh.bojolib.io"]
version = "v0.2.1"                 # or commit = "9f3a2b..."

[replace."gh.bojolib.socket"]
repo    = "https://github.com/myorg/socket-fork"
version = "v1.4.2"                 # or path = "../socket"
```

## Commands

```
bojo init                        create a project
bojo get <url>@<version>         add a dependency (--as <name> to alias; runs static pull check)
bojo remove <url>                delete it from its repository table
bojo update                      re-resolve and write bojo.lock
bojo build [--locked]            build into .out/
bojo run <url>@<version>         ephemeral build and run the repository's [cmd]
bojo install <url>@<version>     ephemeral build -> ~/.bojo/bin
bojo vendor [--check]            materialise .bojo/cache/mojo/modules, write .vendor/.vendor.json, run vendor hooks
bojo validate                    manifests + graph + .vendor/.vendor.json must agree
bojo test                        run every _tests/*.mojo, exit 3 on failure
bojo fmt [paths…]                run `mojo format`
bojo docs <module-path> [path]   render API.md, docs/index.md, or a file inside docs/
bojo tree | why <module-path>    graph / provenance
bojo verify                      check bojo.lock against fresh checkouts
bojo lib verify [--check-outdated] [--strict-semver] [--json]
                                 5-check library gate (manifest/imports, isolated build+tests, outdated deps, API.md/semver, toolchain)
bojo lib api [--check] [--output API.md]
                                 generate or check deterministic API.md from `mojo doc` JSON
bojo lib pages [--output <dir>]  generate zero-CDN static HTML docs site from `mojo doc`, README.md, and docs/
bojo doctor                      toolchain, C compiler, triple, PATH
bojo toolchain list|install|remove <mojo|python> [version]
bojo deps list|add|add-python    edit dependencies
bojo repo-index <url>[@ref] [--follow]   read manifests over raw HTTP, emit JSONL
bojo search <term> [--index <file>] [--json]
bojo cache list|clear            show / clear the project cache
bojo allow <id>|--json|--list    approve or list hooks
bojo clean [--all]               remove .bojo/ (--all: also .out/ and .vendor/)
bojo skill                       print this text
```

Exit codes:

| Code | Meaning | Reaction |
|---|---|---|
| 0 | success | — |
| 1 | usage or configuration error | quote the message, do not guess |
| 2 | blocked by a missing hook approval, **nothing ran** | show the user the proposal; never approve it yourself |
| 3 | build, link, test, or library verification failure | `bojo doctor`, then the log |
| 4 | network or clone failure | retry |
| 5 | lock or vendor conflict (`--locked`, `vendor --check`) | suggest `bojo update` / `bojo vendor`; do not run it yourself |

## Library authoring and pull safety

- `bojo get` and `bojo vendor` run fast, **static-only** checks (manifest
  validity, import purity, package structure). They never execute foreign tests
  or build code.
- `bojo lib verify` runs the full 5-check gate locally and in CI:
  1. Manifest & import purity (forbids bare/alias/Pixi-only imports in library code)
  2. Isolated non-excluded consumer build probe + `_tests/*.mojo`
  3. Outdated dependency check
  4. `API.md` freshness + semver breaking-change diff against the previous tag
  5. Mojo toolchain compatibility
- `bojo lib api` renders `API.md` directly from Mojo docstrings via `mojo doc` JSON.
- `bojo lib pages` builds a unified, zero-CDN static HTML site for GitHub Pages.

## Native code

The package ships sources under `ffi`; the **consumer** compiles them. A library
never runs a compiler on someone else's machine without explicit approval.

A package may declare its own compile step as a `vendor` hook: it runs **once**,
when the source is materialised (`bojo vendor`, `update`, `get`, `run`), in the
store checkout (`.bojo/cache/mojo/src/…`) — never on `bojo build`. Its output
belongs in `.bojo/`, not in `.vendor/`. Hooks need approval; the line lands in
`bojo.allow`, which is committed so CI can run them unattended.

```toml
# in bojo.pkg.toml
[pkg]
description = "C printing helpers"

[pkg.linker]
libs = ["cprint"]
ffi  = ["cprint.c"]
```

```toml
# in the consumer's bojo.toml
[linker]
libdirs = [".bojo/native"]
libs    = ["cprint"]

[hooks]
pre-build = [
  ["sh", "-c", "mkdir -p .bojo/native && cc -shared -fPIC -O2 -o .bojo/native/libcprint.so .bojo/cache/mojo/modules/gh/bojolib/cprint/cprint.c"],
]
```

Rust works the same way: `crate-type = ["cdylib"]`, `#[unsafe(no_mangle)] pub extern "C"`.

## Runtime data

```toml
[pkg.linker]
runtime = ["names.txt"]
```

Copied to `.out/data/<package>/`; the launcher exports `BOJO_DATA`.

## Python

```toml
[version]
mojo   = "1.2.0"
python = "3.12"

[deps.python]
numpy = "2.1.0"
```

`bojo toolchain install python 3.12` once. A toolchain pinned in `bojo.toml`
but missing on the machine is installed automatically on first use — mojo and
python alike — and bojo prints what it downloads. Wheels only, hashed, installed
offline into `.bojo/cache/python/…`; the launcher sets `PYTHONHOME`,
`PYTHONPATH`, `MOJO_PYTHON_LIBRARY`.

## Output

`dir` (default): `.out/<name>` + `lib/` + `python/` + `data/`.
`onefile`: one self-extracting file; first start unpacks into
`~/.cache/bojo/run/<hash>/` and marks it, later starts run directly.

## Rules an agent must never break

- Never invent a flag; `bojo --help` and the subcommand are authoritative.
- Never approve a hook on the user's behalf, and never add to `bojo.allow`.
- After a manifest change, run `bojo build` once and quote the first error.
- `bojo why <module-path>` explains why something is in the tree; `bojo tree` shows the shape.
