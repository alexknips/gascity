package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// emptyBeadsScopeRootAllowedFuncs names the only cmd/gc test functions that
// may clear GC_BEADS_SCOPE_ROOT themselves. Each pins ambient city discovery to
// a throwaway city before it clears the scope root.
var emptyBeadsScopeRootAllowedFuncs = map[string]bool{
	"setUnscopedBeadsProviderForTest": true,
}

// TestCmdGCTestsClearBeadsScopeRootOnlyThroughIsolationHelpers forbids a raw
// Setenv("GC_BEADS_SCOPE_ROOT", "") or Unsetenv("GC_BEADS_SCOPE_ROOT") in cmd/gc
// tests (ga-bvv).
//
// An empty scope root makes scopedBeadsProviderOverride match every city. The
// test binary runs from the cmd/gc package directory, so a store resolved
// without an explicit city path falls back to findCity(cwd). From a worktree
// under a live city that walk reaches the operator's real city.toml, and the
// test opens and mutates the real store (ga-8iq). The isolation helpers pin
// GC_CITY to a throwaway city first, so the guard sends every clear through
// them.
func TestCmdGCTestsClearBeadsScopeRootOnlyThroughIsolationHelpers(t *testing.T) {
	pattern := filepath.Join(repoRootForLint(t), "cmd", "gc", "*_test.go")
	paths, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatalf("glob %s: %v", pattern, err)
	}
	if len(paths) == 0 {
		t.Fatalf("glob %s matched no test files", pattern)
	}

	var offenders []string
	for _, path := range paths {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		offenders = append(offenders, rawEmptyBeadsScopeRootClears(fset, file)...)
	}
	if len(offenders) > 0 {
		t.Fatalf("%d cmd/gc test site(s) clear GC_BEADS_SCOPE_ROOT directly. An empty scope root matches "+
			"every city, and without a GC_CITY pin the cwd walk can reach the operator's live city (ga-bvv). "+
			"Use setIsolatedBeadsProviderForTest, setScopedBeadsProviderForTest(t, <tempdir>, provider), "+
			"or setUnscopedBeadsProviderForTest instead:\n  %s",
			len(offenders), strings.Join(offenders, "\n  "))
	}
}

// beadsProviderHelpersThatKeepCityPins are the helpers that must leave a city
// pin the test already set.
var beadsProviderHelpersThatKeepCityPins = []struct {
	name  string
	apply func(t *testing.T)
}{
	{
		name:  "scoped",
		apply: func(t *testing.T) { setScopedBeadsProviderForTest(t, t.TempDir(), "file") },
	},
	{
		name:  "unscoped",
		apply: func(t *testing.T) { setUnscopedBeadsProviderForTest(t, "file") },
	},
}

// TestBeadsProviderHelpersKeepExplicitCityPin: GC_CITY wins over GC_CITY_PATH
// and GC_CITY_ROOT, so a throwaway GC_CITY written over a test's own pin would
// point that test at an empty city (ga-bvv).
func TestBeadsProviderHelpersKeepExplicitCityPin(t *testing.T) {
	for _, helper := range beadsProviderHelpersThatKeepCityPins {
		for _, key := range explicitCityPinEnvKeys {
			t.Run(helper.name+"/"+key, func(t *testing.T) {
				clearExplicitCityPinsForTest(t)
				cityDir := writeHelperPinTestCity(t)
				t.Setenv(key, cityDir)

				helper.apply(t)

				for _, other := range explicitCityPinEnvKeys {
					want := ""
					if other == key {
						want = cityDir
					}
					if got := os.Getenv(other); got != want {
						t.Errorf("%s = %q after helper, want %q", other, got, want)
					}
				}
				got, ok := resolveExplicitCityPathEnv()
				if !ok || !samePath(got, cityDir) {
					t.Fatalf("resolveExplicitCityPathEnv() = %q, %v; want %q, true", got, ok, cityDir)
				}
			})
		}
	}
}

// TestBeadsProviderHelpersPinThrowawayCityWhenUnpinned: with no city pin the
// cwd walk is the fallback, so every helper must pin a throwaway city.
func TestBeadsProviderHelpersPinThrowawayCityWhenUnpinned(t *testing.T) {
	for _, helper := range beadsProviderHelpersThatKeepCityPins {
		t.Run(helper.name, func(t *testing.T) {
			clearExplicitCityPinsForTest(t)

			helper.apply(t)

			assertThrowawayCityPinned(t)
		})
	}
}

// TestSetIsolatedBeadsProviderForTestReplacesExplicitCityPin: the fully
// confined form scopes GC_BEADS to its own city, so it must pin that city even
// over a pin the test already set.
func TestSetIsolatedBeadsProviderForTestReplacesExplicitCityPin(t *testing.T) {
	clearExplicitCityPinsForTest(t)
	t.Setenv("GC_CITY_PATH", writeHelperPinTestCity(t))

	isolated := setIsolatedBeadsProviderForTest(t, "file")

	got := assertThrowawayCityPinned(t)
	if !samePath(got, isolated) {
		t.Fatalf("resolveExplicitCityPathEnv() = %q, want isolated city %q", got, isolated)
	}
}

func clearExplicitCityPinsForTest(t *testing.T) {
	t.Helper()
	for _, key := range explicitCityPinEnvKeys {
		t.Setenv(key, "")
	}
}

func writeHelperPinTestCity(t *testing.T) string {
	t.Helper()
	cityDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(cityDir, "city.toml"), []byte("[workspace]\nname = \"pinned-test-city\"\n"), 0o644); err != nil {
		t.Fatalf("write pinned city.toml: %v", err)
	}
	return cityDir
}

func assertThrowawayCityPinned(t *testing.T) string {
	t.Helper()
	got, ok := resolveExplicitCityPathEnv()
	if !ok {
		t.Fatal("resolveExplicitCityPathEnv() found no city pin; the cwd walk would decide the city")
	}
	data, err := os.ReadFile(filepath.Join(got, "city.toml"))
	if err != nil {
		t.Fatalf("read pinned city.toml: %v", err)
	}
	if !strings.Contains(string(data), `name = "isolated-test-city"`) {
		t.Fatalf("pinned city %q is not the throwaway city; city.toml:\n%s", got, data)
	}
	return got
}

func TestRawEmptyBeadsScopeRootClearsDetector(t *testing.T) {
	const src = `package main

import (
	"os"
	"testing"
)

var packageScopeHook = func(t *testing.T) {
	t.Setenv("GC_BEADS_SCOPE_ROOT", "")
}

func setUnscopedBeadsProviderForTest(t *testing.T, provider string) {
	t.Setenv("GC_BEADS_SCOPE_ROOT", "")
}

func TestDirectClear(t *testing.T) {
	t.Setenv("GC_BEADS_SCOPE_ROOT", "")
}

func TestProcessEnvClear(t *testing.T) {
	_ = os.Setenv("GC_BEADS_SCOPE_ROOT", "")
	_ = os.Unsetenv("GC_BEADS_SCOPE_ROOT")
}

func TestSubtestRawStringClear(t *testing.T) {
	t.Run("child", func(t *testing.T) {
		t.Setenv(` + "`GC_BEADS_SCOPE_ROOT`" + `, ` + "``" + `)
	})
}

func TestAllowedShapes(t *testing.T) {
	t.Setenv("GC_BEADS_SCOPE_ROOT", t.TempDir())
	t.Setenv("GC_BEADS", "")
	t.Setenv("GC_BEADS_SCOPE_ROOT_EXTRA", "")
	key := "GC_BEADS_SCOPE_ROOT"
	t.Setenv(key, "")
}
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "fixture_test.go", src, 0)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}

	got := rawEmptyBeadsScopeRootClears(fset, file)
	want := []string{
		"fixture_test.go:9: package scope",
		"fixture_test.go:17: TestDirectClear",
		"fixture_test.go:21: TestProcessEnvClear",
		"fixture_test.go:22: TestProcessEnvClear",
		"fixture_test.go:27: TestSubtestRawStringClear",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("rawEmptyBeadsScopeRootClears() =\n  %s\nwant\n  %s",
			strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// rawEmptyBeadsScopeRootClears returns "path:line: function" for every call in
// file that clears GC_BEADS_SCOPE_ROOT outside emptyBeadsScopeRootAllowedFuncs.
// It matches literal keys only; a key computed at run time is out of reach.
func rawEmptyBeadsScopeRootClears(fset *token.FileSet, file *ast.File) []string {
	var offenders []string
	for _, decl := range file.Decls {
		owner := "package scope"
		if fn, ok := decl.(*ast.FuncDecl); ok {
			owner = fn.Name.Name
		}
		if emptyBeadsScopeRootAllowedFuncs[owner] {
			continue
		}
		ast.Inspect(decl, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if ok && clearsBeadsScopeRoot(call) {
				pos := fset.Position(call.Pos())
				offenders = append(offenders, fmt.Sprintf("%s:%d: %s", pos.Filename, pos.Line, owner))
			}
			return true
		})
	}
	return offenders
}

func clearsBeadsScopeRoot(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	switch sel.Sel.Name {
	case "Setenv":
		return len(call.Args) == 2 && isStringLiteral(call.Args[0], "GC_BEADS_SCOPE_ROOT") && isStringLiteral(call.Args[1], "")
	case "Unsetenv":
		return len(call.Args) == 1 && isStringLiteral(call.Args[0], "GC_BEADS_SCOPE_ROOT")
	}
	return false
}

func isStringLiteral(expr ast.Expr, want string) bool {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return false
	}
	value, err := strconv.Unquote(lit.Value)
	return err == nil && value == want
}
