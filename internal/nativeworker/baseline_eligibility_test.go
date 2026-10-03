package nativeworker

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
	"github.com/stanimirivanov/perfeng-control-plane/internal/rawresult"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

type baselineResolverStub struct {
	plans map[string]BaselinePlan
	errs  map[string]error
}

func (stub baselineResolverStub) ResolveNativeBaselinePlan(
	_ context.Context, _ string, current run.Run,
) (BaselinePlan, error) {
	if err := stub.errs[current.ID]; err != nil {
		return BaselinePlan{}, err
	}
	return stub.plans[current.ID], nil
}

func TestV2BaselineEligibilityRequiresReviewedCapabilitiesAndEvidence(t *testing.T) {
	document, err := contract.Files.ReadFile("snapshot/api/native-worker/v2/examples/presence.json")
	if err != nil {
		t.Fatal(err)
	}
	presence, err := ParseV2Presence(document, MaximumPresenceBytes)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := baselineTemplateFixture().Resolve(
		"search-journey",
		rawresult.Identity{ID: "browser-smoke", Version: "1.0.0", SHA256: strings.Repeat("f", 64)},
		"smoke", "local-demo", "https://sut.perfeng.invalid/search", baselineArtifactFixture(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !presence.SupportsBaseline(plan) {
		t.Fatal("matching v2 worker rejected")
	}

	for _, test := range []struct {
		name   string
		mutate func(*Presence, *BaselinePlan)
	}{
		{"v1 protocol", func(p *Presence, _ *BaselinePlan) { p.ProtocolVersion = 1 }},
		{"v1 runner", func(p *Presence, _ *BaselinePlan) { p.Capabilities.RunnerContract = 1 }},
		{"unavailable", func(p *Presence, _ *BaselinePlan) { p.Availability = "DRAINING" }},
		{"browser", func(p *Presence, _ *BaselinePlan) { p.Capabilities.Browsers = []string{"firefox"} }},
		{"artifact kind", func(p *Presence, _ *BaselinePlan) { p.Capabilities.ArtifactKinds = []string{"measurements"} }},
		{"environment", func(_ *Presence, p *BaselinePlan) { p.Assignment.Recipe.Environment.ProfileID = "other-host" }},
		{"evidence digest", func(_ *Presence, p *BaselinePlan) {
			p.Assignment.Recipe.Environment.EvidenceSHA256 = strings.Repeat("b", 64)
		}},
		{"runner revision", func(_ *Presence, p *BaselinePlan) { p.Assignment.ExecutionArtifact.Revision = strings.Repeat("d", 40) }},
		{"malformed evidence", func(p *Presence, _ *BaselinePlan) { p.QualificationEvidence = json.RawMessage(`{"runner":`) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			worker := presence
			reviewed := plan
			test.mutate(&worker, &reviewed)
			if worker.SupportsBaseline(reviewed) {
				t.Fatal("incompatible worker accepted")
			}
		})
	}
}

func TestSelectBaselineCandidateUsesReviewedPolicyAndWorkerCompatibility(t *testing.T) {
	document, err := contract.Files.ReadFile("snapshot/api/native-worker/v2/examples/presence.json")
	if err != nil {
		t.Fatal(err)
	}
	presence, err := ParseV2Presence(document, MaximumPresenceBytes)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := baselineTemplateFixture().Resolve(
		"search-journey",
		rawresult.Identity{ID: "browser-smoke", Version: "1.0.0", SHA256: strings.Repeat("f", 64)},
		"smoke", "local-demo", "https://sut.perfeng.invalid/search", baselineArtifactFixture(),
	)
	if err != nil {
		t.Fatal(err)
	}
	incompatible := plan
	incompatible.Assignment.Recipe.Environment.EvidenceSHA256 = strings.Repeat("d", 64)
	candidates := []Candidate{
		{Principal: "alice", Run: run.Run{ID: "v1-only", State: run.StateCreated}},
		{Principal: "alice", Run: run.Run{ID: "wrong-host", State: run.StateCreated}},
		{Principal: "alice", Run: run.Run{ID: "selected", State: run.StateCreated}},
	}
	resolver := baselineResolverStub{
		plans: map[string]BaselinePlan{"wrong-host": incompatible, "selected": plan},
		errs:  map[string]error{"v1-only": run.ErrForbidden},
	}
	selection, found, err := SelectBaselineCandidate(context.Background(), presence, candidates, resolver)
	if err != nil || !found || selection.Candidate.Run.ID != "selected" || selection.Plan != plan {
		t.Fatalf("selection = %#v, found = %t, err = %v", selection, found, err)
	}

	selection, found, err = SelectBaselineCandidate(
		context.Background(), presence, candidates[:2], resolver,
	)
	if err != nil || found || selection.Candidate.Run.ID != "" {
		t.Fatalf("incompatible selection = %#v, found = %t, err = %v", selection, found, err)
	}
	resolver.errs["wrong-host"] = errors.New("registry unavailable")
	if _, _, err := SelectBaselineCandidate(context.Background(), presence, candidates, resolver); err == nil || err.Error() != "registry unavailable" {
		t.Fatalf("resolver failure hidden: %v", err)
	}
	if _, _, err := SelectBaselineCandidate(context.Background(), presence, candidates, nil); !errors.Is(err, run.ErrValidation) {
		t.Fatalf("nil resolver accepted: %v", err)
	}
}
