package main

import (
	"embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"text/template"

	"cuelang.org/go/cue/ast"
	"cuelang.org/go/cue/build"
	"cuelang.org/go/cue/load"
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

func run() error {
	if len(os.Args) < 3 || os.Args[1] != "export" {
		return fmt.Errorf("usage: %s export <directory>", os.Args[0])
	}
	dir := os.Args[2]

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

	// Walk all instances (including transitive deps) to find @inject names.
	injectNames := collectInjectNames(inst)
	if len(injectNames) == 0 {
		return fmt.Errorf("no @inject attributes found")
	}

	// Parse inject names into module requirements and function references.
	funcs, err := parseInjectNames(injectNames)
	if err != nil {
		return err
	}

	// Create a temporary directory for the generated module.
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

	// Generate register.go with the function map.
	if err := writeRegisterFile(tmpDir, funcs); err != nil {
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

	// Exec the built binary, replacing the current process.
	args := []string{binPath, "export", absDir}
	return syscall.Exec(binPath, args, os.Environ())
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

var registerTmpl = template.Must(template.New("register").Parse(`package main

import (
{{- range .Imports}}
	{{.Alias}} "{{.Path}}"
{{- end}}
)

func init() {
	funcsToRegister = map[string]any{
{{- range .Funcs}}
		"{{.InjectName}}": {{.ImportAlias}}.{{.FuncName}},
{{- end}}
	}
}
`))

type registerData struct {
	Imports []importEntry
	Funcs   []registerEntry
}

type importEntry struct {
	Alias string
	Path  string
}

type registerEntry struct {
	InjectName  string
	ImportAlias string
	FuncName    string
}

func writeRegisterFile(tmpDir string, funcs []funcRef) error {
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

	var entries []registerEntry
	for _, f := range funcs {
		entries = append(entries, registerEntry{
			InjectName:  f.InjectName,
			ImportAlias: importAliases[f.ImportPath],
			FuncName:    f.FuncName,
		})
	}

	out, err := os.Create(filepath.Join(tmpDir, "register.go"))
	if err != nil {
		return err
	}
	defer out.Close()
	return registerTmpl.Execute(out, registerData{
		Imports: imports,
		Funcs:   entries,
	})
}

func goCmd(dir string, args ...string) error {
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
