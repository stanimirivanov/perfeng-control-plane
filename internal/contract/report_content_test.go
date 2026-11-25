package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"testing"
)

func TestReportContentPin(t *testing.T) {
	var lock struct {
		Commit, BundleVersion, RunManagementAPIVersion, OperationID string
		SHA256                                                      map[string]string
	}
	data, err := ReportContentFiles.ReadFile("report-content/lock.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &lock); err != nil {
		t.Fatal(err)
	}
	if lock.Commit != "253733ca16ea2b3a6f9c516e69ae0bf3f9cac5f1" || lock.BundleVersion != "0.32.0" ||
		lock.RunManagementAPIVersion != "0.9.0" || lock.OperationID != "getRunAnalysisReportContent" ||
		lock.SHA256["openapi.json"] != "5f650e99f77f8b6a1b536588af8d11bac90132719a058f863e92160f8833eb1c" {
		t.Fatal("unexpected report-content contract selection")
	}
	snapshot, err := fs.Sub(ReportContentFiles, "report-content")
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	err = fs.WalkDir(snapshot, ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() || path == "lock.json" {
			return walkErr
		}
		body, err := fs.ReadFile(snapshot, path)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(body)
		if hex.EncodeToString(digest[:]) != lock.SHA256[path] {
			t.Errorf("unselected or changed report-content file %s", path)
		}
		seen++
		return nil
	})
	if err != nil || seen != len(lock.SHA256) || seen != 4 {
		t.Fatalf("invalid report-content inventory (%d files): %v", seen, err)
	}
}
