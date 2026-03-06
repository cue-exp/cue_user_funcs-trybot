# cue_user_funcs Contributor Guidelines

## Project overview

A Go program that emulates `cue export` with user-provided functions. It
dynamically discovers `@inject` attributes in CUE code, resolves backing Go
functions from version-qualified package paths, generates a temporary Go module,
builds it, and execs it. Uses a fork of `cuelang.org/go`
(`github.com/cue-exp/cue`, branch `user_funcs_etc`) that implements WIP
user-provided functions and value injection.

This module is also a CUE module providing reusable CUE packages (`semver`,
`sprig`) that bind Go functions via `@extern(inject)` / `@inject` attributes.

## Key workflows

### Build and run

```bash
# Run the export command
go run . export <directory>

# Run tests
go test ./...
```

### CI

CI configuration is CUE source of truth in `internal/ci/`, generating
`.github/workflows/trybot.yaml`:

```bash
go generate ./internal/ci/...
```

### Testing

Use `tmp/` (gitignored) for temporary artifacts. When creating temporary Go
programs in `tmp/`, each needs its own `go.mod` to prevent interference with
the main module's `./...` pattern matching.

CUE test data goes in `testdata/` as testscript txtar files.

### Shell commands

Always use `command cd` when changing directories in shell scripts, as plain
`cd` may be overridden by shell functions. For all other commands, use plain
names without the `command` prefix.

## Project structure

- `main.go` - Entry point. Discovers @inject attrs, generates temp Go module,
  builds and execs it.
- `_template/main.go` - Embedded template for the generated program with
  reflection-based function registration.
- `semver/semver.cue` - CUE package binding golang.org/x/mod/semver functions.
- `sprig/sprig.go` - Go implementations of sprig-compatible functions.
- `sprig/sprig.cue` - CUE package exposing sprig functions via @inject.
- `testdata/` - Testscript txtar files.
- `internal/ci/` - CUE source of truth for CI workflows.
- `.github/workflows/` - Generated CI workflow YAML (do not edit directly).
- `cue.mod/module.cue` - CUE module declaration.
- `go.mod` - Go module with replace directive pointing to the CUE fork.

## Key APIs used

- `cue.PureFunc1` / `cue.PureFunc2` - Wrap Go functions as CUE-callable
  functions.
- `cuecontext.NewInjector` - Creates an injector for `@extern(inject)` /
  `@inject` attributes.
- `cue/load.Instances` - Loads CUE packages from disk.
- `cue.Context.BuildInstance` - Builds a loaded instance into a CUE value.

## Bug-fix process

1. Read the complete issue including all comments.
2. Reproduce the bug.
3. Reduce to a minimal failing test.
4. Commit the reproduction test separately.
5. Fix the bug in a second commit.
6. Cross-check against the original report.
7. Run the full test suite (`go test ./...`).

## Rules

- Do not set `GONOSUMCHECK` or `GONOSUMDB` environment variables.
- Injected functions use hidden definitions (e.g. `#semverIsValid`) in CUE.
- Commit messages: subject line under 50 characters, body explaining the "why."
- Every commit must pass `go test ./...`.
- Do not add Co-Authored-By trailers to commit messages.
- Do not edit generated files in `.github/workflows/` directly; edit the CUE
  source in `internal/ci/` and run `go generate ./internal/ci/...`.
