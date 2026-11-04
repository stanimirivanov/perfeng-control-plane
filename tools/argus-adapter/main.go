// Command argus-adapter runs the repository-owned Argus functional API pilot.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/stanimirivanov/perfeng-control-plane/internal/jsondocument"
)

const (
	suite        = "control-plane-api"
	adapter      = "generic-http"
	version      = "1.0.0"
	packagePath  = "./internal/httpapi"
	requestLimit = 64 << 10
	outputLimit  = 1 << 20
)

var nativeTests = map[string]string{
	"run-lifecycle":         "TestCreateGetCancelReplay",
	"run-authentication":    "TestHeadersAuthenticationAndPermissions",
	"baseline-lifecycle":    "TestBaselineAdministrationLifecycle",
	"integration-admission": "TestIntegrationHTTPAdmissionGuardsAndProtocolErrors",
}

var (
	attemptPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,126}$`)
	sha1Pattern    = regexp.MustCompile(`^[a-f0-9]{40}$`)
	sha256Pattern  = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

type repository struct {
	Provider string `json:"provider"`
	Host     string `json:"host"`
	ID       string `json:"providerRepositoryId"`
	Owner    string `json:"owner"`
	Name     string `json:"name"`
}

var pilotRepository = repository{"github", "github.com", "1354503944", "stanimirivanov", "perfeng-control-plane"}

type requestedTest struct {
	SuiteKey string `json:"suiteKey"`
	TestKey  string `json:"testKey"`
	Name     string `json:"name"`
}

type request struct {
	APIVersion string `json:"apiVersion"`
	AttemptID  string `json:"attemptId"`
	Manifest   struct {
		APIVersion string `json:"apiVersion"`
		SHA256     string `json:"sha256"`
	} `json:"manifest"`
	Stage      string     `json:"stage"`
	Repository repository `json:"testRepository"`
	Revision   struct {
		Algorithm string `json:"algorithm"`
		Digest    string `json:"digest"`
	} `json:"testRevision"`
	Adapter string          `json:"adapter"`
	Tests   []requestedTest `json:"tests"`
}

type identity struct {
	ID      string `json:"id"`
	Version string `json:"version"`
}

type failure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type result struct {
	SuiteKey   string   `json:"suiteKey"`
	TestKey    string   `json:"testKey"`
	Outcome    string   `json:"outcome"`
	DurationMS int64    `json:"durationMs"`
	Failure    *failure `json:"failure"`
}

type response struct {
	APIVersion  string     `json:"apiVersion"`
	AttemptID   string     `json:"attemptId"`
	Adapter     identity   `json:"adapter"`
	StartedAt   time.Time  `json:"startedAt"`
	CompletedAt time.Time  `json:"completedAt"`
	Results     []result   `json:"results"`
	Artifacts   []struct{} `json:"artifacts"`
}

func readRequest(input io.Reader, revision string) (request, error) {
	var value request
	raw, err := io.ReadAll(io.LimitReader(input, requestLimit+1))
	if err != nil || !jsondocument.Valid(raw, requestLimit) || !jsondocument.Decode(raw, &value) {
		return value, errors.New("invalid adapter request")
	}
	if value.APIVersion != "argus.dev/functional-api-adapter-request/v1" ||
		!attemptPattern.MatchString(value.AttemptID) || !sha256Pattern.MatchString(value.Manifest.SHA256) ||
		(value.Manifest.APIVersion != "argus.dev/execution-manifest/v1" && value.Manifest.APIVersion != "argus.dev/execution-manifest/v4") ||
		(value.Stage != "selected" && value.Stage != "full-suite") || value.Repository != pilotRepository ||
		value.Revision.Algorithm != "git-sha1" || value.Revision.Digest != revision || !sha1Pattern.MatchString(revision) ||
		value.Adapter != adapter || len(value.Tests) == 0 || len(value.Tests) > len(nativeTests) {
		return value, errors.New("adapter request does not match this checkout or pilot")
	}
	seen := make(map[string]bool, len(value.Tests))
	for _, test := range value.Tests {
		if test.SuiteKey != suite || nativeTests[test.TestKey] == "" || seen[test.TestKey] ||
			strings.TrimSpace(test.Name) == "" || utf8.RuneCountInString(test.Name) > 255 {
			return value, errors.New("invalid pilot test selection")
		}
		seen[test.TestKey] = true
	}
	return value, nil
}

func run(ctx context.Context, input io.Reader, output io.Writer, revision string,
	execute func(context.Context, string) (string, string)) error {
	value, err := readRequest(input, revision)
	if err != nil {
		return err
	}
	start := time.Now()
	document := response{
		APIVersion: "argus.dev/functional-api-adapter-result/v1",
		AttemptID:  value.AttemptID, Adapter: identity{adapter, version},
		StartedAt: start.UTC(), Results: make([]result, 0, len(value.Tests)), Artifacts: []struct{}{},
	}
	for _, test := range value.Tests {
		began := time.Now()
		outcome, code := execute(ctx, nativeTests[test.TestKey])
		item := result{SuiteKey: test.SuiteKey, TestKey: test.TestKey, Outcome: outcome, DurationMS: time.Since(began).Milliseconds()}
		if code != "" {
			item.Failure = &failure{Code: code, Message: "Native Go test did not pass."}
		}
		document.Results = append(document.Results, item)
	}
	document.CompletedAt = time.Now().UTC()
	return json.NewEncoder(output).Encode(document)
}

type cappedOutput struct {
	data     []byte
	overflow bool
}

func (out *cappedOutput) Write(p []byte) (int, error) {
	count := min(len(p), outputLimit-len(out.data))
	out.data = append(out.data, p[:count]...)
	out.overflow = out.overflow || count != len(p)
	return len(p), nil
}

func executeNative(ctx context.Context, name string) (string, string) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "test", "-mod=readonly", "-count=1", "-timeout=30s",
		"-json", "-run", "^"+regexp.QuoteMeta(name)+"$", packagePath)
	command.Env = append(os.Environ(), "GOENV=off", "GOFLAGS=", "GOWORK=off", "GOPROXY=off", "GOSUMDB=off")
	command.WaitDelay = time.Second
	var output cappedOutput
	command.Stdout = &output
	err := command.Run()
	if ctx.Err() != nil {
		return nativeResult(nil, name, ctx.Err())
	}
	if output.overflow {
		return "error", "test-output-limit"
	}
	return nativeResult(output.data, name, err)
}

func nativeResult(data []byte, name string, commandErr error) (string, string) {
	if errors.Is(commandErr, context.DeadlineExceeded) || errors.Is(commandErr, context.Canceled) {
		return "error", "test-timeout"
	}
	var terminal string
	events := bufio.NewScanner(bytes.NewReader(data))
	events.Buffer(make([]byte, 4096), outputLimit)
	for events.Scan() {
		var event struct{ Action, Test string }
		if json.Unmarshal(events.Bytes(), &event) != nil {
			return "error", "test-process-error"
		}
		if event.Test != name || (event.Action != "pass" && event.Action != "fail" && event.Action != "skip") {
			continue
		}
		if terminal != "" {
			return "error", "test-process-error"
		}
		terminal = event.Action
	}
	if events.Err() != nil {
		return "error", "test-process-error"
	}
	var exit *exec.ExitError
	switch {
	case terminal == "pass" && commandErr == nil:
		return "passed", ""
	case terminal == "skip" && commandErr == nil:
		return "skipped", ""
	case terminal == "fail" && errors.As(commandErr, &exit):
		return "failed", "test-failed"
	default:
		return "error", "test-process-error"
	}
}

func checkoutRevision(ctx context.Context) (string, error) {
	head, err := exec.CommandContext(ctx, "git", "rev-parse", "--verify", "HEAD").Output()
	if err != nil || !sha1Pattern.MatchString(strings.TrimSpace(string(head))) {
		return "", errors.New("checkout revision unavailable")
	}
	status, err := exec.CommandContext(ctx, "git", "status", "--porcelain=v1", "--untracked-files=all", "--", "internal", "go.mod", "go.sum").Output()
	if err != nil || len(status) != 0 {
		return "", errors.New("native test sources differ from the checkout revision")
	}
	return strings.TrimSpace(string(head)), nil
}

func main() {
	if len(os.Args) != 1 {
		fmt.Fprintln(os.Stderr, "argus-adapter reads its request from stdin")
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	revision, err := checkoutRevision(ctx)
	if err == nil {
		err = run(ctx, os.Stdin, os.Stdout, revision, executeNative)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "argus-adapter could not complete the protocol")
		os.Exit(1)
	}
}
