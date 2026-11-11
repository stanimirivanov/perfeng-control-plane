// Command contract-distribution checks an offline projection of the selected
// mixed-family snapshot against authenticated, independently pinned releases.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/jsondocument"
)

const (
	snapshotPath = "internal/contract/snapshot"
	lockPath     = "contracts-distribution.lock.json"
)

var (
	revisionPattern = regexp.MustCompile("^[a-f0-9]{40}$")
	digestPattern   = regexp.MustCompile("^[a-f0-9]{64}$")
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

type publisher struct {
	Revision       string `json:"sourceRevision"`
	ManifestSHA256 string `json:"manifestSha256"`
}

type distribution struct {
	ToolRelease    string               `json:"toolRelease"`
	HistoryRelease string               `json:"historyRelease"`
	Tool           json.RawMessage      `json:"tool"`
	Publisher      map[string]string    `json:"publisher"`
	Families       map[string]publisher `json:"families"`
}

type member struct {
	SHA256      string `json:"sha256"`
	Destination string `json:"destination"`
}

type receiptFamily struct {
	publisher
	Members map[string]member `json:"members"`
}

func main() {
	assets := flag.String("assets", "", "directory containing tool/ and history/ release assets")
	bootstrap := flag.String("bootstrap", "", "trusted environment checkout with its locked dependencies already installed")
	verifier := flag.String("verifier", "", "trusted verifier executable for the tool restore")
	target := flag.String("target", "", "prepare a fresh snapshot directory after verifying all signed families")
	rollback := flag.String("rollback", "", "retained snapshot directory to verify during cold restoration")
	flag.Parse()
	var err error
	if *target != "" {
		err = prepare(*assets, *bootstrap, *verifier, *target, *rollback, os.Stdout)
	} else if *rollback != "" {
		err = errors.New("rollback requires a fresh target")
	} else {
		err = shadow(*assets, *bootstrap, *verifier, os.Stdout)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "contracts-shadow:", err)
		os.Exit(1)
	}
}

func familyOverrides(snapshot ledger) (map[string]string, error) {
	overrides := map[string]string{}
	for name, selected := range map[string]family{
		"performanceCatalog": snapshot.PerformanceCatalog, "performanceIntegration": snapshot.PerformanceIntegration,
		"performanceAnalysis": snapshot.PerformanceAnalysis,
	} {
		for _, path := range selected.Files {
			if snapshot.SHA256[path] == "" || overrides[path] != "" {
				return nil, fmt.Errorf("undeclared or ambiguous family selection: %s", path)
			}
			overrides[path] = name
		}
	}

	return overrides, nil
}

func selectFamilies(snapshot ledger, pins map[string]publisher) (map[string]map[string]member, error) {
	overrides, err := familyOverrides(snapshot)
	if err != nil {
		return nil, err
	}
	expected := map[string]string{
		"runPolicy": snapshot.Commit, "nativeWorker": snapshot.NativeWorker.Commit,
		"performanceCatalog": snapshot.PerformanceCatalog.Commit, "performanceIntegration": snapshot.PerformanceIntegration.Commit,
		"performanceAnalysis": snapshot.PerformanceAnalysis.Commit,
	}
	result := make(map[string]map[string]member)
	for path, digest := range snapshot.SHA256 {
		if !fs.ValidPath(path) || !strings.HasSuffix(path, ".json") || strings.ContainsAny(path, ":\\%\n\r") {
			return nil, fmt.Errorf("invalid selected path: %q", path)
		}
		name, source := sourcePath(path, overrides[path])
		if name == "" {
			return nil, fmt.Errorf("no reviewed source mapping: %s", path)
		}
		if expected[name] == "" || pins[name].Revision != expected[name] {
			return nil, fmt.Errorf("family revision differs from the snapshot: %s", name)
		}
		if result[name] == nil {
			result[name] = make(map[string]member)
		}
		if _, duplicate := result[name][source]; duplicate {
			return nil, fmt.Errorf("duplicate source mapping: %s", source)
		}
		result[name][source] = member{SHA256: digest, Destination: path}
	}
	if len(result) != len(pins) || len(result) == 0 {
		return nil, errors.New("selected families differ from the distribution lock")
	}

	return result, nil
}

func sourcePath(path, override string) (string, string) {
	switch {
	case override != "":
		return override, path
	case strings.HasPrefix(path, "api/native-worker/v2/"):
		return "nativeWorker", path
	case path == "openapi.json", path == "transitions.json",
		strings.HasPrefix(path, "examples/") && strings.Count(path, "/") == 1:
		return "runPolicy", "api/run-management/v1/" + path
	case strings.HasPrefix(path, "examples/policy/"), strings.HasPrefix(path, "schemas/policy/"):
		return "runPolicy", path
	default:
		return "", ""
	}
}

func compare(snapshot string, verified map[string]string, members map[string]member) (result error) {
	local, err := os.OpenRoot(snapshot)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, local.Close()) }()
	for source, selected := range members {
		want, err := local.ReadFile(selected.Destination)
		if err != nil {
			return err
		}
		if verified[source] != selected.SHA256 || digest(want) != selected.SHA256 {
			return fmt.Errorf("authenticated bytes differ from the selected snapshot: %s", selected.Destination)
		}
	}

	return nil
}

func shadow(assets, bootstrap, verifier string, output io.Writer) error {
	if !filepath.IsAbs(assets) || !filepath.IsAbs(bootstrap) || !filepath.IsAbs(verifier) {
		return errors.New("assets, bootstrap and verifier must be absolute paths")
	}
	consumer, err := git("rev-parse", "HEAD")
	if err != nil || !revisionPattern.MatchString(strings.TrimSpace(string(consumer))) {
		return errors.New("cannot identify the consumer commit")
	}
	commit := strings.TrimSpace(string(consumer))
	ledgerBytes, err := os.ReadFile(filepath.Join(snapshotPath, "lock.json"))
	if err != nil {
		return err
	}
	committedLedger, err := git("cat-file", "blob", commit+":"+snapshotPath+"/lock.json")
	if err != nil || !bytes.Equal(committedLedger, ledgerBytes) {
		return errors.New("snapshot ledger differs from the consumer commit")
	}
	var snapshot ledger
	if !jsondocument.Valid(ledgerBytes, 1<<20) || json.Unmarshal(ledgerBytes, &snapshot) != nil {
		return errors.New("invalid snapshot ledger")
	}
	if err := checkInventory(commit, snapshot); err != nil {
		return err
	}
	lockBytes, err := os.ReadFile(lockPath)
	if err != nil {
		return err
	}
	var lock distribution
	if !jsondocument.Valid(lockBytes, 1<<20) || json.Unmarshal(lockBytes, &lock) != nil {
		return errors.New("invalid distribution lock")
	}
	selections, err := selectFamilies(snapshot, lock.Families)
	if err != nil {
		return err
	}
	scratch, err := os.MkdirTemp("", "perfeng-control-plane-shadow-")
	if err != nil {
		return err
	}
	defer func() {
		if err := os.RemoveAll(scratch); err != nil {
			fmt.Fprintln(os.Stderr, "contracts-shadow: clean up temporary files:", err)
		}
	}()
	tool, err := restoreTool(scratch, assets, bootstrap, verifier, lock.Tool)
	if err != nil {
		return fmt.Errorf("restore signed materializer: %w", err)
	}
	families := make(map[string]receiptFamily, len(selections))
	for name, members := range selections {
		pin := lock.Families[name]
		if err := verifyFamily(assets, scratch, tool, lock.HistoryRelease, lock.Publisher, pin, members); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		families[name] = receiptFamily{publisher: pin, Members: members}
	}

	return json.NewEncoder(output).Encode(struct {
		Status             string                   `json:"status"`
		ConsumerCommit     string                   `json:"consumerCommit"`
		LedgerSHA256       string                   `json:"ledgerSha256"`
		DistributionSHA256 string                   `json:"distributionLockSha256"`
		ToolRelease        string                   `json:"toolRelease"`
		HistoryRelease     string                   `json:"historyRelease"`
		Families           map[string]receiptFamily `json:"families"`
	}{"passed", commit, digest(ledgerBytes), digest(lockBytes), lock.ToolRelease, lock.HistoryRelease, families})
}

func restoreTool(scratch, assets, bootstrap, verifier string, pin json.RawMessage) (string, error) {
	tool := filepath.Join(scratch, "tool")
	pins := filepath.Join(scratch, "tool.json")
	if err := writeJSON(pins, pin); err != nil {
		return "", err
	}
	if err := command(bootstrap, nil, "uv", "run", "--locked", "--offline", "--no-sync", "python",
		"-m", "scripts.contracts_tool_release", "restore", "--archive", filepath.Join(assets, "tool", "materializer.tar.gz"),
		"--bundle", filepath.Join(assets, "tool", "materializer.sigstore.jsonl"), "--verifier", verifier,
		"--pins", pins, "--target", tool); err != nil {
		return "", err
	}

	return tool, nil
}

func checkInventory(commit string, snapshot ledger) error {
	count := 0
	err := fs.WalkDir(os.DirFS(snapshotPath), ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || path == "lock.json" {
			return nil
		}
		if !entry.Type().IsRegular() || !digestPattern.MatchString(snapshot.SHA256[path]) {
			return fmt.Errorf("snapshot contains an undeclared or nonregular member: %s", path)
		}
		committed, readErr := git("cat-file", "blob", commit+":"+snapshotPath+"/"+path)
		if readErr != nil || digest(committed) != snapshot.SHA256[path] {
			return fmt.Errorf("committed snapshot bytes differ from the ledger: %s", path)
		}
		count++

		return nil
	})
	if err == nil && count != len(snapshot.SHA256) {
		return errors.New("snapshot is missing a declared member")
	}

	return err
}

func verifyFamily(assets, scratch, tool, release string, policy map[string]string, pin publisher, members map[string]member) error {
	if !revisionPattern.MatchString(pin.Revision) || !digestPattern.MatchString(pin.ManifestSHA256) {
		return errors.New("invalid publisher pin")
	}
	area, err := os.MkdirTemp(scratch, "family-")
	if err != nil {
		return err
	}
	policyFile := filepath.Join(area, "policy.json")
	input := make(map[string]string, len(policy)+1)
	for name, value := range policy {
		input[name] = value
	}
	input["manifestSha256"] = pin.ManifestSHA256
	if err := writeJSON(policyFile, input); err != nil {
		return err
	}
	history := filepath.Join(assets, "history")
	manifestPath := filepath.Join(area, "manifest.json")
	manifest, err := os.ReadFile(filepath.Join(history, pin.Revision+".manifest.json"))
	if err != nil {
		return err
	}
	if err := verifyManifest(manifest, release, pin); err != nil {
		return err
	}
	if err := os.WriteFile(manifestPath, manifest, 0o600); err != nil {
		return err
	}
	// Inspect the complete signed closure with the released tool. The consumer
	// selects only its embedded members, so unused closure members stay offline.
	const inspect = `import sys
sys.path.insert(0, sys.argv.pop(1))
from scripts.contracts_inspect import main
raise SystemExit(main())`
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(tool, "runtime/bin/python3.12"), "-IB", "-c", inspect, tool,
		"--manifest", manifestPath,
		"--archive", filepath.Join(history, pin.Revision+".tar.gz"),
		"--bundle", filepath.Join(history, "manifest.sigstore.jsonl"),
		"--policy", policyFile, "--verifier", filepath.Join(tool, "bin/gh"))
	cmd.Dir, cmd.Env, cmd.Stderr = area, []string{}, os.Stderr
	result, err := cmd.Output()
	if err != nil {
		return err
	}
	var verified map[string]string
	if !jsondocument.Valid(result, 4<<20) || json.Unmarshal(result, &verified) != nil {
		return errors.New("invalid inspection result")
	}

	return compare(snapshotPath, verified, members)
}

func verifyManifest(data []byte, release string, pin publisher) error {
	var manifest struct {
		Release string `json:"releaseId"`
		Source  struct {
			Revision string `json:"revision"`
		} `json:"source"`
	}
	if digest(data) != pin.ManifestSHA256 || !jsondocument.Valid(data, 4<<20) || json.Unmarshal(data, &manifest) != nil {
		return errors.New("manifest differs from its reviewed digest or is invalid")
	}
	if manifest.Release != release || manifest.Source.Revision != pin.Revision {
		return errors.New("manifest release or source identity differs from the reviewed selection")
	}

	return nil
}

func writeJSON(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0o600)
}

func digest(data []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

func git(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"--no-replace-objects", "-c", "protocol.allow=never"}, args...)...)
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "GIT_") {
			cmd.Env = append(cmd.Env, value)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_NO_LAZY_FETCH=1", "GIT_TERMINAL_PROMPT=0")

	return cmd.Output()
}

func command(directory string, env []string, executable string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Dir, cmd.Env, cmd.Stderr = directory, env, os.Stderr

	return cmd.Run()
}
