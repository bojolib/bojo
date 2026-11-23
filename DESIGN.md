# bojo — Specification v0.1

Status: round 3. Normative. Any semantic change requires a decision in §16
(`D<n>`) including the rejected alternative. Open review findings:
`REVIEW-FINDINGS.md`.

---

## 1. Purpose

bojo is a package manager and build driver for Mojo:

```
bojo get     <url>#<pkg>@<version>   add a dependency
bojo build                           build the project
bojo run     <url>#<pkg>@<version>   build and run a remote command
bojo install <url>#<pkg>@<version>   install a command globally
```

Core idea: **a library author declares in one place what is needed to build and
link.** The consumer types no linker flags.

## 2. Non-goals

- **No static ELF.** Mojo cannot do it (`modular/modular#1317`). `output = "static"`
  does not exist.
- **No prebuilt binaries.** `run`/`install` fetch source and build locally.
- **No global object cache** in v0.1 (D32). Everything lives in the project. The
  single exception is the **toolchain store** under `~/.bojo/mojo/<version>/`
  (D43): toolchains are expensive, identical for every project and useless to
  duplicate.
- **No own registry server.**
- **No second target language** (D34). Other languages appear only as hooks.
- **No mandatory vendor directory.**
- **Windows** is not supported in v0.1 (Mojo does not support it natively).

## 3. Why bojo exists

Measured on Debian bookworm, Mojo 1.2.0 (2016d0cd), aarch64. Numbers: §18.

1. `mojo build hello.mojo` produces a 72 KB ELF that needs
   `libKGENCompilerRTShared.so`, with an **absolute** RUNPATH into the build
   machine's environment. It does not start on a foreign machine.
2. Transitively: `libMSupportGlobals.so`, `libAsyncRTRuntimeGlobals.so`,
   `libstdc++.so.6`, `libgcc_s.so.1`. The three Mojo runtime libraries total
   1.96 MB.
3. **Static linking is impossible**: only `.so`, no `.a`. Additionally all three
   runtime libraries link `libstdc++.so.6` themselves, so a static libstdc++
   would change nothing.
4. `mojo build` needs a C compiler driver on the host at build time.
5. `mojo precompile` emits `.mojoc` **without link metadata**; a raylib binding
   cannot ship its linker flags.
6. The official answer to this is conda/pixi. bojo replaces that path.

A single-file binary is possible as a **wrapper** (native stub, unpacks at start),
not as static linking: 26.4 MB / 10 ms, stripped 5.29 MB / 4–5 ms (§18).

## 4. Terms

| Term | Meaning |
|---|---|
| **repo** | git repository, identified by normalised URL + commit |
| **pkg** | package in a repo, declared in `bojo.pkg.toml` |
| **cmd** | pkg with `kind = "cmd"`, produces a binary |
| **route** | import path a pkg provides |
| **cache** | **project-local** working tree under `.bojo/cache/` (D32) |
| **artifact** | `.mojoc` for (repo, commit, pkg, toolchain, dep closure) |
| **hook** | external process a pkg wants to run during a build |
| **allow** | an explicit approval of a hook, one line in `bojo.allow` |

## 5. Files

### 5.1 `bojo.toml` — what the project needs

```toml
[project]
name    = "example"
version = "0.1.0"
entry   = "main.mojo"

[version]
mojo   = "1.2.0"                   # selects the toolchain (D43)
python = "3.12"                    # optional; presence enables bundling (D46)

[build]
output = "dir"                     # dir | onefile
strip  = true

[deps.local_socket]                # a working directory, developed side by side
path = "../socket"                 # identity comes from its own [repo] url (D85)

[[deps-from]]                      # one table per (repo, version)
repo  = "https://github.com/LukasLow/example_pkg"
tag   = "addition/v0.0.1"
libs  = ["addition"]
alias = { addition = "sum" }        # only on a name collision

[deps.python]                      # Python packages, wheels only (D48)
numpy  = "2.1.0"
orjson = "*"

[hooks]                            # own hooks, no allow needed (D12)
pre-build  = [["templ", "generate"]]
post-build = []
```

### 5.2 `bojo.pkg.toml` — one repository, one package

A repository is its package. There is no import list and no in-repo dependency
graph: at most one library and at most one command live in a repository (D83).

Repository root:

```toml
schema = 1

[repo]
url     = "https://github.com/LukasLow/mojo-tls"
license = "Apache-2.0"

[pkg]                              # the library; the name is derived from url
description = "TLS 1.2 and 1.3 for Mojo"
readme      = "README.md"
docs        = "docs"

[pkg.linker]
libs    = ["tls"]
libdirs = [".bojo/native"]
ffi     = ["tls.c"]
runtime = []

[pkg.deps-from."https://github.com/nsalerni/mojo-net"]
version = "v0.2.8"
```

```toml
[cmd]                              # optional; a command is run, never imported
entry = "cmd/main.mojo"            # the default
```

```
repository/
  bojo.pkg.toml
  __init__.mojo      required when [pkg] exists; its layout is the author's choice
  cmd/main.mojo      the default [cmd] entry
```

Rules:

- **The package name is derived, never declared.** It is the repository name as
  a Mojo identifier: `mojo-tls` → `mojo_tls` (D83). Renaming the repository
  renames the import path.
- `[pkg]` requires `__init__.mojo` at the root. Where the library keeps its
  modules below that is the author's decision.
- `[cmd]` is optional and is not importable: it has no module path and cannot
  collide with anything (D83).
- There are no in-repo dependencies, so no cycle, no diamond and no
  "package name declared twice".
- Versions are repository-wide: one tag `vX.Y.Z` covers library and command.


### 5.3 `bojo.lock` — the truth (committed)

```toml
version = 1
mojo    = "1.2.0-2016d0cd"          # fingerprint from `mojo --version`

[[package]]
name   = "addition"
url    = "https://github.com/LukasLow/example_pkg"
tag    = "addition/v0.0.1"
commit = "9f3a2b1c4d5e6f70a1b2c3d4e5f60718293a4b5c"
tree   = "git-tree:4d1e…"           # git tree OID of the commit
alias  = "sum"                      # optional

[python]                            # only when the project bundles Python
version = "3.12"

[[python.package]]
name    = "numpy"
version = "2.1.0"
sha256  = "…"                       # hash of the installed wheel (D48)
```

The wheel hash lives in the lock and **not** in `bojo.allow`: it is an install
artefact, not a trust decision. If the same version suddenly resolves to a
different hash, the build fails — that is the supply-chain alarm.

### 5.4 `bojo.allow` — trust log (JSONL, committed)

```jsonl
{"repo":"https://github.com/LukasLow/example_pkg","commit":"9f3a2b1c4d5e6f70a1b2c3d4e5f60718293a4b5c","pkg":"web_templ","hook":"pre-build","command":["templ","generate"],"binary":"/usr/local/bin/templ"}
```

Append-only, written only by `bojo allow`. One entry per line.

## 6. Versions and tags

| Rule | Value |
|---|---|
| Tag format | `v<major>.<minor>.<patch>[-<pre>]` |
| Scope | the whole repository: one version for library and command (D83) |
| Truth | the **commit**; the tag is a label |
| `@latest` | highest semver |
| `@<hash>` | 7–40 hex, must resolve **unambiguously**, otherwise error |
| Prefixed tag | `<pkg>/vX.Y.Z`, `<pkg>@vX.Y.Z` — ignored |

## 7. Repository identity and URLs

```
https://github.com/LukasLow/example_pkg#addition@v0.1.0
└───────────── repo ──────────────┘└─ pkg ─┘└ version ┘
```

Normalisation, in this order (D14a):

1. Parse into components (scheme, host, path) — never string surgery.
2. SSH/scp forms (`git@host:owner/repo`, `ssh://git@host[:22]/…`) → `https://`.
3. **Strip userinfo** and discard it; URLs with credentials are rejected (D25).
4. Remove query and fragment; strip trailing `/`, doubled `//` in the path, and
   `.git`/`.GIT`.
5. Lowercase scheme and host; **path stays case-sensitive**; IPv6 in `[…]`.
6. Remove default ports.

`urlhash12 = hex(sha256(normalised))[:12]`.

The import path starts with a short host segment (D82). The table is closed:

| Host | Segment |
|---|---|
| `github.com` | `gh` |
| `gitlab.com` | `gl` |
| `codeberg.org` | `cb` |
| `git.sr.ht` | `sr` |
| anything else | the literal host (`git.example.com`) |

An unknown host keeps its literal name. No abbreviation is invented for it,
because a later mapping could collide with a host that already used that name.

Without `#`: the whole repo, pkg name = repo name, otherwise a recursive scan
(depth ≤ 4). Fallback, not the main path (D4).

## 8. Cache — project-local (D32)

```
project/
  .bojo/
    cache/
      src/<urlhash12>/<commit12>/            working copy, writable
          meta.json                          {url, tag, commit, tree, fetched}
          …repo content (hooks may write here)…
      obj/<urlhash12>/<commit12>/<pkgslug>/<toolchain>/<deps12>/<route>.mojoc
    modules/<route> -> cache/src/<…>/<module root>
    mojo-cache/                              MODULAR_CACHE_DIR
    state.json                               resolved objects, fingerprints
  .out/                                       result of `bojo build`
```

- **Integrity is checked while fetching**, not forever: clone the pinned commit,
  compare the tree OID with `bojo.lock`. Afterwards the tree is intentionally
  writable — hooks and builds write there (D33).
- `<toolchain>` = fingerprint from `mojo --version` (`1.2.0-2016d0cd`), not the
  display name (D35).
- `<deps12>` = 12 hex over the **canonically sorted** list of
  `<urlhash12>@<commit12>` of the transitive dependencies (D23).
- `<pkgslug>` = pkg name lowercased, `[a-z0-9-]`; a collision is a conflict.
- `<route>` is the import name **and the file name of the `.mojoc`** — per
  `mojo precompile`, the file name determines the import name. An alias
  therefore produces a second artifact of the same source (D8/D37).

`.bojo/` is gitignored. `bojo clean` removes it.

Cost of this model, accepted on purpose (D32): every dependency exists once per
project, not once per machine; no shared build cache; `install`/`run` cache
nothing at all (§11a).

## 9. Resolution

| # | Rule |
|---|---|
| R1 | **One route → exactly one pkg.** Checked on the final (post-alias) route name, case-folded, and against project-local top-level directories. |
| R2 | `alias` only for a pkg with **exactly one** route that starts with the pkg name; the alias replaces the first segment. |
| R3 | `in-repo` deps inherit the commit of the repo. |
| R4 | A cycle in the pkg graph is an error with a path. |
| R5 | Two pkg names colliding inside one repo tree are an error. |
| R6 | Determinism: conflicts are errors, never overwrites; import order is irrelevant. |
| R7 | `alias` is allowed; it produces a second `.mojoc` under the route name, because the file name determines the import name (D8/D37). |

## 10. Module root and `-I`

```
project/.bojo/modules/
  sum      ->  cache/src/<hash>/<commit12>/lib/addition
  web_http ->  cache/src/<hash>/<commit12>/lib/web_http
```

- One `-I` on `.bojo/modules`, plus the project root for project-local packages
  (D27).
- The module root is swapped via a pointer symlink plus rename, or a temp dir plus
  `rename` where the platform allows an atomic directory exchange (D26a).
- The consumer's Mojo source stays unchanged: `from sum import add` or
  `from addition import add`, depending on the alias. No enforced `akku.` prefix
  (D13/D14).

## 11. Build pipeline

| # | Step | Failure |
|---|---|---|
| 1 | Load and validate `bojo.toml` | exit 1 |
| 2 | Resolve; fetch missing repos into `.bojo/cache/mojo/src`, verify the tree OID | exit 4 |
| 3 | `--locked`: compare lock against resolution | exit 5 |
| 4 | Take the project lock; swap `.bojo/modules` | exit 1 |
| 5 | Check hooks (§12). Missing approval → **abort, execute nothing** | exit 2 |
| 6 | Run pre-build hooks (topological, `cwd` = package dir, may write) | exit 2/3 |
| 7 | Precompile into `cache/obj` — **not** for pkgs with a `pre-build` hook (D40) | exit 3 |
| 8 | Collect linker flags from every pkg (`[linker]`) | — |
| 9 | `mojo build <entry> -I .bojo/modules -I . <flags>` with the toolchain from `[version] mojo` (D43/D47) | exit 3 |
| 10 | Post-build: `strip`, closure into `.out/lib`, relative RPATH at link time, re-sign on macOS | exit 3 |

`MODULAR_CACHE_DIR` = `.bojo/mojo-cache` (D15). Step 10 per platform:

| | Linux | macOS |
|---|---|---|
| Closure | `ldd` | `otool -L` |
| Path | `-Xlinker -rpath -Xlinker '$ORIGIN/lib'` | `install_name_tool -change … @loader_path/lib/…` |
| After modifying the Mach-O | — | `codesign -s - --force` |

### 11a. `bojo run` and `bojo install` — ephemeral (D34a)

Both build **outside** the project cache, in a temporary directory
(`os.MkdirTemp`, e.g. under `$TMPDIR`):

```
tmp/bojo-<rand>/
  src/<commit12>/      clone at the pinned commit
  obj/                 precompile
  .out/<cmd>            binary
```

Flow: resolve → clone (verify tree) → hook gate → build → for `install` copy the
binary to `~/.bojo/bin/<name>`, for `run` execute it → remove the temp directory.

- No cache, no reuse: every `install`/`run` compiles from scratch (D34a).
- `$BOJO_BIN` overrides `~/.bojo/bin`.
- If the temp filesystem is `noexec`, the run aborts with a clear message;
  `--build-dir <path>` allows an exec-capable location.
- A name collision in `~/.bojo/bin` is an error; `--force` overwrites (D30).

## 12. Hooks and trust

### 12.1 Shape

```toml
[pkg.web_templ.hooks]
pre-build  = [["templ", "generate"]]
post-build = [["cargo", "build", "--release"]]
```

- Array of argv arrays. No shell, no file (D16a). `cwd` = package directory,
  `BOJO_*` set, exit ≠ 0 aborts.
- Hooks **may** write into the package directory — that is intended (D33). The
  integrity guarantee applies at fetch time, not to the working tree.

### 12.2 Approval

No built-in trust (D16). `cargo`, `cmake`, `meson`, `make` are treated exactly
like any other command.

```
blocked: pkg 'web_templ' wants to run a hook before build
  repo     https://github.com/LukasLow/example_pkg
  commit   9f3a2b1c4d5e
  pkg      web_templ
  hook     pre-build
  command  templ generate
  binary   /usr/local/bin/templ

Not run. Inspect the repo and the command above, then approve with:

  bojo allow 3c1d9e

Blocked by 1 hook. Nothing was executed.
```

- ID = hash over (repo, commit, pkg, hook, command array), must be unambiguous.
- `bojo allow --json '{…}'` for agents; `bojo allow --list` shows approvals and
  marks stale ones.
- **No `-y`, no "allow all", no Enter-to-confirm.**
- A line is only valid if (repo, commit) is in the current lock (D28).
- Own hooks (`[hooks]` in the project's own `bojo.toml`) need no approval but are
  printed on every build (D12).
- Path deps have no commit: directory hash, recomputed on every build; symlink
  targets are hashed, not followed (D17).

### 12.3 Residual risks

- **No sandbox in v0.1.** The only protection is the explicit approval.
- **Hook chains are invisible:** `["cargo","build"]` runs `build.rs`,
  `["cmake",…]` runs CMake code. The allow line shows only the outer command
  (D29).
- **Committed allow lines can pre-approve.** Mitigations: binding to the lock
  (D28), one log line per executed foreign hook, `--strict-hooks`.
- **`PATH` changes:** the command string is approved, not the resolved binary.
  `bojo allow` records the resolved path in the line (D39).

### 12.4 Phase `vendor` (D84)

A dependency declares at most one phase, `vendor`. It runs **once**, when the
source is materialised — `bojo vendor`, `bojo update`, `bojo get` and
`bojo run`/`install` — never on `bojo build`.

| Property | Value |
|---|---|
| Working directory | the **store checkout** (`.bojo/cache/mojo/src/…`), never `.vendor` |
| Output | belongs in `.bojo/` (e.g. `.bojo/native/`), not in the vendored source |
| Approval | the same gate as before: unapproved → **exit 2, nothing runs** |
| Log | `bojo.allow` is committed, so CI runs the same hooks without a prompt |

Consequences:

- `.vendor` stays pure source, so a committed or cached `.vendor` cannot carry a
  macOS `.dylib` into a Linux CI run.
- A build is pure: no compiler runs on it, and `bojo build` is reproducible from
  the lock plus `.bojo/`.
- `.bojo/` is not committed, so CI starts with an empty cache and builds in its
  own platform. There is no "it thinks it already has something".

A project's own hooks (`[hooks]` in `bojo.toml`) keep all phases `vendor`,
`pre-build`, `post-build`; they need no approval (D12).

## 13. CLI

| Command | Purpose |
|---|---|
| `bojo init` / `bojo new` | create a project |
| `bojo deps list` | show Mojo/Python versions, repositories and Python packages (D50) |
| `bojo deps add <url>@<ver> [--as <name>]` | add a repository dependency (D83) |
| `bojo deps add-python <name>[@<ver>]` | add a Python package to `[deps.python]` |
| `bojo deps check` | newer versions and CVEs (POST-V0.1, P21) |
| `bojo remove <url>` / `bojo update [url]` | remove / re-resolve |
| `bojo build [--locked]` | build into `.out/` |
| `bojo run <url>@<ver>` | ephemeral build and run the repository's `[cmd]` (§11a) |
| `bojo install <url>#<pkg>@<ver>` | ephemeral build, binary to `~/.bojo/bin` (§11a) |
| `bojo test` / `bojo fmt` / `bojo doc` | delegate to the driver |
| `bojo tree` / `bojo why <pkg>` | graph / provenance |
| `bojo verify` | check the lock against a fresh checkout (never the working tree) |
| `bojo doctor` | mojo version + fingerprint, C compiler, triple, PATH, `~/.bojo/bin` |
| `bojo toolchain mojo install <ver>` | install a Mojo toolchain into `~/.bojo/mojo/<ver>` (D43) |
| `bojo toolchain mojo list` | list installed Mojo toolchains |
| `bojo toolchain python install <ver>` | install a relocatable CPython into `~/.bojo/python/<ver>` (D44) |
| `bojo toolchain python list` | list installed Python runtimes |
| `bojo toolchain remove <mojo\|python> <ver>` | delete an installed toolchain |
| `bojo cache list` / `bojo cache clear` | show / clear the project cache |
| `bojo allow <id>\|--json\|--list` | approve / show hooks |
| `bojo clean [--all]` | remove `.bojo/` (`--all`: also `.out/` and `.vendor/`) |
| `bojo skill [<runtime>]` | print the agent skill as markdown (§19) |

Exit codes:

| Code | Meaning |
|---|---|
| 0 | success |
| 1 | usage or configuration error |
| 2 | blocked by a missing hook approval — nothing was executed |
| 3 | build or link failure |
| 4 | network, clone or checkout failure |
| 5 | lock conflict (`--locked`) |

## 14. Output modes and platforms

| Mode | Description | Size / start (Linux, measured) |
|---|---|---|
| `dir` (default) | `.out/` with binary + `lib/`, relative RPATH | 5.29 MB / 4–5 ms |
| `onefile` | one self-extracting file (C stub + `app.bin` + `lib/*`) | measured: 4.28 MB for the hello-world demo, start ~5 ms after first extraction |
| `static` | does not exist (D18) | — |

| | Linux | macOS | Windows |
|---|---|---|---|
| Libs | `.so` | `.dylib` | `.dll` |
| Relative path | `$ORIGIN/lib` | `@loader_path/lib` | next to the `.exe` |
| Stub finds itself | `/proc/self/exe` | `_NSGetExecutablePath` | `GetModuleFileNameW` |

- v0.1: macOS (primary) and Linux (CI). Windows not (D19).
- The stub **execs** the extracted binary; extracted libraries are re-signed ad
  hoc when needed (D31). Extraction goes to `os.UserCacheDir()/bojo/run/<hash>/`,
  not `/tmp` (D21).
- **Signing is not bojo's job** (D20). Distribution is the app author's
  responsibility; bojo only reports.

## 15. Output conventions

- One fact per line, no spinners, no mandatory colour.
- Errors are complete sentences and name the next command.
- `--json` for every data-producing command.
- Progress only for network and build, as lines.

## 16. Decisions

| # | Decision | Rejected alternative | Reason |
|---|---|---|---|
| D1 | Go | Rust, Mojo | `os.UserCacheDir`, `os/exec` argv semantics, static binary; **the resolver is built by hand** |
| D2 | TOML | YAML | indentation is not semantics, explicit types, Cargo/uv parity |
| D3 | `bojo.toml`, `bojo.pkg.toml`, `bojo.lock`, `bojo.allow` | `bojo.yml` + `bojo_libs.yml` | clear roles |
| D4 | `#` separates repo and pkg | guessing paths, an index file | depth stays arbitrary |
| D5 | `import` uses directory paths, fixed file name | file paths, separate repo document | one file per directory |
| D5a | import graph is transitive, conflicts are errors | "last one wins" | determinism |
| D5b | file diamonds are allowed (dedupe before the name check) | diamond is an error | legal structure |
| D6 | tag `pkg/vX.Y.Z` | `vX.Y.Z` per repo | the name is in the manifest |
| D7 | `alias` inside the dep block | global `[alias]` table | with two same-named deps it is otherwise ambiguous |
| D8 | every pkg is precompiled; the **`.mojoc` file name** is the import name | a `precompiled` flag, min/max hardening | `mojo precompile` documents the file name as the import name |
| D9 | **removed** — the cache is project-local (D32) | `os.UserCacheDir` | see D32 |
| D10 | `cache/src/<urlhash12>/<commit12>` | `src/<host>/<owner>/<repo>` | depth, case, special characters |
| D11 | one version per route | Cargo-style coexistence | Mojo imports are paths |
| D12 | own hooks without approval, but printed | lock your own hooks too | the user wrote that file |
| D13 | import route is free (`modules`) | enforce an `akku.` prefix | the consumer imports what they chose |
| D13a | **removed** — no tag in the path (D32) | `tagslug` | the tag is a label, the commit is the identity |
| D14 | the `akku.` prefix is gone | enforce it | see D13 |
| D14a | URLs are parsed and normalised per component | sequential string replacement | `git@` would otherwise eat the scp case |
| D15 | `MODULAR_CACHE_DIR` per project | global | otherwise `~/.modular` grows unbounded |
| D16 | no built-in trust for any build system | privilege cargo/cmake | foreign code brings its own hooks |
| D16a | hooks are argv arrays, no shell | shell string, `.sh` | cross-platform, no quoting |
| D17 | path deps: directory hash, recomputed per build, symlink targets hashed | store the hash once | TOCTOU |
| D18 | no `static` mode | "later" | upstream #1317 is open |
| D19 | v0.1 targets macOS + Linux | Linux first | the development machine is macOS |
| D20 | no signing by bojo | notarise | `run`/`install` build locally |
| D21 | onefile unpacks to `UserCacheDir` | `/tmp` | `/tmp` is cleaned at runtime too |
| D22 | no per-file cache granularity | hash every file | milliseconds against undebuggability |
| D23 | the `obj` key includes the dep closure, canonically sorted | own commit only | stale artifacts after a dep update |
| D24 | no `flags12` dimension | flags in the key | `precompile` has no `-O/-g/-D` |
| D25 | never store credentials | pass userinfo through | secret leak |
| D26 | every cache write is atomic (temp + `rename`) | write in place | an aborted run poisons the cache |
| D26a | module root via pointer symlink and swap | directory `rename` | directory `rename` is not atomic |
| D27 | a second `-I` on the project root | `.bojo/modules` only | project-local packages stay importable |
| D28 | an allow line is only valid if the commit is in the lock | the line always counts | pre-approval via pull request |
| D29 | hook chains are a documented residual risk | pretend to sandbox | v0.1 cannot confine processes |
| D30 | `install` aborts on a name collision | silently overwrite | traceability |
| D31 | the stub **execs**, does not `dlopen` | `dlopen` | `@loader_path` is relative to the binary |
| D32 | **cache is project-local under `.bojo/cache/`** | global object cache | no GC, no reference registry, no cross-project races; cost: N copies |
| D33 | **hooks and builds may write into the working tree** | read-only plus overlay | wanted; integrity is a fetch-time property |
| D34 | core stays Mojo-only, `Driver` is an internal Go struct | public driver API | an interface without a second implementer is an untested promise |
| D34a | `run`/`install` build ephemerally in `os.MkdirTemp` | own cache | saves cache format, GC and state |
| D35 | toolchain identity = fingerprint from `mojo --version` | display name `1.2.0` | two builds of the same version are incompatible |
| D36 | `bojo verify` checks against a fresh checkout | check the working tree | the tree is mutated by hooks |
| D37 | `alias` produces a second `.mojoc`; route is part of the `obj` key | forbid alias | keeps alias available for name collisions |
| D38 | `triple` is not part of the `.mojoc` key | triple in the key | `.mojoc` is architecture-independent |
| D39 | the allow line records the resolved binary path | command string only | makes `PATH` changes visible |
| D40 | pkgs with a `pre-build` hook are **not** artifact-cached | hash the hook output | foreign tool versions would create stale `.mojoc` |
| D41 | `bojo skill` prints the skill as markdown on stdout | a separate `bojo docs` command | `bojo skill > …/SKILL.md` is one step, not a build |
| D42 | `[[deps-from]]` is an array of tables with a `repo` field | `[deps-from."<url>"]` | tags are per pkg; one table per repo allows only one version |
| **D43** | toolchains live machine-wide in `~/.bojo/mojo/<ver>/` and `~/.bojo/python/<ver>/`, installed via `bojo toolchain install <family> <ver>`; `[version] mojo` selects one | per-project toolchain, manual install | Mojo ships as a glibc-only Python package; a venv pins it per version and a build needs no Python |
| **D44** | bundling uses a relocatable CPython (`python-build-standalone`) plus wheels via `pip --target` | ship a conda/pixi env | conda envs bake absolute prefixes into scripts, `.pth` files and native RPATHs; relocation is guesswork |
| **D45** | a launcher stub sets `PYTHONHOME`/`PYTHONPATH` and execs the payload (`app.bin`) | require the user to set the variables | a binary cannot set its own environment; `dir` and `onefile` share one mechanism |
| **D46** | `[python]` is opt-in; without it the target must provide Python 3.10–3.14 | always bundle | a Python-capable bundle is roughly ten times larger; pure Mojo programs stay at 5 MB |
| **D47** | one `[version]` block (`mojo`, optional `python`) in `bojo.toml` and in every package; **no min/max ranges** | `min`/`max` compatibility windows | a range is a guess about the future; one declared version is a fact |
| **D48** | Python packages are wheels only; `.pth` files are scanned; an sdist needs an explicit approval | plain `pip install` | wheels can still execute code at start-up via `.pth`, and sdists run a build backend |
| **D49** | toolchains are namespaced: `bojo toolchain mojo …`, `bojo toolchain python …` | one flat `toolchain install` | two runtimes with independent versions and stores |
| **D50** | dependencies live under `bojo deps …` (`list`, `add`, `add-python`, `check`) | a top-level `get` | one namespace for everything that changes the manifest |
| **D51** | the launcher sets `MOJO_PYTHON_LIBRARY` (absolute `libpython` path) in addition to `PYTHONHOME`/`PYTHONPATH` | rely on `PATH` discovery | research shows `MOJO_PYTHON_LIBRARY` is the primary lookup; a wrong `LIBDIR` ends in `dlopen("")` |
| **D52** | the runtime closure is copied to `.out/lib`, and the relative RPATH (`$ORIGIN/lib`, `@loader_path/lib`) is passed **at link time** | rewrite the RPATH afterwards | Mojo only passes `-Xlinker`; a relative entry can be appended, a `patchelf` step would be an extra dependency |
| **D53** | `onefile` compiles a **C stub at build time** with the host `cc` and appends `app.bin` + `lib/*` plus a 24-byte trailer | embed a prebuilt stub, or require Go at runtime | Mojo already needs a C driver, so no new dependency; the stub sets `LD_LIBRARY_PATH`/`DYLD_LIBRARY_PATH` and execs |
| **D57** | a project may declare `[linker] libdirs`/`libs` and `[build] include`; the dirs are passed as `-L` **and** `-rpath` so the build-time loader finds them, and `Bundle()` copies the result into `.out/lib` | compile C code inside a package manifest | the consumer compiles (`pre-build` hook), the package only ships source; a package must never run a compiler on someone else's machine |
| **D59** | a package declares native sources as `[pkg.<n>.linker] ffi = [...]` and runtime data as `runtime = [...]` | one `runtime` key for both | `ffi` is input to the *build* (sources compiled by the consumer), `runtime` is input to the *bundle* (files that must sit next to the binary) |
| **D58** | a `.mojoc` keeps unresolved FFI symbols; measured: precompiled package with `external_call`, imported and linked by a consumer | resolve FFI at precompile time | the format carries non-elaborated code, so the symbol is bound at the consumer's final link |
| **D64** | no precompiled artefacts: `.mojoc` is dropped | precompile as a cache layer | measured: 640 ms vs 643 ms on 40 functions, 675 ms vs 656 ms on 400; precompiling costs 742 ms, and the consumer re-resolves imports anyway (proved: a package precompiled against `socket` A links against `socket` B) |
| **D70** | every path element uses the **short** hash: 8 hex characters (cache `repo@<commit8>`, vendor `<route>_<hash8>`); the lock always carries the **full** 40-character commit | one length everywhere | a short form keeps paths readable and stays far below the collision risk; the lock is the authoritative record, so it must be exact |
| **D65** | a package's import path is `<host>.<owner>.<repo>.<package>` and nothing else; the directory inside the repository is irrelevant | include the repo-internal path (`…​.lib.cprint`) | the package name is declared in `bojo.pkg.toml`, so the import path needs no hint about where the author keeps it |
| **D66** | the vendor tree is flat: one directory per **module path** under `.vendor/`; no nesting, no hashes, no rewriting | nested vendor per package | one path ⇒ one copy, so a shared `sqlite` lands in the binary once; nesting duplicated it per dependent |
| **D67** | manifests are mandatory: `bojo.repo.toml` at the repository root, `bojo.pkg.toml` per package | infer everything from the directory tree | without them there is no `ffi`, `runtime`, `linker`, `exclude` and no declared package name — the tool would be a worse `-I` |
| **D68** | an alias may only be declared in the user's `bojo.toml`; bojo generates `.vendor/<alias>/__init__.mojo` re-exporting the full path | allow aliases inside libraries | a library that renames its dependencies reintroduces the collision the full path just removed |
| **D69** | two different commits for the same module path are an error naming both chains; no MVS | highest wins | a silent upgrade is exactly what the user rejected |
| **D73** | `[pkg.<n>] exclude = [...]` filters files between store and vendor (`git archive <commit>:<subdir>`) | copy everything | a library should not ship its tests and docs into every consumer |
| **D81** | dependencies are grouped by repository: `[deps-from."<url>"]` with `package = "version"` or `package = { version = "...", as = "alias" }` | repeated `[[deps-from]]` blocks | one repository appears once, every fact is written once, and the alias sits in the value instead of a separate table (D68) |
| **D79** | a tag names its package: `<pkg>@vX.Y.Z`, e.g. `request@v0.0.1`. Git allows `@` in a ref (only `@{` is reserved), so the separator can be the same one the CLI uses | repo-wide `vX.Y.Z` (D74) | several packages live in one repository at independent versions; the path is per package, so the tag must be too |
| **D80** | `bojo get <url>#<pkg>@<version>` selects the package and the version; the `#` keeps the URL unambiguous, since URLs may themselves contain `@` | `url@version --lib pkg` | the URL is the address, the fragment is the selection — the same split the old CLI had, and it survives userinfo and ports |
| **D75** | a package may declare `readme = "README.md"` and `docs = "docs"`; both are optional | one field for both | a README is a greeting you read once, `docs/` is a tree you browse; `bojo docs` needs a root, `bojo get` needs a single file |
| **D76** | `bojo docs <lib> [path]` renders markdown from the vendored copy in the terminal; no path means `docs/index.md`, otherwise the path is resolved inside `docs/` | open the repo in a browser | the docs belong to the dependency you are already building against; leaving the terminal to read them breaks the loop |
| **D77** | `bojo get` and `bojo install` print the package README once, unless `--no-readme` is given | store it silently | the first thing a user wants after adding a dependency is what it does; printing is cheaper than a second command |
| **D78** | the index exposes `hooks`, `readme` and `docs` so the web page can list them; README markdown is fetched from the raw endpoint and sanitised in the browser | render server-side | the index has no server; sanitising in the browser keeps a hostile repo from injecting script into the page |
| **D61** | a package declares **one exact version**. `[repo.mojo] min`/`max` and every other range are removed | ranges for the toolchain version | Mojo has no compatibility promise between versions; a range would be a guess. One version is a fact, and `bojo doctor` can name the mismatch exactly |
| **D62** | every manifest starts with `schema = 1` | no schema marker | a manifest is read by tools that will outlive this format; without a marker every future change is guesswork |
| **D63** | the store path is `<host>/<owner>/<repo>@<tag>-<commit>` | `<hash>/<commit>`, or a `.git`-suffixed name | human-readable, unambiguous, and `.git` is not a legal delimiter: GitHub/GitLab strip it from URLs, so a repository effectively cannot carry it |
| **D55** | the cache is split into `.bojo/cache/mojo/` (git checkouts, module root, compiler cache) and `.bojo/cache/python/<version>/<key>/` (interpreter + site-packages, key = hash of the sorted package set) | one flat cache directory | each language keeps its own invalidation rule; the python key is independent of commit and flags |
| **D56** | `onefile` carries the python environment, and the stub sets `PYTHONHOME`, `PYTHONPATH` and `MOJO_PYTHON_LIBRARY` after extraction | bundle python only in `dir` mode | otherwise a self-contained binary that uses numpy is impossible; measured 205 MB extracted |
| **D54** | with `[version] python`, the build emits `.out/<name>` (C launcher) + `.out/<name>.bin` + `.out/python/{runtime,site-packages}` | symlink to the toolchain store | the launcher sets `PYTHONHOME`, `PYTHONPATH`, `MOJO_PYTHON_LIBRARY`; the store must not be required at runtime |
| **D82** | the first segment of an import path is a short host alias from a closed table (`github.com` → `gh`, `gitlab.com` → `gl`, `codeberg.org` → `cb`, `git.sr.ht` → `sr`); an unknown host keeps its literal name | keep the full host (`github.com.…`) | the host is already unique, but two dot-separated segments cost a directory level in `.vendor` and one segment in every `from` line; a closed table stays reviewable and cannot invent a colliding abbreviation |
| **D83** | a repository is its package: at most one library (`[pkg]`, `__init__.mojo` at the root) and at most one command (`[cmd]`, default `cmd/main.mojo`); the package name is derived from the repository name (`mojo-tls` → `mojo_tls`); tags are repository-wide `vX.Y.Z`; `get` takes the library, `run`/`install` take the command; commands have no module path | keep several packages per repository with an `import` list and per-package tags (`<pkg>/vX.Y.Z`) | the identity becomes derivable instead of declared, which removes `import`, the in-repo closure, cycle and diamond handling, the tag grammar and the `#<pkg>` selection — and it matches how Mojo libraries are actually published |
| **D84** | a dependency declares only the `vendor` hook phase; it runs once at vendoring time in the **store checkout**, is gated by `bojo.allow`, and never runs on `bojo build`; the project keeps `vendor`/`pre-build`/`post-build` | run dependency hooks on every build (they would recompile on each build and write into `.vendor`), or run them during `vendor --check` | `.vendor` must stay pure source so a cached tree cannot carry one platform's binary into another; `vendor --check` must not execute code; the committed `bojo.allow` lets CI run the same hooks unattended |
| **D87** | the extracted module root lives in `.bojo/cache/mojo/modules/` and `-I` points there; `.vendor/` keeps only `.vendor.json`, which records path, tag, commit and tree for `vendor --check` | keep the module root inside `.vendor/` | a build must never read from a directory that also receives build output: a vendor tree that a hook can write into is how one platform's artefact reaches another, and `.bojo/` is the one place that is regenerated and never committed |
| **D86** | a toolchain pinned in `[version]` (`mojo`, `python`) but not installed is fetched automatically on first use, after printing exactly what it installs; the explicit `bojo toolchain install` remains | fail with a "not installed" error and a hint | the version lives in `bojo.toml`, so a fresh checkout or a CI runner needs no second command; an implicit fetch that announces itself is cheaper than a failed build, and `.bojo/` stays the only place it lands |
| **D85** | a local dependency is a directory (`[deps.<label>] path = "…"`); its import path comes from its own `[repo] url`, it is never pinned in `bojo.lock`, and it may not declare hooks | a synthetic `local.<name>` path, or pinning a directory hash | the identity must survive the move to a published repository: a library developed locally and later pushed keeps its import path, and an unpinned tree has nothing to approve a hook against |

## 21. Runtime requirements (verified)

Research summary, sources in the commit message. What a `mojo build` artifact
needs on the target:

1. **Always** `libKGENCompilerRTShared.so` (plus `libMSupportGlobals.so`,
   `libAsyncRTRuntimeGlobals.so`, `libstdc++`, `libgcc_s`). The compiler links the
   runtime into every binary, including `def main(): pass`.
2. The loader finds them through an **absolute RUNPATH** into the build
   environment. `MODULAR_*` variables are compiler settings and are never read by
   `ld.so`; only `MODULAR_NVPTX_*` matters at runtime for GPU.
3. The target does **not** need the toolchain, but it needs the runtime libraries
   of the matching Mojo version.
4. **Python is `dlopen`ed, not linked.** Lookup order: `MOJO_PYTHON_LIBRARY`, then
   the first Python on `PATH` and its `LIBDIR`. A wrong `LIBDIR` ends in
   `dlopen("")` and a confusing abort inside the KGEN runtime.
5. Supported Python: **3.10–3.14** (3.15 fails, conda-forge split `libpython`).
6. GPU is `dlopen`-only (`libcuda.so.1`, `libamdhip64.so`, Metal); PTX is
   assembled at runtime.
7. macOS: `LC_RPATH` + `@rpath/…`, `install_name_tool`, then `codesign -s -`;
   `MACOSX_DEPLOYMENT_TARGET` decides the minimum OS in `LC_BUILD_VERSION`.
8. Measured glibc floor for the produced binary: 2.35.

Consequences already reflected in the spec: relative RPATH in step 10, the
launcher stub (D45/D51), and the `[python]` bundling rules (§20).

## 17. Open questions

1. Sandbox for hooks: own concept or platform facilities? → P5.
2. Does v0.1 need `bojo toolchain`? Without pinning the `obj` cache is worthless
   when the toolchain changes. → P1.
3. `bojo test` vs. an existing `Taskfile.yml`: respect, generate, replace? → P9.
4. Keep `akku` as a meta package? → P10.
5. Shared cache as an opt-in (`--shared-cache`) later? → P17.

## 18. Measurement log

Debian bookworm, aarch64, Mojo 1.2.0 (2016d0cd), gcc 12.

| Measurement | Value |
|---|---|
| `hello.mojo` via `mojo build` | 72,512 B, PIE, aarch64 ELF |
| NEEDED of the app | `libKGENCompilerRTShared.so`, `libc.so.6` |
| RUNPATH | `/root/.pixi/envs/mojo/lib` (absolute) |
| transitive | `libMSupportGlobals.so`, `libAsyncRTRuntimeGlobals.so`, `libstdc++.so.6`, `libgcc_s.so.1`, `libm`, `libdl` |
| Mojo runtime total | 1,239,944 + 53,528 + 669,224 = 1.96 MB |
| `libstdc++.so.6` | 23.6 MB → `strip --strip-unneeded` 2,698,712 B |
| single file, complete | 26,433,696 B, start 10 ms |
| single file, stripped | 5.29 MB, start 4–5 ms |
| bare binary | 0.07 MB, start 2 ms |
| `-Xlinker -static` | fails (`cannot find -lgcc_s`) |
| `-Xlinker --static-libstdc++` | `ld: unable to disambiguate` |
| `libstdc++.a` in the env | absent |
| NEEDED of the runtime libs | each `libstdc++.so.6` + `libgcc_s.so.1` |
| `mojo build` without `cc` | `unable to find suitable c compiler for linking` |
| `-Xlinker -rpath -Xlinker '$ORIGIN/lib'` | RUNPATH is appended |
| stub with `LD_DEBUG=libs` | loads `libKGENCompilerRTShared.so` from the extraction directory |
| `mojo build --help-hidden` | contains `-strip-file-prefix <PATH>` (confirmed) |

Upstream: `modular/modular#1317`, open since 2023-11-21; the follow-up question
from 2026-09-14 is unanswered.

## 20. Python interop and bundling

Mojo has first-class Python interop, but neither the interpreter nor any Python
package is embedded: `mojo build --help` states that Python libraries used by a
project "are not included in the executable binary, so they must be provided by
the environment where you run the executable". A single-file artifact therefore
has to carry a Python runtime of its own or the target machine must provide one.

### 20.1 Two modes

| Mode | When | Consequence |
|---|---|---|
| **host Python** | `[python]` absent | interop works only where Python 3.10–3.14 (plus the used packages) is installed; `bojo doctor` names the exact requirement |
| **bundled Python** | `[python]` present | interpreter and packages ship with the artifact; no Python needed on the target |

```toml
[version]
python = "3.12"

[deps.python]
numpy  = "2.1.0"
orjson = "*"
```

### 20.2 Why a relocatable CPython, not conda/pixi

A conda/pixi environment is **not relocatable**: prefixes are baked into
`pyvenv.cfg`, shebangs, `.pth` files and RPATHs of native extensions. Shipping one
means shipping a path-rewriting tool and hoping every package tolerates it.

bojo therefore bundles a **python-build-standalone** interpreter (relocatable by
design, one tarball per triple) and installs wheels into it with
`pip install --target`. Wheels of native packages (numpy, orjson, …) are plain
`.so` plus Python files and survive relocation as long as the ABI matches the
bundled interpreter — which it does, because the interpreter *is* the one used to
install them.

### 20.3 Layout

`output = "dir"` with `[python]`:

```
.out/
  app            launcher stub: sets PYTHONHOME / PYTHONPATH, then execs app.bin
  app.bin        the Mojo binary
  lib/           Mojo runtime libraries (relative RPATH)
  python/        relocatable interpreter + site-packages
```

`output = "onefile"`: the same four pieces are appended to one stub, which
extracts them to `<cachedir>/bojo/run/<hash>/` and execs `app.bin` (D45).

### 20.4 Sizes (to be measured, not yet)

| Piece | Expectation |
|---|---|
| CPython standalone, stripped | ~25–35 MB compressed, ~60–80 MB unpacked |
| numpy wheel | ~20 MB unpacked |
| Mojo runtime + app | ~5.3 MB (measured, §18) |

A Python-capable `onefile` therefore lands in the **40–70 MB** range. This is the
honest price for "no Python on the target"; the numbers are estimates until the
first real measurement is in §18.

### 20.5 Rules

- The bundled interpreter version must match what the Mojo runtime expects; a
  mismatch is a hard error, not a warning (D44).
- `bojo build` warns when Mojo source imports `python`/`PythonObject` but
  `[version] python` is absent, and names the target requirement.
- `bojo doctor` reports: interpreter found, version, `libpython` path, whether
  the current project bundles one.
- Native wheels are copied as-is; no compiler is required on the target.
- Nothing about conda/pixi is used at runtime. `bojo bundle --python-env <path>`
  (best effort, path rewriting) is a POST-V0.1 escape hatch, not the default.

### 20.6 Wheels only, and what a wheel can still do

A wheel is installed, not executed — but two things can run code anyway:

1. **`.pth` files** are imported by `site` at interpreter start-up. They are the
   standard trick to execute code in every Python process of an environment.
2. **sdists** run an arbitrary build backend during `pip install` (equivalent to
   a hook, and therefore subject to the same trust rules).

Rules (D48):

- `pip install --only-binary=:all:` — an sdist is refused. A package that only
  ships an sdist needs an explicit approval line, the same shape as a hook.
- After installing, bojo scans every `.pth` file in the environment. A line that
  is not a plain path (i.e. `import …` / `exec(…)`) blocks the build and prints
  the file, the line and the package that owns it.
- The scan result is recorded in the lock (`python = "3.12"` plus a hash of the
  resolved package set), so a later build does not silently pick different
  wheels.

## 19. Agent skill (`bojo skill`)

bojo is mostly used by agents, so the CLI ships its own skill instead of
maintaining a second copy of the documentation.

```
bojo skill > .agents/skills/bojo/SKILL.md
```

- **Source of truth** is `skill/bojo.md` in the repository, embedded via
  `go:embed` (`skill/skill.go`). One file, two output paths.
- Output is plain Markdown on **stdout**, no colour, no progress, no epilogue.
  Exit 0, no network.
- `bojo skill` (no argument) is the full version. Runtime hint:
  `bojo skill claude`, `bojo skill codex`, `bojo skill opencode` — in v0.1 all
  three emit **identical output**; the arguments are reserved so runtime-specific
  notes can be added later without changing the command line (D41).
- The text may only describe what is implemented. If the CLI diverges, the skill
  is wrong, not the user.
- It states the limits explicitly (no static ELF, no embedded runtime, no
  automatic hooks) and the exit-code semantics, because agents act on them.

## 22. Store, vendor and manifests

Settled in D61–D69.

### 22.1 Import paths

```
gh.LukasLow.mojo_tls
```

Built from the repository URL alone: the repository name is the package name,
as a Mojo identifier (D83). Where the author keeps its modules below
`__init__.mojo` does not appear in the path.

Mojo splits on dots, so on disk the host becomes one segment (D82):

```
.vendor/gh/LukasLow/mojo_tls/__init__.mojo
```

Inside a library the import is always written in full. Only the end user may
shorten it, and only in `bojo.toml` — keyed by module path, so two repositories
with the same name stay distinguishable:

```toml
[alias]
"gh.nsalerni.mojo_tls" = "tls"
```

bojo then generates `.vendor/tls/__init__.mojo` with `from …mojo_tls import *`.

### 22.2 Store and vendor

The store keeps complete repositories, read-only, one checkout per commit:

```
.bojo/cache/mojo/src/github.com/LukasLow/bojo_demo_pkg@6a3efcc2/
```

The vendor tree is flat and holds exactly one directory per module path:

```
.vendor/
    .vendor.json          route -> repo, tag, commit, required_by
    .vendor.lock          copy of bojo.lock at vendoring time
    gh/LukasLow/mojo_tls/
    gh/nsalerni/mojo_net/
    gh/Anna/mojosocket/               <- and
    gh/Markus/mojosocket/             <- both, side by side
```

One module root: `mojo build … -I .vendor`.

Because the path is the identity, two providers of the same short name never
meet, and a package used by several dependents is compiled once.

### 22.3 Versions

One version per repository, and the repository is the package (D83). One tag
covers the library and the command:

```
v0.0.2
```

A breaking change gets a new path segment by convention (`mojo_tls.v2` or a
separate repository `mojo-tls-v2`); bojo does not enforce a scheme.

### 22.4 Manifests

| File | Where | Mandatory | Contains |
|---|---|---|---|
| `bojo.toml` | project | yes | project, entry, versions, hooks, linker, include, aliases |
| `bojo.pkg.toml` | repository root | yes | `[repo]`, at most one `[pkg]` and one `[cmd]`, `ffi`, `runtime`, `exclude`, deps |

All start with `schema = 1` (D62). Versions are exact (D61).

### 22.5 Shipping

A published package carries `bojo.pkg.toml` and `bojo.lock`; the lock records
repository, tag, full commit and tree for every dependency, so a consumer can
reproduce the exact tree and `vendor/.vendor.json` stays explainable.
