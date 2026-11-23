# bojo — Agent Skill

> `bojo skill` prints this text. Save it as `bojo skill > .agents/skills/bojo/SKILL.md`.
> It describes only what exists. If the CLI and this text disagree, the CLI wins —
> say so instead of guessing.

## When this skill applies

Any project with a `bojo.toml`, any mention of `bojo build`, `run`, `get`, or any
Mojo program spanning more than one file.

## What bojo is

A package manager and build driver for Mojo. It solves what `mojo build` cannot:
dependencies with identity and versions, native code (C/Rust) that has to be
compiled and linked, and a result that runs on another machine.

Deliberately absent: no precompiled artefacts, no static linking, no automatic
hooks.

## Files

| File | Role | Committed |
|---|---|---|
| `bojo.toml` | what this project needs (`schema = 1`) | yes |
| `bojo.pkg.toml` | repository root: `[repo]` plus at most one `[pkg]` and one `[cmd]` | yes |
| `bojo.lock` | every dependency by module path with repo, tag, full commit, tree | yes |
| `bojo.allow` | approved hooks (JSONL) | yes |
| `.vendor/` | `.vendor.json`: what was vendored, at which commit (`vendor --check`) | no |
| `.bojo/` | module root (`-I`), cache, toolchains, build state | no |
| `.out/` | build result | no |

## Import paths

```mojo
from gh.LukasLow.mojo_tls import TLSContext
```

`<host>.<owner>.<repo>` — a repository is its package. The package name is the
repository name as a Mojo identifier (`mojo-tls` → `mojo_tls`); it is never
declared. The host is a short alias: `gh` (github.com), `gl` (gitlab.com),
`cb` (codeberg.org), `sr` (git.sr.ht); any other host keeps its literal name.

A repository holds one library (`[pkg]`, `__init__.mojo` at the root) and
optionally one command (`[cmd]`, default `cmd/main.mojo`). A command is never
imported and has no module path.

Libraries always write the full path; only the project may alias, and only in
`bojo.toml`, keyed by module path:

```toml
[alias]
"gh.nsalerni.mojo_tls" = "tls"
```

```mojo
from tls import TLSContext     # bojo generates .vendor/tls/__init__.mojo
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

## Versions

One version per repository, as a tag `vX.Y.Z` (`v0.1.0`). It covers library and
command. A prefixed tag (`pkg/vX`, `pkg@vX`) is ignored. `"latest"` or a missing
value means the highest tag. Two different commits for the same module path are
an error — bojo never upgrades silently.

## Commands

```
bojo init                        create a project
bojo get <url>@<version>         add a dependency (--as <name> to alias)
bojo remove <url>                delete it from its repository table
bojo update                      re-resolve and write bojo.lock
bojo build [--locked]            build into .out/
bojo run <url>@<version>         ephemeral build and run the repository's [cmd]
bojo install <url>@<version>     ephemeral build -> ~/.bojo/bin
bojo vendor [--check]            write .vendor and run vendor hooks; --check verifies only
bojo validate                    manifests + graph + .vendor must agree
bojo test                        run every _tests/*.mojo, exit 3 on failure
bojo fmt [paths…]                run `mojo format`
bojo docs <module-path> [path]   render docs/index.md or a file inside docs/
bojo tree | why <module-path>    graph / provenance
bojo verify                      check the lock against a fresh checkout
bojo doctor                      toolchain, C compiler, triple, PATH
bojo toolchain list|install|remove <mojo|python> [version]
bojo deps list|add|add-python    edit dependencies
bojo repo-index <url>[@ref] [--follow]   read manifests over raw HTTP, emit JSONL
bojo search <term> [--index <file>] [--json]
bojo channel list|add <url>|remove <url>
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
| 3 | build, link or test failure | `bojo doctor`, then the log |
| 4 | network or clone failure | retry |
| 5 | lock or vendor conflict (`--locked`, `vendor --check`) | suggest `bojo update` / `bojo vendor`; do not run it yourself |

## Native code

The package ships sources under `ffi`; the **consumer** compiles them. A library
never runs a compiler on someone else's machine.

A package may declare its own compile step as a `vendor` hook: it runs **once**,
when the source is materialised (`bojo vendor`, `update`, `get`, `run`), in the
store checkout — never on `bojo build`. Its output belongs in `.bojo/`, not in
`.vendor/`. Hooks need approval; the line lands in `bojo.allow`, which is
committed so CI can run them unattended.

```toml
[pkg]
description = "C printing helpers"

[pkg.linker]
libs = ["cprint"]
ffi  = ["cprint.c"]
```

```toml
[linker]
libdirs = [".bojo/native"]
libs    = ["cprint"]

[hooks]
pre-build = [
  ["sh", "-c", "mkdir -p .bojo/native && cc -shared -fPIC -O2 -o .bojo/native/libcprint.so .vendor/gh/Owner/repo/cprint.c"],
]
```

Rust works the same way: `crate-type = ["cdylib"]`, `#[unsafe(no_mangle)] pub extern "C"`.

## Runtime data

```toml
[pkg.randomname.linker]
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
- `bojo why <pkg>` explains why something is in the tree; `bojo tree` shows the shape.
