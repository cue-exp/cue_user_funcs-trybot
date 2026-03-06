# cue_user_funcs

A Go program that emulates `cue export`, extended with user-provided functions.
It dynamically discovers `@inject` attributes in CUE code, resolves the backing
Go functions from version-qualified package paths, generates a temporary Go
module with the right dependencies, builds it, and execs it.

This uses CUE's WIP user-provided functions and value injection proposals
([#4293](https://github.com/cue-lang/proposal/blob/main/designs/4293-user-functions-and-validators.md),
[#4294](https://github.com/cue-lang/proposal/blob/main/designs/4294-value-injection.md))
via a [fork](https://github.com/cue-exp/cue/tree/user_funcs_etc) of
`cuelang.org/go`.

## Usage

```
go run . export <directory>
```

The directory must contain a CUE package with `@extern(inject)` and `@inject`
attributes. The program:

1. Loads the CUE package and walks transitive imports to discover all `@inject`
   attributes.
2. Parses the version-qualified inject names (e.g.
   `golang.org/x/mod@v0.33.0/semver.IsValid`).
3. Generates a temporary Go module with the required dependencies and a
   reflection-based function registration.
4. Builds and execs the generated binary, which evaluates and exports the CUE
   as JSON.

## CUE packages

This module is also a CUE module (`github.com/cue-exp/cue_user_funcs`)
that provides reusable CUE packages:

### semver

Binds [`golang.org/x/mod/semver`](https://pkg.go.dev/golang.org/x/mod/semver)
functions: `#IsValid`, `#Compare`, `#Canonical`, `#Major`, `#MajorMinor`,
`#Prerelease`, `#Build`.

```cue
import "github.com/cue-exp/cue_user_funcs/semver"

valid: semver.#IsValid("v1.2.3")
```

### sprig

Provides [sprig](https://masterminds.github.io/sprig/)-compatible string
functions: `#Title`, `#Untitle`, `#Substr`, `#Nospace`, `#Trunc`, `#Abbrev`,
`#Abbrevboth`, `#Initials`, `#Wrap`, `#WrapWith`, `#Indent`, `#Nindent`,
`#Snakecase`, `#Camelcase`, `#Kebabcase`, `#Swapcase`, `#Plural`,
`#SemverCompare`, `#Semver`.

```cue
import "github.com/cue-exp/cue_user_funcs/sprig"

title: sprig.#Title("hello world")
```

## Inject name format

Inject names are version-qualified Go package paths:

```
module@version/subpath.FuncName
```

For example:
- `golang.org/x/mod@v0.33.0/semver.IsValid`
- `github.com/cue-exp/cue_user_funcs@v0.0.0-20260306160924-85e7d61cf247/sprig.Title`

## CUE package setup

CUE files that use injected functions directly must have `@extern(inject)` at
the file level and `@inject(name=...)` on fields:

```cue
@extern(inject)

package mypackage

#semverIsValid: _ @inject(name="golang.org/x/mod@v0.33.0/semver.IsValid")

result: #semverIsValid("v1.0.0")
```

Alternatively, import the provided CUE packages which handle the wiring:

```cue
package mypackage

import "github.com/cue-exp/cue_user_funcs/semver"

result: semver.#IsValid("v1.0.0")
```

## CI

CI configuration lives in `internal/ci/` as CUE source of truth, generating
`.github/workflows/trybot.yaml` via:

```
go generate ./internal/ci/...
```
