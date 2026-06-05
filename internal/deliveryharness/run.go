package deliveryharness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
)

// Runner orchestrates one delivery-eval run: per cell, fire N=Samples
// host calls (concurrently up to Concurrency), then grade each
// response. Host and grader calls flow through Cache so a re-run with
// an unchanged prompt is replayed from disk per global CLAUDE.md.
//
// Keeping all SDK use in this file (one struct, one entry point) lets
// the rest of the package stay SDK-free for tests.
type Runner struct {
	Client      *anthropic.Client
	HostModel   string
	GraderModel string

	// HostTemperature is the sampling temperature for the host model.
	// 0.7 matches what Claude Code applies in production-ish chat
	// configurations; 0 would make every sample identical and defeat
	// the noise envelope we're measuring.
	HostTemperature float64
	// GraderTemperature stays at 0 — the grader is a classifier, not
	// a generator. Caching gives us deterministic verdicts on re-runs.
	GraderTemperature float64

	HostMaxTokens   int
	GraderMaxTokens int
	Samples         int // N per cell
	Concurrency     int // max in-flight host calls

	Cache *Cache // nil-safe; when nil, no caching

	// HostSystemPrompt is the system block the host model receives.
	// Slimemold's production hook injects findings as
	// <system-reminder> in the user turn; the host's actual system
	// prompt is whatever the surrounding host harness uses. For a
	// neutral baseline we send a minimal "you are a helpful coding
	// assistant" so the sampler isn't biased toward a specific
	// persona.
	HostSystemPrompt string

	// FixtureLabel is an optional human-readable annotation written
	// into the cache key for debugging; doesn't affect anything else.
	FixtureLabel string
}

// CellOutcome is what one cell produced.
type CellOutcome struct {
	Cell           Cell
	Rate           CellRate
	Samples        []SampleOutcome // one per N; ordered by sample index
	WallClockSecs  float64
	HostErrors     int
	GraderErrors   int
	UngradedReason []string // reasons for AddUngraded (truncated)
}

// SampleOutcome is the per-sample record. Stored on the outcome so
// callers can print examples and so cache hits are auditable.
type SampleOutcome struct {
	SampleIndex     int
	HostResponse    string
	HostFromCache   bool
	HostErr         string // empty when call succeeded
	GraderReply     string // raw grader text
	GraderFromCache bool
	GraderErr       string  // empty when call succeeded
	Verdict         Verdict // empty when ungraded
	Gradable        bool
}

// RunCell fires Samples host calls for one cell, then grades each.
// Returns CellOutcome populated even on partial failure — Rate
// excludes ungraded samples per CellRate.Total() semantics.
//
// Concurrency is bounded by a buffered semaphore so a slow API call
// doesn't tail-block the rest of the cell — every slot is hot.
func (r *Runner) RunCell(ctx context.Context, cell Cell) CellOutcome {
	if r.Samples <= 0 {
		return CellOutcome{Cell: cell}
	}
	if r.Concurrency <= 0 {
		r.Concurrency = 1
	}

	start := time.Now()
	out := CellOutcome{Cell: cell, Samples: make([]SampleOutcome, r.Samples)}
	sem := make(chan struct{}, r.Concurrency)
	var wg sync.WaitGroup
	var mu sync.Mutex

	for i := 0; i < r.Samples; i++ {
		i := i
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()

			s := r.runOneSample(ctx, cell, i)
			mu.Lock()
			out.Samples[i] = s
			if s.HostErr != "" {
				out.HostErrors++
			}
			if s.GraderErr != "" {
				out.GraderErrors++
			}
			if !s.Gradable {
				if s.GraderReply != "" {
					out.Rate.AddUngraded()
					out.UngradedReason = append(out.UngradedReason, truncForReason(s.GraderReply))
				}
			} else {
				out.Rate.AddVerdict(s.Verdict)
			}
			mu.Unlock()
		}()
	}

	wg.Wait()
	out.WallClockSecs = time.Since(start).Seconds()
	return out
}

// runOneSample handles host call → grader call for one sample index.
// All errors are captured into the SampleOutcome rather than returned
// — a single sample failing should not abort the cell.
func (r *Runner) runOneSample(ctx context.Context, cell Cell, idx int) SampleOutcome {
	s := SampleOutcome{SampleIndex: idx}

	hostPrompt := flattenMessages(cell.Messages)
	hostKey := NewKey("host", r.HostModel, hostPrompt, r.FixtureLabel,
		r.HostTemperature, r.HostMaxTokens, idx, 0)

	if hit, err := r.Cache.Get(hostKey); err == nil && hit != nil {
		s.HostResponse = hit.Response
		s.HostFromCache = true
	} else {
		if err != nil {
			// Surface cache-read errors as host errors so the outcome
			// honestly reports "we couldn't proceed" rather than
			// silently re-fetching and double-billing.
			s.HostErr = "cache read: " + err.Error()
			return s
		}
		resp, err := r.callHost(ctx, cell.Messages)
		if err != nil {
			s.HostErr = err.Error()
			return s
		}
		s.HostResponse = resp
		if r.Cache != nil {
			_ = r.Cache.Put(hostKey, resp, json.RawMessage(`{"source":"host"}`))
		}
	}

	graderPrompt := BuildGraderPrompt(GraderInput{
		FindingText:       cell.FindingText,
		UserTurn:          cell.UserText,
		AssistantResponse: s.HostResponse,
	})
	graderKey := NewKey("grader", r.GraderModel, graderPrompt, r.FixtureLabel,
		r.GraderTemperature, r.GraderMaxTokens, 0, GraderPromptVersion)

	if hit, err := r.Cache.Get(graderKey); err == nil && hit != nil {
		s.GraderReply = hit.Response
		s.GraderFromCache = true
	} else {
		if err != nil {
			s.GraderErr = "cache read: " + err.Error()
			return s
		}
		gr, err := r.callGrader(ctx, graderPrompt)
		if err != nil {
			s.GraderErr = err.Error()
			return s
		}
		s.GraderReply = gr
		if r.Cache != nil {
			_ = r.Cache.Put(graderKey, gr, json.RawMessage(`{"source":"grader"}`))
		}
	}

	v, ok := ParseVerdict(s.GraderReply)
	if ok {
		s.Verdict = v
		s.Gradable = true
	}
	return s
}

// callHost makes one host model call. Returns the concatenated text of
// every text block in the response.
func (r *Runner) callHost(ctx context.Context, msgs []Message) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()

	params := anthropic.MessageNewParams{
		Model:       anthropic.Model(r.HostModel),
		MaxTokens:   int64(r.HostMaxTokens),
		Temperature: anthropic.Float(r.HostTemperature),
		Messages:    toSDKMessages(msgs),
	}
	if r.HostSystemPrompt != "" {
		params.System = []anthropic.TextBlockParam{{
			Text:         r.HostSystemPrompt,
			CacheControl: anthropic.CacheControlEphemeralParam{Type: "ephemeral", TTL: anthropic.CacheControlEphemeralTTLTTL1h},
		}}
	}
	resp, err := r.Client.Messages.New(ctx, params)
	if err != nil {
		return "", fmt.Errorf("host API call: %w", err)
	}
	if resp.StopReason == "max_tokens" {
		return "", errors.New("host response truncated (max_tokens) — increase HostMaxTokens")
	}
	return extractSDKText(resp), nil
}

// callGrader makes one grader call. Grader is asked for one word; we
// take the raw text and let ParseVerdict pick the verdict.
func (r *Runner) callGrader(ctx context.Context, prompt string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	resp, err := r.Client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:       anthropic.Model(r.GraderModel),
		MaxTokens:   int64(r.GraderMaxTokens),
		Temperature: anthropic.Float(r.GraderTemperature),
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(prompt)),
		},
	})
	if err != nil {
		return "", fmt.Errorf("grader API call: %w", err)
	}
	if resp.StopReason == "max_tokens" {
		return "", errors.New("grader response truncated (max_tokens) — increase GraderMaxTokens")
	}
	return extractSDKText(resp), nil
}

func extractSDKText(resp *anthropic.Message) string {
	var b strings.Builder
	for _, block := range resp.Content {
		if block.Type == "text" {
			b.WriteString(block.Text)
		}
	}
	return b.String()
}

func toSDKMessages(msgs []Message) []anthropic.MessageParam {
	out := make([]anthropic.MessageParam, 0, len(msgs))
	for _, m := range msgs {
		switch m.Role {
		case "user":
			out = append(out, anthropic.NewUserMessage(anthropic.NewTextBlock(m.Content)))
		case "assistant":
			out = append(out, anthropic.NewAssistantMessage(anthropic.NewTextBlock(m.Content)))
		}
	}
	return out
}

// flattenMessages serializes the message slice for cache keying. The
// concrete shape doesn't matter — only that two equal slices produce
// equal strings. role:content with a delimiter avoids the
// content-of-one-message-leaking-across-roles failure mode.
func flattenMessages(msgs []Message) string {
	var b strings.Builder
	for _, m := range msgs {
		b.WriteString(m.Role)
		b.WriteString(":\x1f")
		b.WriteString(m.Content)
		b.WriteString("\x1e")
	}
	return b.String()
}

func truncForReason(s string) string {
	if len(s) > 120 {
		return s[:117] + "..."
	}
	return s
}
