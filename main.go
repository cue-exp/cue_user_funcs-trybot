package main

import (
	"bytes"
	"embed"
	"fmt"
	goast "go/ast"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"text/template"

	"cuelang.org/go/cue/ast"
	"cuelang.org/go/cue/build"
	"cuelang.org/go/cue/load"
	"github.com/rogpeppe/go-internal/cache"
	"golang.org/x/tools/go/packages"
)

//go:embed _template/main.go
var templateFS embed.FS

func main() {
	os.Exit(main1())
}

func main1() int {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}
	return 0
}

var debug bool

func debugf(format string, args ...any) {
	if debug {
		fmt.Fprintf(os.Stderr, "debug: "+format+"\n", args...)
	}
}

func run() error {
	if len(os.Args) < 3 || os.Args[1] != "export" {
		return fmt.Errorf("usage: %s export [--shim] [--debug] <directory>", os.Args[0])
	}

	// Parse flags before the directory argument.
	args := os.Args[2:]
	showShim := false
	for len(args) > 0 && strings.HasPrefix(args[0], "--") {
		switch args[0] {
		case "--shim":
			showShim = true
		case "--debug":
			debug = true
		default:
			return fmt.Errorf("unknown flag: %s", args[0])
		}
		args = args[1:]
	}
	if len(args) == 0 {
		return fmt.Errorf("usage: %s export [--shim] [--debug] <directory>", os.Args[0])
	}
	dir := args[0]

	// Make dir absolute so the generated binary can find it.
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}

	// Load the CUE package to discover @inject attributes.
	cfg := &load.Config{Dir: absDir}
	instances := load.Instances([]string{"."}, cfg)
	if len(instances) == 0 {
		return fmt.Errorf("no instances found in %s", dir)
	}
	inst := instances[0]
	if inst.Err != nil {
		return inst.Err
	}

	injectNames := collectInjectNames(inst)
	if len(injectNames) == 0 {
		return fmt.Errorf("no @inject attributes found")
	}

	funcs, err := parseInjectNames(injectNames)
	if err != nil {
		return err
	}

	// Open the artifact cache.
	c, err := openCache()
	if err != nil {
		return err
	}

	// Compute cache keys from the sorted inject names.
	shimID := shimActionID(injectNames)
	binID := binaryActionID(shimID)

	// For --shim, try the shim cache first.
	if showShim {
		if shimBytes, _, err := c.GetBytes(shimID); err == nil {
			debugf("shim cache hit")
			_, err = os.Stdout.Write(shimBytes)
			return err
		}
		debugf("shim cache miss")
		regData, err := resolveRegisterData(funcs)
		if err != nil {
			return err
		}
		var buf bytes.Buffer
		if err := registerTmpl.Execute(&buf, regData); err != nil {
			return err
		}
		c.PutBytes(shimID, buf.Bytes())
		_, err = os.Stdout.Write(buf.Bytes())
		return err
	}

	// Try the binary cache.
	if binFile, _, err := c.GetFile(binID); err == nil {
		// The cache stores files without execute permission; fix before exec.
		if err := os.Chmod(binFile, 0o555); err == nil {
			debugf("binary cache hit")
			execArgs := []string{binFile, "export", absDir}
			return syscall.Exec(binFile, execArgs, os.Environ())
		}
	}
	debugf("binary cache miss")

	// Cache miss: resolve the shim (possibly from cache).
	shimBytes, _, shimErr := c.GetBytes(shimID)
	if shimErr != nil {
		debugf("shim cache miss")
		regData, err := resolveRegisterData(funcs)
		if err != nil {
			return err
		}
		var buf bytes.Buffer
		if err := registerTmpl.Execute(&buf, regData); err != nil {
			return err
		}
		shimBytes = buf.Bytes()
		c.PutBytes(shimID, shimBytes)
	} else {
		debugf("shim cache hit")
	}

	// Build the binary in a temporary directory.
	tmpDir, err := os.MkdirTemp("", "cue-user-funcs-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	// Write the template main.go.
	tmplMain, err := templateFS.ReadFile("_template/main.go")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "main.go"), tmplMain, 0o666); err != nil {
		return err
	}

	// Write go.mod.
	if err := writeGoMod(tmpDir, funcs); err != nil {
		return err
	}

	// Write the generated register.go.
	if err := os.WriteFile(filepath.Join(tmpDir, "register.go"), shimBytes, 0o666); err != nil {
		return err
	}

	// Run go mod tidy.
	if err := goCmd(tmpDir, "mod", "tidy"); err != nil {
		return fmt.Errorf("go mod tidy: %w", err)
	}

	// Build the generated module.
	binPath := filepath.Join(tmpDir, "cue-user-funcs")
	if err := goCmd(tmpDir, "build", "-o", binPath, "."); err != nil {
		return fmt.Errorf("go build: %w", err)
	}

	// Cache the built binary.
	if f, err := os.Open(binPath); err == nil {
		c.Put(binID, f)
		f.Close()
		debugf("binary cached")
	}

	// Exec the built binary, replacing the current process.
	execArgs := []string{binPath, "export", absDir}
	return syscall.Exec(binPath, execArgs, os.Environ())
}

// openCache opens the artifact cache in ~/.cache/cue-user-funcs.
func openCache() (*cache.Cache, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return nil, fmt.Errorf("cache dir: %w", err)
	}
	cacheDir := filepath.Join(dir, "cue-user-funcs")
	if err := os.MkdirAll(cacheDir, 0o777); err != nil {
		return nil, err
	}
	return cache.Open(cacheDir)
}

// shimActionID computes a cache key for the generated register.go shim.
// The shim is fully determined by the sorted inject names.
func shimActionID(injectNames []string) cache.ActionID {
	sorted := slices.Clone(injectNames)
	slices.Sort(sorted)
	h := cache.NewHash("shim")
	for _, name := range sorted {
		h.Write([]byte(name))
		h.Write([]byte{0})
	}
	return h.Sum()
}

// binaryActionID computes a cache key for the built binary.
// It mixes the shim key with the Go version, platform, and template content.
func binaryActionID(shimID cache.ActionID) cache.ActionID {
	tmplMain, _ := templateFS.ReadFile("_template/main.go")
	h := cache.NewHash("binary")
	h.Write(shimID[:])
	h.Write([]byte(runtime.Version()))
	h.Write([]byte{0})
	h.Write([]byte(runtime.GOOS))
	h.Write([]byte{0})
	h.Write([]byte(runtime.GOARCH))
	h.Write([]byte{0})
	h.Write(tmplMain)
	return h.Sum()
}

// resolveRegisterData downloads the referenced Go modules, parses function
// signatures, and returns the data needed to generate register.go.
func resolveRegisterData(funcs []funcRef) (*registerData, error) {
	// Create a temporary directory for module resolution.
	tmpDir, err := os.MkdirTemp("", "cue-user-funcs-resolve-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpDir)

	// Write go.mod and a stub file with blank imports so go mod tidy
	// downloads the function packages.
	if err := writeGoMod(tmpDir, funcs); err != nil {
		return nil, err
	}
	if err := writeStubFile(tmpDir, funcs); err != nil {
		return nil, err
	}
	if err := goCmd(tmpDir, "mod", "tidy"); err != nil {
		return nil, fmt.Errorf("go mod tidy: %w", err)
	}

	// Resolve function signatures from the downloaded Go source.
	sigs, err := resolveFuncSigs(tmpDir, funcs)
	if err != nil {
		return nil, err
	}

	return buildRegisterData(funcs, sigs)
}

// collectInjectNames walks the instance and its transitive imports,
// returning all @inject name values from files that have @extern(inject).
func collectInjectNames(root *build.Instance) []string {
	var names []string
	seen := map[string]bool{}
	var walk func(inst *build.Instance)
	walk = func(inst *build.Instance) {
		if seen[inst.ImportPath] {
			return
		}
		seen[inst.ImportPath] = true

		for _, f := range inst.Files {
			names = append(names, extractInjectNames(f)...)
		}
		for _, imp := range inst.Imports {
			walk(imp)
		}
	}
	walk(root)
	return names
}

// extractInjectNames returns all @inject name values from a file
// that has a file-level @extern(inject) attribute.
func extractInjectNames(f *ast.File) []string {
	// Check for file-level @extern(inject).
	hasExtern := false
	for _, d := range f.Decls {
		attr, ok := d.(*ast.Attribute)
		if !ok {
			continue
		}
		key, body := attr.Split()
		if key == "extern" && body == "inject" {
			hasExtern = true
			break
		}
	}
	if !hasExtern {
		return nil
	}

	// Collect @inject names from fields.
	var names []string
	for _, d := range f.Decls {
		field, ok := d.(*ast.Field)
		if !ok {
			continue
		}
		for _, a := range field.Attrs {
			key, body := a.Split()
			if key != "inject" {
				continue
			}
			name := parseInjectAttrName(body)
			if name != "" {
				names = append(names, name)
			}
		}
	}
	return names
}

// parseInjectAttrName extracts the name value from an @inject attribute body
// like `name="golang.org/x/mod@v0.33.0/semver.IsValid"`.
func parseInjectAttrName(body string) string {
	// Body is: name="value"
	prefix := `name="`
	if !strings.HasPrefix(body, prefix) {
		return ""
	}
	rest := body[len(prefix):]
	end := strings.Index(rest, `"`)
	if end < 0 {
		return ""
	}
	return rest[:end]
}

// funcRef represents a parsed inject name.
type funcRef struct {
	// InjectName is the full inject name, e.g. "golang.org/x/mod@v0.33.0/semver.IsValid".
	InjectName string
	// Module is the Go module path, e.g. "golang.org/x/mod".
	Module string
	// Version is the module version, e.g. "v0.33.0".
	Version string
	// ImportPath is the full Go import path, e.g. "golang.org/x/mod/semver".
	ImportPath string
	// FuncName is the function name, e.g. "IsValid".
	FuncName string
}

// injectNameRe matches "module@version/subpath.Func" or "module@version.Func" (no subpath).
var injectNameRe = regexp.MustCompile(`^(.+)@(v[^/]+?)(?:/(.+))?\.([A-Z]\w*)$`)

func parseInjectNames(names []string) ([]funcRef, error) {
	var funcs []funcRef
	for _, name := range names {
		m := injectNameRe.FindStringSubmatch(name)
		if m == nil {
			return nil, fmt.Errorf("cannot parse inject name %q", name)
		}
		module := m[1]
		version := m[2]
		subpath := m[3]
		funcName := m[4]

		importPath := module
		if subpath != "" {
			importPath = module + "/" + subpath
		}

		funcs = append(funcs, funcRef{
			InjectName: name,
			Module:     module,
			Version:    version,
			ImportPath: importPath,
			FuncName:   funcName,
		})
	}
	return funcs, nil
}

func writeGoMod(tmpDir string, funcs []funcRef) error {
	goMod := `module _cue_user_funcs_generated

go 1.25.0

require cuelang.org/go v0.16.0

replace cuelang.org/go v0.16.0 => github.com/cue-exp/cue v0.0.0-20260306105357-d03fc6701a45
`
	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte(goMod), 0o666); err != nil {
		return err
	}

	// Add a require for each unique module@version.
	seen := map[string]bool{}
	for _, f := range funcs {
		key := f.Module + "@" + f.Version
		if seen[key] {
			continue
		}
		seen[key] = true
		if err := goCmd(tmpDir, "mod", "edit", "-require="+key); err != nil {
			return fmt.Errorf("go mod edit -require=%s: %w", key, err)
		}
	}
	return nil
}

// writeStubFile writes a temporary Go file with blank imports for each
// function package, so that go mod tidy downloads the required modules.
func writeStubFile(tmpDir string, funcs []funcRef) error {
	var buf strings.Builder
	buf.WriteString("package main\n\nimport (\n")
	seen := map[string]bool{}
	for _, f := range funcs {
		if seen[f.ImportPath] {
			continue
		}
		seen[f.ImportPath] = true
		fmt.Fprintf(&buf, "\t_ %q\n", f.ImportPath)
	}
	buf.WriteString(")\n")
	return os.WriteFile(filepath.Join(tmpDir, "stub.go"), []byte(buf.String()), 0o666)
}

// funcSig holds the parsed signature of a Go function.
type funcSig struct {
	Params       []string // Go type names, e.g. ["string", "int"]
	ReturnType   string   // first return type name, e.g. "string", "bool", "*SemverVersion"
	ReturnsError bool     // true if the last return value is error
}

// resolveFuncSigs loads the Go packages for all inject functions using
// go/packages and extracts each function's signature from the parsed syntax.
func resolveFuncSigs(tmpDir string, funcs []funcRef) (map[string]*funcSig, error) {
	// Collect unique import paths.
	var patterns []string
	seen := map[string]bool{}
	for _, f := range funcs {
		if seen[f.ImportPath] {
			continue
		}
		seen[f.ImportPath] = true
		patterns = append(patterns, f.ImportPath)
	}

	// Load all packages in one call.
	cfg := &packages.Config{
		Mode: packages.NeedSyntax | packages.NeedName | packages.NeedFiles,
		Dir:  tmpDir,
	}
	pkgs, err := packages.Load(cfg, patterns...)
	if err != nil {
		return nil, fmt.Errorf("loading packages: %w", err)
	}

	// Index packages by import path.
	pkgByPath := map[string]*packages.Package{}
	for _, pkg := range pkgs {
		if len(pkg.Errors) > 0 {
			return nil, fmt.Errorf("package %s: %s", pkg.PkgPath, pkg.Errors[0])
		}
		pkgByPath[pkg.PkgPath] = pkg
	}

	// Extract each function's signature.
	sigs := map[string]*funcSig{}
	for _, f := range funcs {
		pkg, ok := pkgByPath[f.ImportPath]
		if !ok {
			return nil, fmt.Errorf("package %s not loaded", f.ImportPath)
		}
		sig, err := findFuncSig(pkg, f.FuncName)
		if err != nil {
			return nil, fmt.Errorf("resolving %s: %w", f.InjectName, err)
		}
		sigs[f.InjectName] = sig
	}
	return sigs, nil
}

// findFuncSig searches the parsed syntax of a loaded package for the named
// function and extracts its signature.
func findFuncSig(pkg *packages.Package, funcName string) (*funcSig, error) {
	for _, file := range pkg.Syntax {
		for _, decl := range file.Decls {
			fn, ok := decl.(*goast.FuncDecl)
			if !ok || fn.Name.Name != funcName || fn.Recv != nil {
				continue
			}
			sig := &funcSig{}
			for _, param := range fn.Type.Params.List {
				typeName, err := typeExprStr(param.Type)
				if err != nil {
					return nil, fmt.Errorf("function %s: %w", funcName, err)
				}
				// Handle grouped params like (a, b string).
				n := len(param.Names)
				if n == 0 {
					n = 1
				}
				for range n {
					sig.Params = append(sig.Params, typeName)
				}
			}
			if fn.Type.Results != nil {
				results := fn.Type.Results.List
				last := results[len(results)-1]
				if ident, ok := last.Type.(*goast.Ident); ok && ident.Name == "error" {
					sig.ReturnsError = true
				}
				retType, err := typeExprStr(results[0].Type)
				if err != nil {
					return nil, fmt.Errorf("function %s return type: %w", funcName, err)
				}
				sig.ReturnType = retType
			}
			return sig, nil
		}
	}
	return nil, fmt.Errorf("function %s not found in package %s", funcName, pkg.PkgPath)
}

// typeExprStr returns a Go source representation of an AST type expression.
// It handles identifiers (e.g. "string", "SemverVersion") and pointer types
// (e.g. "*SemverVersion").
func typeExprStr(expr goast.Expr) (string, error) {
	switch e := expr.(type) {
	case *goast.Ident:
		return e.Name, nil
	case *goast.StarExpr:
		inner, err := typeExprStr(e.X)
		if err != nil {
			return "", err
		}
		return "*" + inner, nil
	default:
		return "", fmt.Errorf("unsupported type expression %T", expr)
	}
}

// builtinTypes is the set of Go predeclared type names.
var builtinTypes = map[string]bool{
	"bool": true, "byte": true, "rune": true,
	"string": true, "error": true, "any": true,
	"int": true, "int8": true, "int16": true, "int32": true, "int64": true,
	"uint": true, "uint8": true, "uint16": true, "uint32": true, "uint64": true, "uintptr": true,
	"float32": true, "float64": true,
	"complex64": true, "complex128": true,
}

// qualifyType prefixes non-builtin type names with the import alias.
// For example, "*SemverVersion" with alias "pkg1" becomes "*pkg1.SemverVersion".
func qualifyType(typeStr, alias string) string {
	if strings.HasPrefix(typeStr, "*") {
		return "*" + qualifyType(typeStr[1:], alias)
	}
	if builtinTypes[typeStr] {
		return typeStr
	}
	return alias + "." + typeStr
}

var registerTmpl = template.Must(template.New("register").Parse(`package main

import (
	"cuelang.org/go/cue"
	"cuelang.org/go/cue/cuecontext"
{{- range .Imports}}
	{{.Alias}} "{{.Path}}"
{{- end}}
)

func registerAll(j *cuecontext.Injector) {
{{- range .Funcs}}
	j.Register("{{.InjectName}}", cue.PureFunc{{.Arity}}(func({{.ParamDecl}}) ({{.ReturnType}}, error) {
{{- if .ReturnsError}}
		return {{.CallExpr}}
{{- else}}
		return {{.CallExpr}}, nil
{{- end}}
	}, cue.Name("{{.InjectName}}")))
{{- end}}
}
`))

type registerData struct {
	Imports []importEntry
	Funcs   []registerFuncEntry
}

type importEntry struct {
	Alias string
	Path  string
}

type registerFuncEntry struct {
	InjectName   string
	Arity        int
	ParamDecl    string
	ReturnType   string
	CallExpr     string
	ReturnsError bool
}

func buildRegisterData(funcs []funcRef, sigs map[string]*funcSig) (*registerData, error) {
	// Assign unique import aliases.
	importAliases := map[string]string{} // importPath -> alias
	aliasCounter := 0
	for _, f := range funcs {
		if _, ok := importAliases[f.ImportPath]; ok {
			continue
		}
		aliasCounter++
		importAliases[f.ImportPath] = fmt.Sprintf("pkg%d", aliasCounter)
	}

	var imports []importEntry
	seen := map[string]bool{}
	for _, f := range funcs {
		if seen[f.ImportPath] {
			continue
		}
		seen[f.ImportPath] = true
		imports = append(imports, importEntry{
			Alias: importAliases[f.ImportPath],
			Path:  f.ImportPath,
		})
	}

	var entries []registerFuncEntry
	for _, f := range funcs {
		sig := sigs[f.InjectName]
		alias := importAliases[f.ImportPath]

		// Build typed parameter declaration, e.g. "a0 string, a1 int".
		paramParts := make([]string, len(sig.Params))
		argNames := make([]string, len(sig.Params))
		for i, paramType := range sig.Params {
			name := fmt.Sprintf("a%d", i)
			paramParts[i] = name + " " + paramType
			argNames[i] = name
		}
		paramDecl := strings.Join(paramParts, ", ")
		callExpr := fmt.Sprintf("%s.%s(%s)", alias, f.FuncName, strings.Join(argNames, ", "))

		entries = append(entries, registerFuncEntry{
			InjectName:   f.InjectName,
			Arity:        len(sig.Params),
			ParamDecl:    paramDecl,
			ReturnType:   qualifyType(sig.ReturnType, alias),
			CallExpr:     callExpr,
			ReturnsError: sig.ReturnsError,
		})
	}

	return &registerData{
		Imports: imports,
		Funcs:   entries,
	}, nil
}

func goCmd(dir string, args ...string) error {
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
