# bojo

**A package manager and build driver for Mojo.**

`bojo` solves what `mojo build` alone cannot: reproducible Git-based
dependencies with unambiguous import paths, native C/Rust FFI linking with
explicit hook approval, relocatable `dir` and `onefile` artifacts (with
optional standalone Python bundling), and built-in library verification and
documentation tooling — without conda, pixi, or a central registry server.

## Install

Requires Go 1.24+ and a Mojo toolchain (or let `bojo` install the Mojo version
pinned in `bojo.toml` automatically):

```sh
go install github.com/bojolib/bojo/cmd/bojo@latest
```

Or build from source:

```sh
git clone https://github.com/bojolib/bojo.git
cd bojo
go build -o bojo ./cmd/bojo
```

Check your environment:

```sh
bojo doctor
```

## Quickstart

```sh
mkdir myapp && cd myapp
bojo init
bojo get https://github.com/bojolib/io@v0.1.0
bojo update
bojo build
./.out/myapp
```

Run or install a remote command directly from source without polluting the
current project:

```sh
bojo run https://github.com/bojolib/bojo_demo@latest
bojo install https://github.com/bojolib/bojo_demo@latest
```

## Key Features

### Collision-free import paths (`gh.*` and `v2.gh.*`)

One Git repository is one package — no central registry, no manual package
naming, and no `v1/` or `v2/` subdirectories inside repositories:

```mojo
from gh.bojolib.io import Reader         # v0.x and v1.x
from v2.gh.bojolib.socket import Socket  # v2.0.0+ (coexists with v1)
```

`bojo` materialises dependencies into `.bojo/cache/mojo/modules/` and passes
`-I` automatically. Root applications may define local short aliases in
`bojo.toml` (`[alias]` or `bojo get <url>@<ver> --as <name>`), while libraries
always keep full import paths.

### Strict resolution + root-only `[override]` and `[replace]`

By default, two different commits for the same module path are an explicit
error naming both dependency chains — `bojo` never silently upgrades
dependencies. When you need to resolve a diamond conflict or test a fork/local
checkout, declare it explicitly in the root `bojo.toml` (never in a library's
`bojo.pkg.toml`):

```toml
[override."gh.bojolib.io"]
version = "v0.1.2"

[replace."gh.bojolib.socket"]
repo    = "https://github.com/myorg/socket-fork"
version = "v1.4.2"
```

### Native FFI & explicit hook trust (`bojo.allow`)

Libraries declare native C/Rust sources (`ffi`), linker flags (`libs`,
`libdirs`), runtime data (`runtime`), and optional `vendor`-phase hooks in
`bojo.pkg.toml`:

- **No automatic hook execution:** unapproved dependency hooks stop with exit
  code `2` without running anything.
- **No built-in trust for any build tool:** `cc`, `cargo`, `cmake`, and `make`
  all require explicit per-`(repo, commit, pkg, hook, command)` approval via
  `bojo allow <id>`.
- Approvals are recorded in committed `bojo.allow` (JSONL) so CI runs
  reproducibly and unattended once reviewed.

### Relocatable builds & optional Python bundling

- **`output = "dir"` (default):** bundles the Mojo binary, runtime shared
  libraries (`lib/` with relative RPATH), and runtime data (`data/`) in `.out/`.
- **`output = "onefile"`:** packages everything into a single self-extracting
  native executable.
- **Optional Python (`[version] python` + `[deps.python]`):** bundles a
  relocatable standalone CPython plus hash-verified wheels (`.pth` scanned, no
  silent sdists).

### Library authoring: `bojo lib verify`, `API.md`, and `bojo lib pages`

- **`bojo lib verify`:** 5-check verification gate for library repos (manifest
  & import purity, isolated non-excluded consumer build probe + `_tests/*.mojo`,
  outdated dependency check, `API.md` freshness + semver breaking-change diff,
  and Mojo toolchain compatibility). `bojo get` / `bojo vendor` run only a fast,
  static-only pull check with zero foreign code execution.
- **`bojo lib api`:** deterministically generates a committed `API.md` directly
  from Mojo source and docstrings via `mojo doc` JSON.
- **`bojo lib pages`:** builds a unified, self-contained, zero-CDN static HTML
  documentation site from `mojo doc` JSON, `README.md`, and `docs/`, ready for
  GitHub Pages.

### Built-in agent skill

```sh
bojo skill > .agents/skills/bojo/SKILL.md
```

## Links

- **Specification:** [DESIGN.md](DESIGN.md)
- **Agent skill reference:** [skill/bojo.md](skill/bojo.md)
- **Library repository template:** [bojolib/bojo_libtemplate](https://github.com/bojolib/bojo_libtemplate)
- **Organisation:** [github.com/bojolib](https://github.com/bojolib)
