// Command contract-source-admission binds selected snapshot bytes to immutable
// local Git source objects without fetching or selecting new contracts.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/jsondocument"
)

type family struct {
	Commit string   `json:"commit"`
	Files  []string `json:"files"`
}

type ledger struct {
	Commit                 string            `json:"commit"`
	SHA256                 map[string]string `json:"sha256"`
	NativeWorker           family            `json:"nativeWorker"`
	PerformanceCatalog     family            `json:"performanceCatalog"`
	PerformanceIntegration family            `json:"performanceIntegration"`
	PerformanceAnalysis    family            `json:"performanceAnalysis"`
}

type sourceFile struct {
	Path       string `json:"path"`
	SHA256     string `json:"sha256"`
	Commit     string `json:"commit"`
	SourcePath string `json:"sourcePath"`
}

var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func main() {
	source := flag.String("source", "", "local perfeng-contracts Git object database")
	snapshot := flag.String("snapshot", "internal/contract/snapshot", "selected snapshot directory")
	flag.Parse()
	if *source == "" {
		fmt.Fprintln(os.Stderr, "--source is required; source acquisition is explicit")
		os.Exit(1)
	}
	if err := attest(*source, *snapshot, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func gitObject(repository string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "git", append([]string{"--no-pager", "--no-replace-objects", "-c", "protocol.allow=never", "-C", repository, "cat-file"}, args...)...)
	for _, variable := range os.Environ() {
		if !strings.HasPrefix(variable, "GIT_") {
			command.Env = append(command.Env, variable)
		}
	}
	command.Env = append(command.Env, "GIT_NO_LAZY_FETCH=1", "GIT_TERMINAL_PROMPT=0")
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("read local immutable Git object: %w", err)
	}
	return output, nil
}

func fileSource(name string, lock ledger, overrides map[string]string) (string, string, error) {
	if !fs.ValidPath(name) || !strings.HasSuffix(name, ".json") || strings.ContainsAny(name, ":\\%\n\r") {
		return "", "", fmt.Errorf("invalid selected path %q", name)
	}
	if commit, ok := overrides[name]; ok {
		return commit, name, nil
	}
	if strings.HasPrefix(name, "api/native-worker/v2/") {
		return lock.NativeWorker.Commit, name, nil
	}
	switch {
	case name == "openapi.json", name == "transitions.json":
		return lock.Commit, "api/run-management/v1/" + name, nil
	case strings.HasPrefix(name, "examples/") && strings.Count(name, "/") == 1:
		return lock.Commit, "api/run-management/v1/" + name, nil
	case strings.HasPrefix(name, "schemas/policy/"), strings.HasPrefix(name, "examples/policy/"):
		return lock.Commit, name, nil
	default:
		return "", "", fmt.Errorf("selected path has no reviewed source mapping: %s", name)
	}
}

func attest(repository, directory string, output io.Writer) error {
	root, err := os.OpenRoot(directory)
	if err != nil {
		return err
	}
	defer root.Close()
	encoded, err := fs.ReadFile(root.FS(), "lock.json")
	if err != nil {
		return err
	}
	if !jsondocument.Valid(encoded, 1<<20) {
		return fmt.Errorf("invalid snapshot ledger JSON")
	}
	var lock ledger
	if err = json.Unmarshal(encoded, &lock); err != nil {
		return err
	}
	if len(lock.SHA256) == 0 {
		return fmt.Errorf("selected snapshot ledger is empty")
	}
	overrides := map[string]string{}
	for _, selected := range []family{lock.PerformanceCatalog, lock.PerformanceIntegration, lock.PerformanceAnalysis} {
		for _, name := range selected.Files {
			if _, ok := lock.SHA256[name]; !ok || overrides[name] != "" {
				return fmt.Errorf("ambiguous or undeclared family override: %s", name)
			}
			overrides[name] = selected.Commit
		}
	}
	actual := 0
	if err = fs.WalkDir(root.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("snapshot symlink: %s", path)
		}
		if entry.IsDir() || path == "lock.json" {
			return nil
		}
		if _, ok := lock.SHA256[path]; !ok {
			return fmt.Errorf("undeclared snapshot file: %s", path)
		}
		actual++
		return nil
	}); err != nil {
		return err
	}
	if actual != len(lock.SHA256) {
		return fmt.Errorf("snapshot inventory differs from ledger")
	}
	names := make([]string, 0, len(lock.SHA256))
	for name := range lock.SHA256 {
		names = append(names, name)
	}
	sort.Strings(names)
	files := make([]sourceFile, 0, len(names))
	verifiedCommits := map[string]bool{}
	for _, name := range names {
		commit, path, sourceErr := fileSource(name, lock, overrides)
		if sourceErr != nil {
			return sourceErr
		}
		if !commitPattern.MatchString(commit) || !digestPattern.MatchString(lock.SHA256[name]) {
			return fmt.Errorf("invalid immutable source identity: %s", name)
		}
		if !verifiedCommits[commit] {
			kind, kindErr := gitObject(repository, "-t", commit)
			if kindErr != nil {
				return kindErr
			}
			if string(bytes.TrimSpace(kind)) != "commit" {
				return fmt.Errorf("source identity is not a commit")
			}
			verifiedCommits[commit] = true
		}
		data, readErr := fs.ReadFile(root.FS(), name)
		if readErr != nil {
			return readErr
		}
		digest := sha256.Sum256(data)
		if hex.EncodeToString(digest[:]) != lock.SHA256[name] {
			return fmt.Errorf("selected checksum mismatch: %s", name)
		}
		source, readErr := gitObject(repository, "blob", commit+":"+path)
		if readErr != nil {
			return readErr
		}
		if !bytes.Equal(source, data) {
			return fmt.Errorf("immutable source bytes differ: %s", name)
		}
		files = append(files, sourceFile{name, lock.SHA256[name], commit, path})
	}
	lockDigest := sha256.Sum256(encoded)
	return json.NewEncoder(output).Encode(struct {
		Status string       `json:"status"`
		Ledger string       `json:"ledgerSha256"`
		Files  []sourceFile `json:"files"`
	}{"source-matched", hex.EncodeToString(lockDigest[:]), files})
}
