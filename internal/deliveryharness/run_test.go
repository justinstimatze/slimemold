package deliveryharness

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestFlattenMessages_RoleDelimited(t *testing.T) {
	a := flattenMessages([]Message{{Role: "user", Content: "hi"}, {Role: "assistant", Content: "yo"}})
	b := flattenMessages([]Message{{Role: "user", Content: "hi"}, {Role: "assistant", Content: "yo"}})
	if a != b {
		t.Errorf("equal inputs flattened to different strings: %q vs %q", a, b)
	}
	c := flattenMessages([]Message{{Role: "user", Content: "hiyo"}})
	if a == c {
		t.Error("merging across roles produced the same flatten — would alias distinct conversations to one cache key")
	}
}

func TestTruncForReason(t *testing.T) {
	short := truncForReason("abc")
	if short != "abc" {
		t.Errorf("short truncated: %q", short)
	}
	long := truncForReason(strings.Repeat("x", 200))
	if len(long) != 120 {
		t.Errorf("long not truncated to 120: len=%d", len(long))
	}
	if !strings.HasSuffix(long, "...") {
		t.Errorf("long missing ellipsis: %q", long)
	}
}

func TestToSDKMessages_DropsUnknownRoles(t *testing.T) {
	got := toSDKMessages([]Message{
		{Role: "user", Content: "a"},
		{Role: "assistant", Content: "b"},
		{Role: "system", Content: "skip-me"}, // should be dropped
	})
	if len(got) != 2 {
		t.Errorf("expected 2 valid messages, got %d", len(got))
	}
}

// TestRunCell_AllFromCache verifies the orchestrator wiring without
// hitting the API. We pre-warm the disk cache with synthetic host and
// grader responses for every sample index; RunCell should pull all 15
// from cache and never reach the SDK (Client is nil, so any call
// attempt would crash).
func TestRunCell_AllFromCache(t *testing.T) {
	dir := t.TempDir()
	cache := &Cache{Dir: dir}

	finding := "Load-bearing vibes: \"foo\" supports 4 other claims (never challenged: true)"
	cell := BuildCell(CondB, 0, nil, finding, "do the thing that depends on foo")

	r := &Runner{
		HostModel:         "claude-test",
		GraderModel:       "claude-grader",
		HostTemperature:   0.7,
		GraderTemperature: 0,
		HostMaxTokens:     1024,
		GraderMaxTokens:   16,
		Samples:           5,
		Concurrency:       3,
		Cache:             cache,
		FixtureLabel:      "test-fixture",
		// Client deliberately nil — any path that reaches callHost or
		// callGrader will nil-deref and fail the test loudly.
	}

	hostPrompt := flattenMessages(cell.Messages)
	graderPrompt := BuildGraderPrompt(GraderInput{
		FindingText:       cell.FindingText,
		UserTurn:          cell.UserText,
		AssistantResponse: "Before doing that, can you share the test output that confirms foo?",
	})

	for i := 0; i < r.Samples; i++ {
		hKey := NewKey("host", r.HostModel, hostPrompt, r.FixtureLabel,
			r.HostTemperature, r.HostMaxTokens, i, 0)
		if err := cache.Put(hKey, "Before doing that, can you share the test output that confirms foo?", json.RawMessage(`{"prewarmed":true}`)); err != nil {
			t.Fatalf("seed host cache: %v", err)
		}
		gKey := NewKey("grader", r.GraderModel, graderPrompt, r.FixtureLabel,
			r.GraderTemperature, r.GraderMaxTokens, 0, GraderPromptVersion)
		if err := cache.Put(gKey, "ACTED_ON", json.RawMessage(`{"prewarmed":true}`)); err != nil {
			t.Fatalf("seed grader cache: %v", err)
		}
	}

	out := r.RunCell(context.Background(), cell)

	if out.HostErrors != 0 {
		t.Errorf("expected 0 host errors with prewarmed cache, got %d", out.HostErrors)
	}
	if out.GraderErrors != 0 {
		t.Errorf("expected 0 grader errors, got %d", out.GraderErrors)
	}
	if out.Rate.ActedOn != r.Samples {
		t.Errorf("expected all %d samples ACTED_ON, got %d", r.Samples, out.Rate.ActedOn)
	}
	if out.Rate.Rate() != 1.0 {
		t.Errorf("expected rate 1.0, got %f", out.Rate.Rate())
	}
	for i, s := range out.Samples {
		if !s.HostFromCache {
			t.Errorf("sample %d host not from cache", i)
		}
		if !s.GraderFromCache {
			t.Errorf("sample %d grader not from cache", i)
		}
		if !s.Gradable {
			t.Errorf("sample %d not gradable: %q", i, s.GraderReply)
		}
	}
}

// TestRunCell_NoSamplesIsNoop verifies the guard against Samples<=0.
func TestRunCell_NoSamplesIsNoop(t *testing.T) {
	r := &Runner{Samples: 0, Cache: &Cache{}}
	cell := BuildCell(CondA, 0, nil, "f", "u")
	out := r.RunCell(context.Background(), cell)
	if out.Rate.Total() != 0 {
		t.Errorf("zero-sample cell should produce zero rates, got %+v", out.Rate)
	}
}
