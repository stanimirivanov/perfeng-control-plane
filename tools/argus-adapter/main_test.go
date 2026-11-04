package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

const revision = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func payload(t *testing.T) map[string]any {
	t.Helper()
	var value map[string]any
	err := json.Unmarshal([]byte(`{
	"apiVersion":"argus.dev/functional-api-adapter-request/v1",
	"attemptId":"github-1",
	"manifest":{"apiVersion":"argus.dev/execution-manifest/v1","sha256":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
	"stage":"selected","testRepository":{"provider":"github","host":"github.com","providerRepositoryId":"1354503944","owner":"stanimirivanov","name":"perfeng-control-plane"},
	"testRevision":{"algorithm":"git-sha1","digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
	"adapter":"generic-http","tests":[{"suiteKey":"control-plane-api","testKey":"run-lifecycle","name":"ignored display name"}]
}`), &value)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestAdapterRejectsMalformedUnknownAndWrongRevisionBeforeExecution(t *testing.T) {
	for _, alter := range []func(map[string]any){
		func(v map[string]any) { v["command"] = "untrusted" },
		func(v map[string]any) {
			v["tests"] = []map[string]string{{"suiteKey": "control-plane-api", "testKey": "unknown", "name": "unknown"}}
		},
		func(v map[string]any) { v["testRevision"].(map[string]any)["digest"] = strings.Repeat("c", 40) },
		func(v map[string]any) { v["tests"] = nil },
	} {
		v := payload(t)
		alter(v)
		raw, _ := json.Marshal(v)
		var output bytes.Buffer
		err := run(t.Context(), bytes.NewReader(raw), &output, revision, func(context.Context, string) (string, string) {
			t.Fatal("invalid request reached native execution")
			return "", ""
		})
		if err == nil || output.Len() != 0 {
			t.Fatalf("invalid request produced output: %s", &output)
		}
	}
}

func TestAdapterCorrelatesFailedTestAndUsesLiteralNativeSelector(t *testing.T) {
	raw, _ := json.Marshal(payload(t))
	var output bytes.Buffer
	err := run(t.Context(), bytes.NewReader(raw), &output, revision, func(_ context.Context, name string) (string, string) {
		if name != "TestCreateGetCancelReplay" {
			t.Fatalf("selector = %q", name)
		}
		return "failed", "test-failed"
	})
	if err != nil {
		t.Fatal(err)
	}
	var observed response
	if err := json.Unmarshal(output.Bytes(), &observed); err != nil {
		t.Fatal(err)
	}
	if observed.AttemptID != "github-1" || len(observed.Results) != 1 ||
		observed.Results[0].TestKey != "run-lifecycle" || observed.Results[0].Outcome != "failed" ||
		observed.Results[0].Failure.Code != "test-failed" || observed.Artifacts == nil {
		t.Fatalf("incorrect evidence: %s", &output)
	}
}

func TestNativeResultRequiresExactTerminalEvent(t *testing.T) {
	for _, tc := range []struct {
		name, events string
		err          error
		want         string
	}{
		{"pass", `{"Action":"pass","Test":"TestTarget"}`, nil, "passed"},
		{"failed", `{"Action":"fail","Test":"TestTarget"}`, &exec.ExitError{}, "failed"},
		{"omitted", `{"Action":"pass","Test":"TestOther"}`, nil, "error"},
		{"crash", `{"Action":"output","Output":"secret"}`, errors.New("process stopped"), "error"},
		{"timeout", "", context.DeadlineExceeded, "error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			outcome, code := nativeResult([]byte(tc.events), "TestTarget", tc.err)
			if outcome != tc.want || strings.Contains(code, "secret") {
				t.Fatalf("outcome=%s code=%s", outcome, code)
			}
		})
	}
}

func TestPilotCatalogMatchesNativeInventory(t *testing.T) {
	raw, err := os.ReadFile("../../.argus/test-catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	var catalog struct {
		Repository repository
		TestSuites []struct {
			Key, Family, Adapter string
			Repository           repository
			Tests                []struct{ Key string }
		}
	}
	if err := json.Unmarshal(raw, &catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog.TestSuites) != 1 || catalog.Repository != pilotRepository {
		t.Fatal("wrong pilot repository or suite count")
	}
	group := catalog.TestSuites[0]
	if group.Key != suite || group.Family != "functional-api" || group.Adapter != adapter ||
		group.Repository != pilotRepository || len(group.Tests) != len(nativeTests) {
		t.Fatal("catalog does not declare the complete pilot")
	}
	command := exec.CommandContext(t.Context(), "go", "test", "-mod=readonly", "-list", "^Test", packagePath)
	command.Dir = "../.."
	command.Env = append(os.Environ(), "GOENV=off", "GOFLAGS=", "GOWORK=off", "GOPROXY=off", "GOSUMDB=off")
	output, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	discovered := strings.Fields(string(output))
	keys, selectors := map[string]bool{}, map[string]bool{}
	for _, test := range group.Tests {
		name := nativeTests[test.Key]
		if name == "" || keys[test.Key] || selectors[name] || !slices.Contains(discovered, name) {
			t.Fatalf("catalog key %q lacks a unique discovered Go test", test.Key)
		}
		keys[test.Key], selectors[name] = true, true
	}
}

func TestAdapterRejectsDuplicateRequestKeys(t *testing.T) {
	raw, _ := json.Marshal(payload(t))
	raw = append([]byte(`{"attemptId":"conflicting",`), raw[1:]...)
	var output bytes.Buffer
	err := run(t.Context(), bytes.NewReader(raw), &output, revision, func(context.Context, string) (string, string) {
		t.Fatal("duplicate key reached native execution")
		return "", ""
	})
	if err == nil || output.Len() != 0 {
		t.Fatal("duplicate key produced evidence")
	}
}
