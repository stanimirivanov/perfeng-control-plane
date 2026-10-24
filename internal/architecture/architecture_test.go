// Package architecture contains executable dependency-boundary policy.
package architecture

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const modulePath = "github.com/stanimirivanov/perfeng-control-plane"

type layer string

const (
	coreLayer        layer = "core"
	applicationLayer layer = "application"
	edgeLayer        layer = "edge"
	compositionLayer layer = "composition"
)

// packageLayers is exhaustive for production packages under internal and cmd.
// The edge layer is intentionally broad while concrete reconciliation and
// registry dependencies are removed through focused refactoring. It is not a
// claim that every edge-to-edge dependency is the desired final architecture.
var packageLayers = map[string]layer{
	modulePath + "/internal/integrationcontract": edgeLayer,
	modulePath + "/internal/analysisresult":      coreLayer,
	modulePath + "/internal/baseline":            coreLayer,
	modulePath + "/internal/benchmark":           coreLayer,
	modulePath + "/internal/contract":            coreLayer,
	modulePath + "/internal/jsondocument":        coreLayer,
	modulePath + "/internal/nativeworker":        coreLayer,
	modulePath + "/internal/normalizedresult":    coreLayer,
	modulePath + "/internal/policy":              coreLayer,
	modulePath + "/internal/rawresult":           coreLayer,
	modulePath + "/internal/run":                 coreLayer,

	modulePath + "/internal/worker": applicationLayer,

	modulePath + "/internal/authentication": edgeLayer,
	modulePath + "/internal/httpapi":        edgeLayer,
	modulePath + "/internal/kubernetes":     edgeLayer,
	modulePath + "/internal/memory":         edgeLayer,
	modulePath + "/internal/objectstore":    edgeLayer,
	modulePath + "/internal/postgres":       edgeLayer,
	modulePath + "/internal/reconcile":      edgeLayer,
	modulePath + "/internal/registry":       edgeLayer,

	modulePath + "/internal/app":          compositionLayer,
	modulePath + "/cmd/control-plane":     compositionLayer,
	modulePath + "/cmd/migrate":           compositionLayer,
	modulePath + "/cmd/validate-registry": compositionLayer,
	modulePath + "/cmd/verify-image":      compositionLayer,
}

var allowedDependencies = map[layer]map[layer]bool{
	coreLayer:        {coreLayer: true},
	applicationLayer: {coreLayer: true, applicationLayer: true},
	edgeLayer:        {coreLayer: true, applicationLayer: true},
	compositionLayer: {coreLayer: true, applicationLayer: true, edgeLayer: true, compositionLayer: true},
}

// existingEdgeDependencies records current concrete coupling while those
// packages are refactored. Removing a dependency should also remove its entry;
// adding one requires a separate architecture decision, not a blanket layer rule.
var existingEdgeDependencies = map[string]map[string]bool{
	modulePath + "/internal/authentication": {modulePath + "/internal/httpapi": true},
	modulePath + "/internal/postgres": {
		modulePath + "/internal/httpapi":    true,
		modulePath + "/internal/kubernetes": true,
	},
	modulePath + "/internal/reconcile": {
		modulePath + "/internal/kubernetes":  true,
		modulePath + "/internal/objectstore": true,
	},
	modulePath + "/internal/registry": {
		modulePath + "/internal/kubernetes": true,
		modulePath + "/internal/reconcile":  true,
	},
}

type productionPackage struct {
	path    string
	imports map[string]struct{}
}

func TestProductionDependenciesFollowDeclaredBoundary(t *testing.T) {
	root := repositoryRoot(t)
	packages := discoverProductionPackages(t, root)
	observedEdgeDependencies := map[string]map[string]bool{}

	for path := range packages {
		if _, classified := packageLayers[path]; !classified {
			t.Errorf("%s is not classified; add it to packageLayers and choose the narrowest current boundary (policy: docs/development/harness.md#command-inventory)", path)
		}
	}
	for path := range packageLayers {
		if _, exists := packages[path]; !exists {
			t.Errorf("architecture classification for %s is stale; remove or update it", path)
		}
	}

	for _, sourcePath := range sortedPackagePaths(packages) {
		sourceLayer, classified := packageLayers[sourcePath]
		if !classified {
			continue
		}
		for _, importedPath := range sortedImports(packages[sourcePath].imports) {
			if sourceLayer == coreLayer && hasInfrastructureAuthority(importedPath) {
				t.Errorf("%s imports %s: core infrastructure dependency is forbidden; move I/O behind an edge adapter (policy: docs/development/engineering-standards.md#architecture-and-boundaries)", sourcePath, importedPath)
				continue
			}
			if !strings.HasPrefix(importedPath, modulePath+"/") {
				continue
			}
			targetLayer, known := packageLayers[importedPath]
			if !known {
				t.Errorf("%s imports unknown repository package %s; classify the target before adding the dependency", sourcePath, importedPath)
				continue
			}
			if sourceLayer == edgeLayer && targetLayer == edgeLayer {
				if observedEdgeDependencies[sourcePath] == nil {
					observedEdgeDependencies[sourcePath] = map[string]bool{}
				}
				observedEdgeDependencies[sourcePath][importedPath] = true
				if !existingEdgeDependencies[sourcePath][importedPath] {
					t.Errorf("%s imports %s: new edge-to-edge dependency is forbidden; introduce a consumer-owned contract or record a reviewed architecture decision", sourcePath, importedPath)
				}
				continue
			}
			if !allowedDependencies[sourceLayer][targetLayer] {
				t.Errorf("%s (%s) imports %s (%s): dependency points outward; introduce a consumer-owned contract or move composition outward", sourcePath, sourceLayer, importedPath, targetLayer)
			}
		}
	}
	for sourcePath, targets := range existingEdgeDependencies {
		for targetPath := range targets {
			if !observedEdgeDependencies[sourcePath][targetPath] {
				t.Errorf("edge-to-edge exception %s -> %s is stale; remove it after deleting the import", sourcePath, targetPath)
			}
		}
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func discoverProductionPackages(t *testing.T, root string) map[string]productionPackage {
	t.Helper()
	packages := map[string]productionPackage{}
	for _, top := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".go" || strings.HasSuffix(entry.Name(), "_test.go") {
				return nil
			}
			relative, err := filepath.Rel(root, filepath.Dir(path))
			if err != nil {
				return err
			}
			packagePath := modulePath + "/" + filepath.ToSlash(relative)
			current := packages[packagePath]
			current.path = packagePath
			if current.imports == nil {
				current.imports = map[string]struct{}{}
			}
			parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
			if err != nil {
				return err
			}
			for _, imported := range parsed.Imports {
				value, err := strconv.Unquote(imported.Path.Value)
				if err != nil {
					return err
				}
				current.imports[value] = struct{}{}
			}
			packages[packagePath] = current
			return nil
		})
		if err != nil {
			t.Fatalf("discover production packages: %v", err)
		}
	}
	return packages
}

func hasInfrastructureAuthority(importPath string) bool {
	if strings.HasPrefix(importPath, modulePath+"/") || importPath == "net/url" || importPath == "net/netip" {
		return false
	}
	first := strings.Split(importPath, "/")[0]
	if strings.Contains(first, ".") {
		return true
	}
	for _, prefix := range []string{"crypto/tls", "crypto/x509", "database/sql", "net", "os", "plugin", "syscall", "unsafe"} {
		if importPath == prefix || strings.HasPrefix(importPath, prefix+"/") {
			return true
		}
	}
	return false
}

func sortedPackagePaths(packages map[string]productionPackage) []string {
	paths := make([]string, 0, len(packages))
	for path := range packages {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func sortedImports(imports map[string]struct{}) []string {
	paths := make([]string, 0, len(imports))
	for path := range imports {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func TestCoreInfrastructureClassification(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		path string
		want bool
	}{
		{"net/url", false},
		{"net/netip", false},
		{"net/http", true},
		{"os", true},
		{"github.com/example/client", true},
		{modulePath + "/internal/run", false},
	} {
		if got := hasInfrastructureAuthority(test.path); got != test.want {
			t.Errorf("hasInfrastructureAuthority(%q) = %t, want %t", test.path, got, test.want)
		}
	}
}

func TestAllowedLayerDirections(t *testing.T) {
	t.Parallel()
	if allowedDependencies[coreLayer][edgeLayer] {
		t.Fatal("core must not depend on edge")
	}
	if allowedDependencies[edgeLayer][edgeLayer] {
		t.Fatal("edge-to-edge dependencies must use the explicit exception list")
	}
	if !allowedDependencies[compositionLayer][coreLayer] || !allowedDependencies[edgeLayer][applicationLayer] {
		t.Fatal("outer layers must retain inward dependency authority")
	}
}
