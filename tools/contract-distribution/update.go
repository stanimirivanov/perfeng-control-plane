package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"
)

type selectionReceipt struct {
	Status             string                   `json:"status"`
	ConsumerCommit     string                   `json:"consumerCommit"`
	LedgerSHA256       string                   `json:"ledgerSha256"`
	DistributionSHA256 string                   `json:"distributionLockSha256"`
	ToolRelease        string                   `json:"toolRelease"`
	HistoryRelease     string                   `json:"historyRelease"`
	Families           map[string]receiptFamily `json:"families"`
}

func prepare(assets, bootstrap, verifier, target, rollback string, output io.Writer) error {
	var evidence bytes.Buffer
	if err := shadow(assets, bootstrap, verifier, &evidence); err != nil {
		return err
	}
	var receipt selectionReceipt
	if err := json.Unmarshal(evidence.Bytes(), &receipt); err != nil {
		return err
	}
	ledgerBytes, err := os.ReadFile(filepath.Join(snapshotPath, "lock.json"))
	if err != nil || digest(ledgerBytes) != receipt.LedgerSHA256 {
		return errors.New("snapshot ledger changed during preparation")
	}
	pinBytes, err := os.ReadFile(lockPath)
	if err != nil || digest(pinBytes) != receipt.DistributionSHA256 {
		return errors.New("distribution lock changed during preparation")
	}
	var lock distribution
	if err := json.Unmarshal(pinBytes, &lock); err != nil {
		return err
	}
	scratch, err := os.MkdirTemp("", "perfeng-contract-update-")
	if err != nil {
		return err
	}
	defer func() {
		if err := os.RemoveAll(scratch); err != nil {
			fmt.Fprintln(os.Stderr, "contracts-update: temporary cleanup:", err)
		}
	}()
	tool, err := restoreTool(scratch, assets, bootstrap, verifier, lock.Tool)
	if err != nil {
		return err
	}
	files := map[string][]byte{"lock.json": ledgerBytes}
	for name, family := range receipt.Families {
		projected, err := projectFamily(assets, scratch, tool, lock, family)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		for destination, content := range projected {
			if _, exists := files[destination]; exists {
				return fmt.Errorf("duplicate candidate destination: %s", destination)
			}
			files[destination] = content
		}
	}
	if rollback != "" {
		if err := compareRetained(rollback, files); err != nil {
			return fmt.Errorf("retained rollback: %w", err)
		}
	}
	if err := writeCandidate(target, files); err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(struct {
		Status    string           `json:"status"`
		Selection selectionReceipt `json:"selection"`
		Rollback  string           `json:"rollbackScope,omitempty"`
	}{"prepared", receipt, rollbackScope(rollback)})
}

func rollbackScope(retained string) string {
	if retained != "" {
		return "distribution-path; same selected family revisions"
	}
	return ""
}

func projectFamily(assets, scratch, tool string, lock distribution, family receiptFamily) (result map[string][]byte, err error) {
	area, err := os.MkdirTemp(scratch, "projection-")
	if err != nil {
		return nil, err
	}
	input := make(map[string]string, len(lock.Publisher)+1)
	for key, value := range lock.Publisher {
		input[key] = value
	}
	input["manifestSha256"] = family.ManifestSHA256
	policy := filepath.Join(area, "policy.json")
	if err := writeJSON(policy, input); err != nil {
		return nil, err
	}
	profile := filepath.Join(assets, "history", family.Revision)
	manifestBytes, err := os.ReadFile(profile + ".manifest.json")
	if err != nil {
		return nil, err
	}
	if err := verifyManifest(manifestBytes, lock.HistoryRelease, family.publisher); err != nil {
		return nil, err
	}
	var manifest struct {
		Members map[string]string `json:"members"`
	}
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return nil, err
	}
	mapping := make(map[string]member, len(manifest.Members))
	for source, hash := range manifest.Members {
		destination := "closure/" + source
		if selected, ok := family.Members[source]; ok {
			if selected.SHA256 != hash {
				return nil, errors.New("selected member digest differs from signed manifest")
			}
			destination = "snapshot/" + selected.Destination
		}
		mapping[source] = member{SHA256: hash, Destination: destination}
	}
	selection := filepath.Join(area, "selection.json")
	if err := writeJSON(selection, struct {
		Revision string            `json:"sourceRevision"`
		Members  map[string]member `json:"members"`
	}{family.Revision, mapping}); err != nil {
		return nil, err
	}
	store := filepath.Join(area, "store")
	if err := os.Mkdir(store, 0o700); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(tool, "runtime/bin/python3.12"), "-IB", filepath.Join(tool, "materialize.py"),
		"--manifest", profile+".manifest.json", "--archive", profile+".tar.gz",
		"--bundle", filepath.Join(assets, "history/manifest.sigstore.jsonl"),
		"--policy", policy, "--selection", selection, "--target", store, "--execute")
	cmd.Dir, cmd.Env, cmd.Stderr = area, []string{}, os.Stderr
	published, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	pointer, err := os.ReadFile(filepath.Join(store, "current"))
	if err != nil || len(pointer) != 65 || pointer[64] != '\n' || !digestPattern.Match(pointer[:64]) {
		return nil, errors.New("invalid projection pointer")
	}
	generation := filepath.Join(store, string(pointer[:64]))
	if string(published) != generation+"\n" {
		return nil, errors.New("projection output disagrees with its pointer")
	}
	projection, err := os.OpenRoot(filepath.Join(generation, "snapshot"))
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, projection.Close()) }()
	result = make(map[string][]byte, len(family.Members))
	for _, selected := range family.Members {
		content, err := projection.ReadFile(selected.Destination)
		if err != nil || digest(content) != selected.SHA256 {
			return nil, fmt.Errorf("projected bytes differ: %s", selected.Destination)
		}
		result[selected.Destination] = content
	}
	return result, nil
}

func writeCandidate(target string, files map[string][]byte) (result error) {
	for name := range files {
		if !fs.ValidPath(name) || strings.ContainsAny(name, ":\\") {
			return fmt.Errorf("invalid candidate destination: %s", name)
		}
	}
	if err := os.Mkdir(target, 0o700); err != nil {
		return err
	}
	defer func() {
		if result != nil {
			result = errors.Join(result, os.RemoveAll(target))
		}
	}()
	root, err := os.OpenRoot(target)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, root.Close()) }()
	for name, content := range files {
		if err := root.MkdirAll(path.Dir(name), 0o700); err != nil {
			return err
		}
		if err := root.WriteFile(name, content, 0o600); err != nil {
			return err
		}
	}
	return nil
}

func compareRetained(directory string, expected map[string][]byte) (result error) {
	root, err := os.OpenRoot(directory)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, root.Close()) }()
	count := 0
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		want, exists := expected[name]
		if !entry.Type().IsRegular() || !exists {
			return fmt.Errorf("different or undeclared retained file: %s", name)
		}
		content, readErr := root.ReadFile(name)
		if readErr != nil || !bytes.Equal(content, want) {
			return fmt.Errorf("different or undeclared retained file: %s", name)
		}
		count++
		return nil
	})
	if err == nil && count != len(expected) {
		return errors.New("retained snapshot is missing a selected file")
	}
	return err
}
