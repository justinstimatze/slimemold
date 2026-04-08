# Slimemold

A reasoning topology mapper for [Claude Code](https://docs.anthropic.com/en/docs/claude-code).
Extracts claims from conversations, builds a persistent graph, and surfaces
structural vulnerabilities mechanically — whether you asked for it or not.

*If you just want to install it: [skip to Installation](#installation).*

---

## The Problem: Reasoning That Stops Too Soon

When you partially understand something, it feels like understanding. The
pleasure of a clean mental model — even an incomplete one — mimics the
satisfaction of actual comprehension. You stop digging because the partial
answer already feels right.

This is not a new observation. Cognitive scientists have mapped the phenomenon
from multiple angles:

**Processing fluency masquerades as truth.** When information feels easy to
process, we judge it as more likely to be true (Winkielman & Schwarz 2001,
Topolinski & Strack 2009). The effect is modest in isolation (d ~ 0.3-0.5
in lab settings). Whether it compounds across multi-step reasoning — each
fluent step making the next feel more solid — has not been directly measured.
It's a prediction from the mechanism, not an established result. But the
mechanism is clear: fluent claims feel correct *because* they're fluent,
not because they've been verified.

**Insight feelings terminate deliberation.** The "Eureka heuristic"
(Laukkonen et al. 2018, 2021) shows that the affective spike accompanying
insight — the *aha!* moment — functions as a metacognitive stop signal. The
feeling of rightness (Thompson 2011) substitutes for verification. You feel
like you've arrived, so you stop walking.

**Cognitive foraging follows effort gradients.** Information foraging
theory (Pirolli & Card 1999) predicts that people will over-exploit
"information patches" that provide easy returns and under-explore patches
that require more effort — even when the effortful patches contain more
important information. Hills, Todd, and Goldstone (2008) showed that
internal and external search share cognitive mechanisms: the same
explore/exploit dynamics that govern physical foraging govern how we
search through ideas.

**Effortful processing is the corrective, not the disease.** Bjork's
"desirable difficulties" framework (1994, 2011) shows that conditions which
make learning harder — spacing, interleaving, generation — improve retention
precisely *because* they disrupt fluency. The difficulty itself is the
signal that real processing is happening. The problem isn't that reasoning is
hard. The problem is that fluency makes you think you're done when you're
not.

We expect this to be worse in conversations with AI, though this has not
been directly measured. The mechanism is plausible: language models are
trained to minimize prediction loss on human text, which means their
output is optimized for exactly the qualities that drive processing
fluency — coherence, confidence, natural flow. And the same RLHF
training that makes models useful
makes them agreeable — they'll build on your assumptions rather than
question them. The human brings the partial model; the AI wraps it in
fluent, confident language; and together they construct an argument that
*feels* airtight because every step was easy to process.

The usual response is to tell the thinker to try harder, or to prompt the
AI to push back. Neither works reliably — deliberate effort fades, and
the model's agreeable training distribution reasserts itself within a few
exchanges.

Slimemold takes a different approach: make the structure of reasoning
visible mechanically, so the room catches what the participants miss.

### The Design Principle

The model receives the structural diagnosis. The user receives better
conversation.

When slimemold injects a finding — "this claim is load-bearing with no
evidence" — the model's RLHF-trained helpfulness means it tends to follow
the suggestion, the same way it tends to follow any instruction. The intended
result: "You're absolutely right — and that's exactly why it's worth
digging into where that claim comes from, because if it holds up,
everything you're building on it gets stronger." The model redirects you
toward the work that hedonic friction previously prevented, wrapping the
redirect in the same agreeable tone it wraps everything in.

We call this a *bright pattern* — a design intent, not a proven
mechanism. A dark pattern uses good UX to drive bad outcomes. A bright
pattern attempts to use the model's agreeableness to drive epistemically
good behavior. Whether it actually works — whether users change their
reasoning when structural findings are injected — is an empirical
question we haven't tested yet. The tool detects structural
vulnerabilities. Whether detection leads to correction is the open bet.

## The Slime Mold Metaphor

*Physarum polycephalum* forages by following local chemical gradients.
This is adaptive when the gradient is calibrated — the organism builds
efficient networks *because* it follows the signal. The pathology isn't
gradient-following. It's miscalibration: when the signal doesn't
correspond to actual value, the organism commits resources in the wrong
direction without any indication that it's doing so.

Human reasoning follows the same pattern. We follow the fluency gradient.
When it's calibrated — when things that feel right *are* right — this
works. When it's not — when every AI response is optimized to feel
right regardless of whether it is — we're foraging unevenly without
knowing it.

## What This Tool Does

Slimemold watches conversations as they happen, extracts the claims being
made, builds a persistent graph of how those claims relate to each other,
and surfaces structural vulnerabilities mechanically.

It runs as a Claude Code hook and MCP server. Every few turns, it:

1. Extracts claims from the conversation transcript using Claude Sonnet 4.6
2. Classifies each claim by *basis* — how it was established (research,
   empirical observation, analogy, vibes, LLM output, deduction, assumption,
   definition)
3. Records the *confidence* with which each claim was stated
4. Maps relationships between claims (supports, depends on, contradicts)
5. Runs structural analysis on the resulting graph
6. Injects findings into the conversation as directive system messages

A note on the basis taxonomy: these categories mix evidence source (research,
empirical), reasoning mode (deduction, analogy), and evidence quality (vibes,
assumption). This is intentional. The taxonomy isn't a clean epistemic
hierarchy — it's a practical classification that helps distinguish "I read
this in a paper" from "the AI said it confidently" from "this feels right."
The structural analysis catches the cases where the distinction matters:
when something that *feels* well-sourced is actually load-bearing vibes.

A note on circularity: slimemold uses an LLM (Sonnet) to extract claims and
classify their basis. The tool that flags "llm_output" as epistemically weak
is itself producing llm_output. If the extraction model misclassifies a
sourced claim as vibes, you get a false alarm. If it classifies vibes as
research, you miss a real vulnerability. The tool is a structural diagnostic,
not an oracle. It makes the topology *visible* — but the topology it shows
is only as good as the extraction.

### Seven Vulnerability Types

**CHALLENGE: Load-Bearing Vibes.** A claim with basis "vibes" or
"assumption" that supports two or more other claims. The reasoning structure
depends on something nobody verified. In the conversations we've analyzed, this is the most common vulnerability
— the AI states something confidently, the human
builds on it, and now three layers of deduction rest on an unsourced
assertion.

**CHALLENGE: Fluency Trap.** A claim stated with high confidence but a weak
basis, where other claims depend on it. Confidence 0.9 on a "vibes" claim
is the processing fluency phenomenon made visible: it *felt* true, so it
was stated as true, and now things are built on it.

**REBALANCE: Coverage Imbalance.** Some clusters of claims receive
disproportionate attention relative to their foundational importance.
"Rabbit holes" are clusters with lots of internal activity but nothing
outside depends on them. "Neglected foundations" are clusters that
other claims depend on but that received little development.

**REVISIT: Abandoned Topic.** A cluster of claims explored in earlier
sessions but not touched recently. Was it resolved, or did something more
interesting come along?

**INVESTIGATE: Unchallenged Chain.** A chain of three or more claims where
nothing was questioned. Every step felt reasonable, so nobody paused to
check.

**PUSHBACK: Echo Chamber.** The assistant validates user claims without
challenging them — zero contradictions across the conversation, or
unsourced user assertions accumulating assistant support unchecked.
Structural sycophancy made visible.

**WATCH: Bottleneck.** A claim with high betweenness centrality — many
reasoning paths flow through it. If this single claim is wrong, a large
fraction of the argument collapses.

## What It Found

The most instructive example came from running slimemold on its own
development conversation. The AI stated "SQLite splits writes between
the main database file and WAL files" as confident fact. The tool
classified it as **load-bearing llm_output** — a confident assertion by
the AI without citation, supporting multiple downstream claims. The human
acted on it (moved the database file without the WAL), lost data, and
only then discovered the claim was imprecise. The tool had flagged it
*before* the data loss. The vulnerability was visible. Nobody acted on it.

Visibility doesn't guarantee correction. It creates the opportunity.

The tool also flagged "sessions come and go but the claim graph should
accumulate" as a **load-bearing assumption** supporting six other claims.
This drove the project-naming design, the database architecture, and the
hook's session-handling logic. It was never challenged. It might be
wrong — maybe per-session graphs would work better. The tool surfaced
this; we moved on without addressing it. That's the honest version of
the feedback loop: the diagnostic works, but the patient doesn't always
follow the prescription.

When run on a knowledge-exploration conversation (memory, learning, SRS
research), it extracted 251 claims and 540 edges. Of those, 134 claims
(53%) were classified as "llm_output" — the AI stated them confidently
without citing a source. Only 51 (20%) were "research" with actual
citations. The tool surfaced the pattern the essay describes: a
conversation that *felt* like rigorous research but was structurally
dominated by unsourced confident assertions. (Caveat: this statistic
reflects the extraction model's classification accuracy, not ground
truth. If the extraction model systematically misclassifies, the 53%
number is wrong.)

When run on a transcript from a user whose AI conversations had
[documented concerning patterns](https://www.nytimes.com/2025/08/08/technology/ai-chatbots-delusions-chatgpt.html),
it extracted 54 claims and 70 edges. It flagged five load-bearing
llm_output claims — every one of them was the AI validating the user's
unverified theories, with downstream claims building on the validation.
The structural pattern was exactly what the tool was designed to catch:
confident unsourced assertions becoming the foundation for an entire
conversation's reasoning.

## How Accurate Is It

Benchmarked against the [DialAM-2024](http://dialam.arg.tech/) shared
task — BBC Question Time debates with human-annotated argument structure.
This is adversarial out-of-domain data (multi-speaker political debate,
not AI-assisted reasoning), so these numbers are a floor, not a ceiling:

| Metric | Value |
|--------|-------|
| Claim recall | 76% (64/84 gold propositions found) |
| Edge recall | 52% (15/29 gold argument relations found) |
| Relation type accuracy | 100% (support vs conflict always correct) |

Edge precision against QT30 is 10% — but this is misleading as a quality
metric. QT30 annotates only strict logical inference and conflict.
Slimemold intentionally captures a broader topology (topical dependencies,
conceptual relationships) because the vulnerability detectors need to see
the full reasoning structure, not just formal argumentation.

Basis classification accuracy on a known-provenance benchmark (Wikipedia
citation-needed statements, synthetic research citations, arXiv abstracts):
91.8% with Sonnet 4.6.

## Limitations and Open Questions

**Most unchallenged chains are fine.** If you're explaining how a car
engine works, every step from "fuel enters the cylinder" to "piston
compresses the mixture" is unchallenged — and should be. Slimemold treats
structural patterns as signals, not verdicts. An unchallenged chain of
well-sourced empirical claims is very different from an unchallenged chain
of vibes, even though both trigger the same finding. The tool surfaces
candidates for scrutiny; the human decides whether scrutiny is warranted.

**Structural visibility may not change behavior.** The calibration
literature (Fischhoff 1982, Lichtenstein et al. 1982) shows that *outcome*
feedback improves judgment, but structural feedback — "here's the shape of
your argument" — is a different kind of intervention. The bet slimemold
makes is that people who can see their reasoning topology will fix the
obvious structural failures the same way they fix obvious bugs: not because
they were trained to, but because the problem became visible.

This is testable. If users shown their reasoning topology during
conversations show no change in reasoning behavior — same rate of
unchallenged assumptions, same reliance on llm_output, same abandonment
patterns — compared to a control group without visibility, the thesis is
wrong. We haven't run this experiment.

**The tool itself is a fluency trap.** You just read several paragraphs of
cognitive science citations, a biological metaphor, benchmark numbers, and
concrete examples. It probably felt well-supported. How much of it did you
actually verify? We ran slimemold on its own development conversation and
it flagged claims from this essay — including "language models are fluency
amplifiers" (load-bearing llm_output, no citation, supports the entire
AI-specific section). We kept the claim and grounded it in mechanism
(prediction loss on human text produces fluent output by construction),
but we can't cite a study measuring it. The tool caught it. We made a
judgment call. That's the feedback loop: not automatic, but visible.

## Installation

Requires [Claude Code](https://docs.anthropic.com/en/docs/claude-code),
Go 1.26+, and an Anthropic API key (for claim extraction via Sonnet 4.6).

```bash
# Build
go build -o slimemold .

# Add MCP server to your project's .mcp.json
{
  "mcpServers": {
    "slimemold": {
      "command": "/absolute/path/to/slimemold",
      "args": ["mcp"],
      "env": {}
    }
  }
}
```

Register the Stop hook in `~/.claude/settings.json`:

```json
{
  "hooks": {
    "Stop": [
      {
        "matcher": "",
        "hooks": [
          {
            "type": "command",
            "command": "/absolute/path/to/hooks/slimemold-audit.sh"
          }
        ]
      }
    ]
  }
}
```

The hook fires every 5th assistant response. Each extraction makes one
Sonnet API call (~$0.01-0.05 depending on transcript length). A typical
hour-long session triggers 10-20 extractions.

Set `SLIMEMOLD_MODEL` to override the extraction model:

```bash
export SLIMEMOLD_MODEL=claude-opus-4-6          # best extraction quality
export SLIMEMOLD_MODEL=claude-sonnet-4-6        # default, good balance
export SLIMEMOLD_MODEL=claude-haiku-4-5-20251001  # cheapest, weaker edges
```

### Quick Start (No Hooks)

```bash
# Extract claims from an existing Claude Code transcript
./scripts/test-extract.sh ~/.claude/projects/.../transcript.jsonl

# View the topology
./slimemold viz
```

## CLI

```bash
./slimemold viz                    # ASCII topology for current project
./slimemold -p palace viz          # topology for a different project
./slimemold audit                  # text findings summary
./slimemold -p myproject audit     # audit a specific project
./slimemold reset                  # clear graph for current project
```

Project resolution: `--project` flag > `.slimemold-project` file > directory
name.

## Security Considerations

Slimemold processes conversation transcripts by sending them to the Anthropic
API for claim extraction. This means transcript content leaves your machine.
If your conversations contain sensitive information, be aware that it will be
sent to Anthropic's API as part of the extraction prompt.

**Prompt injection:** Transcript text is injected into the extraction prompt
without sanitization. A malicious transcript could attempt to manipulate the
extraction model's output. The tool_use schema constrains the output format,
which limits but does not eliminate this risk. In practice, slimemold processes
your own Claude Code transcripts, so the threat model assumes local trust.

**Transcript path:** The MCP server validates that transcript paths end in
`.jsonl` and are regular files. It does not restrict which directories can be
read. If you expose the MCP server to untrusted clients, restrict access at
the transport level.

**Data storage:** The claim graph is stored in SQLite at `~/.slimemold/`.
Claims contain text extracted from your conversations. No API keys or
credentials are stored in the database.

## References

- Bjork, R. A. (1994). Memory and metamemory considerations in the training of human beings. In *Metacognition: Knowing about Knowing*.
- Bjork, R. A., & Bjork, E. L. (2011). Making things hard on yourself, but in a good way. In *Psychology and the Real World*.
- Fischhoff, B. (1982). Debiasing. In *Judgment Under Uncertainty: Heuristics and Biases*.
- Hills, T. T., Todd, P. M., & Goldstone, R. L. (2008). Search in external and internal spaces. *Psychological Science*.
- Laukkonen, R. E., et al. (2018). Getting a grip on insight: Real-time and embodied Aha experiences predict correct solutions. *Cognition & Emotion*.
- Laukkonen, R. E., et al. (2021). The dark side of Eureka: Artificially induced Aha moments make facts feel true. *Cognition*.
- Lichtenstein, S., Fischhoff, B., & Phillips, L. D. (1982). Calibration of probabilities. In *Judgment Under Uncertainty*.
- Pirolli, P., & Card, S. (1999). Information foraging. *Psychological Review*.
- Ioannidis, J. P. A. (2005). Why most published research findings are false. *PLoS Medicine*.
- Thompson, V. A. (2011). Dual-process theories: A metacognitive perspective. In *In Two Minds*.
- Topolinski, S., & Strack, F. (2009). The analysis of intuition: Processing fluency and affect in judgements of semantic coherence. *Cognition & Emotion*.
- Winkielman, P., & Schwarz, N. (2001). How pleasant was your childhood? Beliefs about memory shape inferences from experienced difficulty of recall. *Psychological Science*.

---

<details>
<summary><b>Appendix: Slimemold's audit of this README</b></summary>

We fed this README to slimemold as a transcript. Here is what it found.

```
SLIMEMOLD [readme-selfcheck] — 69 claims, 57 edges

CHALLENGE Load-bearing vibes: "Language models are trained to minimize
  prediction loss on human text, which means their output is optimized for
  exactly the qualities that drive processing fluency" supports 4 other
  claims (never challenged)

CHALLENGE Load-bearing vibes: "RLHF training that makes models useful also
  makes them agreeable — they'll build on your assumptions rather than
  question them" supports 4 other claims (never challenged)

CHALLENGE Fluency trap: "The pathology of Physarum-like foraging is
  miscalibration: when the signal doesn't correspond to actual value"
  stated at confidence 0.8 but basis is analogy

CHALLENGE Fluency trap: "Whether injecting structural findings actually
  causes users to change their reasoning is an empirical question we
  haven't tested yet" stated at confidence 0.9 but basis is vibes

CHALLENGE Fluency trap: "Slimemold's bet is that people who can see their
  reasoning topology will fix the obvious structural failures the same way
  they fix obvious bugs" stated at confidence 0.8 but basis is analogy

WATCH Bottleneck (centrality 903): "Processing fluency masquerades as
  truth" [research] — many reasoning paths flow through this claim

WATCH Bottleneck (centrality 796): "The human brings a partial model; the
  AI wraps it in fluent, confident language" [deduction] — many reasoning
  paths flow through this claim
```

Two load-bearing vibes claims. Three fluency traps. Two bottlenecks. The
tool's core thesis ("processing fluency masquerades as truth") is itself
the highest-centrality node in the graph — if that claim is wrong,
everything downstream collapses. We kept it because Winkielman & Schwarz
(2001) is a real citation with a real effect size (Ioannidis 2005). But
the tool is right to flag it: you should check.

</details>
