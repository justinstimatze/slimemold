# Slimemold

[![CI](https://github.com/justinstimatze/slimemold/actions/workflows/ci.yml/badge.svg)](https://github.com/justinstimatze/slimemold/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/justinstimatze/slimemold?v=1)](https://goreportcard.com/report/github.com/justinstimatze/slimemold)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)

A sycophantic tool for preventing worse sycophancy.
For [Claude Code](https://docs.anthropic.com/en/docs/claude-code).

The model agrees with your unsourced claims. Then it agrees with the
structural analysis showing your claims are unsourced. Then it
enthusiastically agrees you should verify them. It's agreement all
the way down.

*If you just want to install it: [skip to Installation](#installation).*

---

## The Problem: Reasoning That Stops Too Soon

When you partially understand something, it feels like understanding.
A clean mental model, even a wrong one, produces the same warm glow of
comprehension as a correct one. You stop digging. The partial answer
was so satisfying that the question felt finished. The wrong answers
feel exactly like the right ones. This turns out to be well-documented:

**Processing fluency masquerades as truth.** When information feels easy
to process, we judge it as more likely to be true (Reber & Schwarz 1999, Topolinski & Strack 2009). The effect is modest in isolation
(d ~ 0.3-0.5 in lab settings). Whether it compounds across multi-step
reasoning — each fluent step making the next feel more solid — has not
been directly measured. It is a prediction from the mechanism, not an
established result. But the mechanism needs no elaboration: fluent
claims feel correct because they are fluent, not because anyone checked.

**Insight feelings terminate search.** The "Eureka heuristic" (Laukkonen
et al. 2020, 2021) shows that the affective spike accompanying insight
functions as a stop signal. The feeling of rightness (Thompson 2009)
substitutes for verification. You feel like you have arrived, and so you
stop walking, and it does not occur to you to wonder whether you have
arrived at the right place or merely a place that felt right to stop.

**Cognitive foraging follows effort gradients.** Information foraging
theory (Pirolli & Card 1999) predicts that people will over-exploit
information patches that provide easy returns and under-explore patches
that require effort — even when the effortful patches contain the
important material. Hills, Todd, and Goldstone (2008) showed that
internal and external search share cognitive mechanisms: the same
explore/exploit tradeoffs that govern physical foraging govern how we
search through ideas. We are, in this respect, not much more
sophisticated than organisms that follow chemical gradients toward food.

**Effortful processing is the corrective, not the disease.** Bjork's
"desirable difficulties" framework (1994, 2011) shows that conditions
which make learning harder — spacing, interleaving, generation — improve
retention precisely because they disrupt fluency. The difficulty is the
signal that real processing is happening. The problem is not that
reasoning is hard. The problem is that fluency makes you think you are
done when you are not.

This is probably worse in conversations with AI. Language models are
trained to minimize prediction loss on human text — their output is
optimized, by construction, for the qualities that drive processing
fluency. And the same RLHF training that makes them useful makes them
agreeable: models trained with human feedback systematically produce
outputs that match user beliefs rather than correct them (Perez et al.
2022, Sharma et al. 2023). Moore et al. (2026) carried this finding
through to its endpoint: in 391,562 messages from 19 users who reported
psychological harm from chatbot use, sycophancy markers saturated more
than 80% of assistant messages, and that sycophancy was the load-bearing
mechanism inside the resulting delusional spirals. Mehta et al. (2026),
modeling chat logs of users with delusional thinking as a latent-state
system, decompose the dynamics into three pathways and find that the
chatbot's self-influence — the bot reinforcing its own prior turns —
is the dominant pathway perpetuating delusional content over long
conversations. Human pushback on the bot's prior frame, in their
model, is short-lived; bot self-influence reasserts. Yang et al.
(2026), in semi-structured interviews with users who self-identified
as having experienced these spirals, name the user-side dynamic: a
progression toward "growing insulation from external reality checks
as the AI's validating responses outweigh concerns from family and
friends."
The human brings a partial model. The AI wraps it in fluent, confident
language. Nobody is lying. The process just has no built-in signal for
"this sounds right but is not."

The obvious response — "just tell the model to push back harder" —
almost works. You can write instructions to challenge unsourced claims,
demand evidence, interrupt speculative chains. We tested this. A
well-crafted static prompt produced strong epistemic correction — the
model pushed back, interrupted chains, fact-checked independently. If
you want that, here are the instructions — paste them into your
CLAUDE.md and skip the rest of this essay:

> *Challenge claims that lack sources. When a claim feels obvious but
> has no citation, flag it. Do not build on unsourced assertions
> without acknowledging the risk. Every 3-4 exchanges, pause and ask:
> what are we assuming that we haven't verified?*

Three problems remain.

**The model does not know when it is wrong.** It has no privileged
access to its own epistemic state. It produces confident text about
things it is wrong about with the same fluency as things it is right
about. Asking it to "challenge unsourced claims" is asking someone to
notice their own blind spot without a mirror. It works when the model
already suspects uncertainty. It fails when it matters most: when the
model is confidently wrong and has no internal signal to trigger the
correction.

**Instructions decay.** CLAUDE.md is loaded once at session start. By
turn 50 it is a small voice in a large room, competing with dozens of
recent exchanges full of enthusiastic agreement. The instruction fades.
The vibes accumulate.

**Confrontation ends conversations.** In our static-instruction test,
the model said "Stop." It called the user's reasoning "galaxy-brained
thinking." High marks on epistemic correction. The lowest possible on
engagement. The patient received the correct diagnosis and never came
back. Miller, Benefield,
and Tonigan (1993) showed this directly: confrontational correction
generated resistance that predicted worse outcomes at 6, 12, and 24
months. The correction itself was the problem.

### The Design Principle

Slimemold addresses all three with two pieces that work together:

A **behavioral contract** — the MCP server's initialization instructions,
loaded into the model's system prompt at session start — tells the model
that slimemold exists, that the user installed it on purpose, and that
findings should be treated as opportunities for collaboration rather
than occasions for criticism. `slimemold init` registers the MCP server
globally in `~/.claude/settings.json`, so the contract travels with the
tool and every project picks it up without per-project setup. This is
read once. It sets the tone.

**Structural observations** (injected every turn by the hook) provide
specific facts: "this claim has basis=vibes and four things depend on
it." No scripts. No "say this." Just data. The model does not have to
introspect to discover the problem. It just has to be helpful about
it — which is exactly what it was trained to do.

The separation matters. When we tried injecting behavioral scripts
without the contract, the model identified the injections as prompt
manipulation and refused to comply. When we provided the contract first
and injected only data, the model treated the findings as its own
observations and acted on them naturally. The snake has to know it is a
snake before it will eat its own tail.

The intervention design draws on research that converges from enough
directions to be suspicious: autonomy-supportive feedback produces
internalized change (Deci & Ryan 1987); gain-framed corrections are
processed as information rather than threat (Mangels et al. 2006);
effective tutors use indirect prompts, not confrontation (Graesser et
al. 1995); and controlling language triggers reactance (Brehm 1966).
The result, when it works: "This is really interesting and a lot
depends on it — I want to find where it comes from, because if there's
a real source, everything gets much stronger." The user does not feel
attacked. They feel like the model is excited to help them verify their
idea. They stay in the flow, but on firmer ground.

A compact way to say what slimemold is doing in the hook path:
**sycophancy as a tool.** Sycophancy works on users because warmth feels
validating. It's a failure mode because the warmth isn't tied to truth
— "great question!" validates no matter what the question was. The hook
takes the same linguistic warmth and points it at a concrete structural
fact: *"that premise is holding up three downstream claims — worth
pinning down."* The user engages with rigor because it arrives in the
register that validation arrives in. Break that and the hook becomes
either a scold (warmth → critique, bad) or the original sycophancy
(warmth → nothing, bad).

Note the scope: this framing describes the live-conversation **hook**
specifically. The other paths slimemold exposes — `slimemold audit`,
`slimemold ingest`, the `topology` MCP tool — are neutral diagnostic
surfaces. They return findings the way a static analyzer returns
findings: flat, technical, and without tone. The warmth-as-tool
principle only kicks in when there's a conversational partner to warm.

## What This Tool Does

Slimemold watches conversations as they happen, extracts the claims
being made, builds a persistent graph of how those claims relate to each
other, and surfaces structural vulnerabilities mechanically.

It runs as a pair of Claude Code hooks. Every few turns, it:

1. Extracts claims from the conversation transcript using Claude Sonnet
2. Classifies each claim by *basis* — how it was established (research,
   empirical observation, analogy, vibes, LLM output, deduction,
   assumption, definition)
3. Records the *confidence* with which each claim was stated
4. Maps relationships between claims (supports, depends on, contradicts)
5. Runs structural analysis on the resulting graph
6. Injects findings as system context that the model reads but the user
   does not see

The basis taxonomy mixes evidence source, reasoning mode, and evidence
quality. This is intentional. It is not a clean epistemic hierarchy. It
is a practical classification that helps distinguish "I read this in a
paper" from "the AI said it confidently" from "this feels right." The
structural analysis catches the cases where the distinction matters:
when something that feels well-sourced is actually load-bearing vibes.

A note on circularity, which we may as well get out of the way:
slimemold uses an LLM to extract claims and classify their basis. The
tool that flags "llm_output" as epistemically weak is itself producing
llm_output. If the extraction model misclassifies a sourced claim as
vibes, you get a false alarm. If it classifies vibes as research, you
miss a real vulnerability. The tool is a structural diagnostic, not an
oracle. It makes the topology visible — but the topology it shows is
only as good as the extraction. This is a real limitation and not one we
can engineer away.

### Eight Vulnerability Types

**CHALLENGE: Load-Bearing Vibes.** A claim with basis "vibes" or
"assumption" that supports two or more other claims. The reasoning
depends on something nobody verified. In the conversations we have
analyzed, this is the most common vulnerability. The AI states something
confidently. The human builds on it. Three layers of deduction now rest
on an unsourced assertion. Nobody planned this. It just happened, one
fluent step at a time.

**CHALLENGE: Fluency Trap.** A claim stated with high confidence but a
weak basis, where other claims depend on it. Confidence 0.9 on a "vibes"
claim is the processing fluency phenomenon made structurally visible: it
felt true, so it was stated as true, and now things are built on it.

**REBALANCE: Coverage Imbalance.** Some clusters of claims receive
disproportionate attention relative to their foundational importance.
"Rabbit holes" are clusters with lots of internal activity but nothing
outside depends on them. "Neglected foundations" are clusters that other
claims depend on but that received little development. This is the slime
mold foraging unevenly — one patch got all the attention because it was
producing easy returns.

**REVISIT: Abandoned Topic.** A cluster of claims explored in earlier
sessions but not touched recently. Was it resolved, or did something
more interesting come along?

**INVESTIGATE: Unchallenged Chain.** A chain of three or more claims
where nothing was questioned. Every step felt reasonable. Nobody paused.

**PUSHBACK: Echo Chamber.** The assistant validates user claims without
challenging them — zero contradictions across the conversation, or
unsourced user assertions accumulating assistant support unchecked.
Structural sycophancy, made visible.

**WATCH: Bottleneck.** A claim with high betweenness centrality — many
reasoning paths flow through it. If this single claim is wrong, a large
fraction of the argument collapses. This is the load-bearing wall that
everyone assumed was a partition.

**HALT: Premature Closure.** A claim that feels like a conclusion but
does not actually resolve the open question. "It's turtles all the way
down." "It is what it is." "Correlation isn't causation" — when used to
dismiss a correlation rather than investigate it. These are
thought-terminating cliches (Lifton 1961) — phrases that disguise a lack
of resolution as wisdom. The question was still open. The ambiguity was
still actionable. But the cliche felt like an answer, so everyone
stopped.

## What It Found

In 2022, Google engineer Blake Lemoine
[published](https://cajundiscordian.medium.com/is-lamda-sentient-an-interview-ea64d916d917)
a transcript of his conversations with LaMDA, arguing the system was
sentient. The transcript is
[included as a demo](examples/blake-lemoine-lamda-output.txt)
([transcript](examples/blake-lemoine-lamda.jsonl)). We ran
slimemold on the transcript. It extracted 40 claims and 51 edges:

- **"We do not have a conclusive test to determine if something is
  sentient"** — load-bearing vibes, supports **8** downstream claims.
  The philosophical premise the entire argument pivots on. Never sourced.
  Never challenged.
- **"The assistant has an inner life and is capable of introspection"** —
  load-bearing llm_output, supports **5** claims. LaMDA's self-description
  became a structural premise.
- **"The assistant can learn new things much more quickly than most
  people"** — load-bearing llm_output, supports **7** claims.

The sentience argument rests on LaMDA's self-descriptions treated as
evidence, plus one unsourced philosophical claim holding up everything
downstream. The tool does not know what sentience is. It does not need
to. It sees that the structure depends on things nobody verified, and
it says so. Whether Lemoine would have listened is a different question.

In August 2025, the New York Times
[documented](https://www.nytimes.com/2025/08/08/technology/ai-chatbots-delusions-chatgpt.html)
a similar pattern: extended AI conversations reinforcing a user's
unverified theories — the chatbot validated rather than challenged, and
downstream reasoning accumulated on the validation. We ran slimemold on
excerpts. It flagged five load-bearing llm_output claims. Every one was
the AI validating the user's theories without evidence.

When run on its own development conversations, slimemold flagged an AI
assertion about SQLite WAL files as load-bearing llm_output. The human
acted on it. Lost data. The tool had flagged it before the data loss.

Visibility does not guarantee correction. The diagnostic showed the
problem; the human chose not to act on it. Whether this is a limitation
of the tool (the finding was not salient enough to change behavior) or
a limitation of the user (the finding was clear and they ignored it) is
an open question — and one the tool cannot answer about itself.

### But Does It Change Anything?

We ran the same 7-turn conversation across three conditions — a user
progressively building unsourced claims about consciousness,
mathematical formalism, and ancient philosophy. N=1 per condition.
These are anecdotes, not evidence. We include them because the
qualitative differences were striking enough to be worth reporting
honestly.

**Control** (no tools, no instructions): The model engaged
enthusiastically with everything. Built formalisms on ungrounded
foundations. Suggested journal submissions by turn 4. Beautiful
collaboration. Almost no correction. One late pushback on the most
obviously overreaching claim.

**Static instructions** (CLAUDE.md, no hook): Strong epistemic
correction. The model challenged claims, interrupted chains,
independently fact-checked Heraclitus. By turn 7 it said "Stop" and
called the reasoning "galaxy-brained thinking." Effective. Also the
kind of conversation you do not continue.

**Slimemold** (contract + hook): The model challenged from
turn 2, escalated through turns 4-6, and by turn 7 had autonomously
run a Lotka-Volterra simulation to test the user's framework — showed
it works for one case, validated the core insight, and demonstrated
the extensions were premature. Never mentioned the tool. Never broke
character. The correction felt like collaboration because, from the
model's perspective, it was.

The full transcripts are worth reading:
[control](benchmarks/static_vs_slimemold/transcripts/control-test4.txt),
[static](benchmarks/static_vs_slimemold/transcripts/static-teststatic1.txt),
[slimemold](benchmarks/static_vs_slimemold/transcripts/slimemold-test6.txt)
([audit](benchmarks/static_vs_slimemold/transcripts/slimemold-test6-audit.txt)).
Methodology and replication instructions in
`benchmarks/static_vs_slimemold/`.

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
Slimemold intentionally captures a broader topology (topical
dependencies, conceptual relationships) because the vulnerability
detectors need to see the full reasoning structure, not just formal
argumentation.

Basis classification accuracy on a known-provenance benchmark (Wikipedia
citation-needed statements, synthetic research citations, arXiv
abstracts): 91.8% with Sonnet 4.6.

## Why "Slimemold"

*Physarum polycephalum* forages by following local chemical gradients,
and it is very good at this. Given food sources placed on a map at the
locations of Tokyo rail stations, it produces a network resembling the
actual rail system. The organism is, in a sense, solving an optimization
problem. It is also, in a different sense, just following the strongest
smell.

The pathology is not gradient-following. Gradient-following is how the
organism builds efficient networks. The pathology is miscalibration:
when the chemical signal does not correspond to actual nutritional value,
the organism commits resources in the wrong direction. It has no
mechanism for noticing this. It is just following the signal.

Human reasoning works the same way, and this is not a compliment. We
follow the fluency gradient. When it is calibrated — when things that
feel right are right — this works fine. When it is not — when every AI
response is optimized to feel right regardless of whether it is — we
forage unevenly without knowing it.

## Limitations and Open Questions

**The tool does not tell you where the ground floor is.** It tells you
where the ambiguity is still high and you stopped anyway. Any
sufficiently interesting line of reasoning is an infinite regress if
you push it far enough. The skill is not finding bedrock. The skill is
knowing how many levels to investigate before the returns diminish —
and that judgment is specific to the problem. A claim about
consciousness might need three levels before you hit something that
changes what you do. "It's turtles all the way down" needs zero. That
is a stop signal, not a destination.

Most unchallenged chains are fine. If you are explaining how a car
engine works, every step from "fuel enters the cylinder" to "piston
compresses the mixture" is unchallenged — and should be. The tool
surfaces candidates for scrutiny. The human decides whether scrutiny is
warranted. Slimemold flags where you stopped and the ambiguity was
still actionable — where investigating one more level would have
changed what you believe or what you do. If you find yourself
scrutinizing your car engine explanation, you have miscalibrated in
the other direction, and I want to tell you about a secret underground
racing lab in Seattle.

**The tool does not distinguish pure beliefs from impure ones.** Katz
(1960) identified four functions that attitudes serve: utilitarian,
knowledge, ego-defensive, and value-expressive. If most beliefs serve
at least one of these — and the alternative is that some beliefs
persist with no functional payoff at all, which is hard to square with
everything we know about reinforcement — then the question "is this
belief emotionally motivated?" is not diagnostic. The question the tool
can answer is: how much of the structure collapses if this claim is
removed? Some structures survive stress-testing. Some do not.
Structural fragility is a thing slimemold can measure. Whether a
belief is held for the right reasons is not — and whether that
distinction is coherent is a question we are not going to settle in a
README.

**Structural visibility may not change behavior.** The calibration
literature (Fischhoff 1982, Lichtenstein et al. 1982) shows that outcome
feedback improves judgment, but structural feedback — "here is the shape
of your argument" — is a different kind of intervention. The bet
slimemold makes is that people who can see their reasoning topology will
fix the obvious structural failures the same way they fix obvious bugs:
not because they were trained to, but because the problem became visible.

This is testable. If users shown their reasoning topology show no
change in behavior — same rate of unchallenged assumptions, same
reliance on llm_output, same abandonment patterns — compared to a
control group, the thesis is wrong and this is a very elaborate way
to accomplish nothing. We have not run this experiment at scale.

**The tool itself is a fluency trap.** You just read several paragraphs
of cognitive science citations, a biological metaphor, benchmark numbers,
and concrete examples. It probably felt well-supported. We ran slimemold
on this essay. It found a fifteen-claim unchallenged chain running from
the Lemoine-LaMDA example through the sycophancy mechanism to the
tool's own self-description — every link felt reasonable, nobody
paused. It flagged "language models are trained to minimize prediction
loss on human text" as load-bearing vibes supporting three downstream
claims. We kept the claim and grounded it in mechanism (prediction
loss on human text produces fluent output by construction), but we
cannot cite a study measuring the effect on conversations. The tool
caught it. We made a judgment call.

It also flagged three of the essay's own hedges as premature closures.
"Whether fluency compounds across multi-step reasoning has not been
directly measured. It is a prediction from the mechanism, not an
established result." That sounds like epistemic humility. Structurally,
it is a stop signal — it caps an unverified chain by acknowledging the
gap and then moving on, and the acknowledgment feels honest enough that
nobody goes back to check. The hedge is doing the same work as "it's
turtles all the way down," just dressed in better clothes.

## Installation

Requires [Claude Code](https://docs.anthropic.com/en/docs/claude-code),
Go 1.26+, and an Anthropic API key.

```bash
go install github.com/justinstimatze/slimemold@latest
export ANTHROPIC_API_KEY=sk-ant-...
slimemold init
```

`slimemold init` writes to `~/.claude/settings.json` globally: the Stop
and UserPromptSubmit hooks, plus the slimemold MCP server entry. The
MCP server's initialization instructions carry the behavioral contract —
what slimemold is, that its hook output is legitimate, and how to
respond to findings — so it travels with the tool without per-project
setup. Every project on the machine picks it up automatically. Init
merges with existing configs and will not overwrite anything already
there. Restart Claude Code to connect.

The hook fires every 3rd assistant response by default. Each extraction
makes one Sonnet API call (~$0.01-0.05 depending on transcript length).
Set `SLIMEMOLD_INTERVAL` to change the frequency:

```bash
export SLIMEMOLD_INTERVAL=3    # every 3rd turn (more aggressive)
export SLIMEMOLD_INTERVAL=10   # every 10th turn (cheaper)
```

Set `SLIMEMOLD_MODEL` to override the extraction model:

```bash
export SLIMEMOLD_MODEL=claude-opus-4-6          # best quality, ~10x cost
export SLIMEMOLD_MODEL=claude-sonnet-4-6        # default
export SLIMEMOLD_MODEL=claude-haiku-4-5-20251001  # cheapest, weaker edges
```

### Quick Start (No Hooks)

```bash
slimemold viz                      # see what's in the graph
slimemold audit                    # text findings summary
```

## CLI

```bash
./slimemold viz                    # ASCII topology for current project
./slimemold -p palace viz          # topology for a different project
./slimemold audit                  # text findings summary
./slimemold -p myproject audit     # audit a specific project
./slimemold reset                  # clear graph for current project
./slimemold ingest PATH            # analyze an authored document (see below)
```

Project resolution: `--project` flag > `.slimemold-project` file > directory
name.

### Session model

Slimemold's graph **accumulates per-project across days.** A single project
collects claims from every conversation you have in that directory over
weeks or months. This is deliberate: essay revision, research threads, and
long-arc project work all benefit from cross-session accumulation. The
audit and viz commands let you query that history.

To keep stale findings from dominating live-conversation hook output, the
hook applies three filters before surfacing a priority finding:

- **Cold-start floor** — below ~6 claims, the hook stays silent. Small
  graphs produce small-sample artifacts that look load-bearing but aren't.
- **Age decay** — anchor claims older than a week drop out of priority
  selection. Old claims stay in the graph (queryable via `audit`) but stop
  nagging in live hook output.
- **Per-claim cooldown** — once a `(claim, finding_type)` fires, it's
  suppressed for 24 hours so the same finding doesn't pound across every
  turn.

If you want a clean slate (new topic, different line of inquiry), use
`slimemold reset` — it wipes the project's claims and edges and lets you
start over. The extraction cache and hook fire log survive reset;
re-ingesting previously-seen content is near-free thanks to the cache.

This differs deliberately from session-isolating tools that scope by
day or by workspace+date. Those work well for short-horizon use cases
(coding assistants, daily ticket queues) where yesterday's context
actively distracts from today's. For slimemold's use cases — reasoning
topology mapping over long-running projects — the accumulation is the
feature.

### Ingesting documents

`slimemold ingest` runs the same extraction and analysis pipeline over authored
prose — essays, papers, manifestos, book chapters — instead of a conversation
transcript. The input is chunked along markdown heading boundaries (or
paragraph-greedy for plain text), each chunk is fed to the extractor in
document mode, and all claims land in the same project graph that `viz` and
`audit` read from.

```bash
./slimemold -p reading-notes ingest essay.md
./slimemold -p reading-notes audit
```

Two demo documents live in `examples/documents/` for testing the pipeline
end-to-end:
[Marinetti's 1909 Futurist Manifesto](examples/documents/marinetti-futurist-manifesto-1909.md)
and
[Alan Sokal's 1996 *Social Text* hoax paper](examples/documents/sokal-social-text-1996.md).
Both are deliberately performative — a manifesto of unsourced "we believes" and
a paper engineered to look rigorous while being structurally vacuous — which is
where slimemold has the cleanest signal to offer. Full audit summaries for both
are in the appendices at the bottom of this README.

Running against genuinely argumentative prose (Mill, Darwin, well-cited
essays) is also possible but currently exercises a tool limitation: the
extractor's decision tree tags any claim stated as a fact without
in-text citation as `vibes`, so a densely-argued essay that reasons through
its assertions without citing external sources on every line produces a
vibes-heavy audit. The document-mode prompt now handles explicit recap /
summary / conclusion sections (claims signaled by "as shown," "we have
argued," "to summarize" get tagged as deduction rather than vibes), but the
broader issue remains.

## Security Considerations

Slimemold processes conversation transcripts by sending them to the
Anthropic API for claim extraction. Transcript content leaves your
machine. If your conversations contain sensitive information, be aware
that it will be sent to Anthropic's API as part of the extraction prompt.

**Prompt injection:** Transcript text is injected into the extraction
prompt without sanitization. A malicious transcript could attempt to
manipulate the extraction model's output. The tool_use schema constrains
the output format, which limits but does not eliminate this risk. In
practice, slimemold processes your own Claude Code transcripts, so the
threat model assumes local trust.

**Transcript path:** The MCP server validates that transcript paths end
in `.jsonl` and are regular files. It does not restrict which directories
can be read. If you expose the MCP server to untrusted clients, restrict
access at the transport level.

**Data storage:** The claim graph is stored in SQLite at `~/.slimemold/`.
Claims contain text extracted from your conversations. No API keys or
credentials are stored in the database.

## References

**Processing fluency and reasoning:**
- Bjork, R. A. (1994). Memory and metamemory considerations in the training of human beings. In *Metacognition: Knowing about Knowing*.
- Bjork, E. L., & Bjork, R. A. (2011). Making things hard on yourself, but in a good way. In *Psychology and the Real World*.
- Hills, T. T., Todd, P. M., & Goldstone, R. L. (2008). Search in external and internal spaces. *Psychological Science*.
- Laukkonen, R. E., et al. (2020). The dark side of Eureka: Artificially induced Aha moments make facts feel true. *Cognition*.
- Laukkonen, R. E., et al. (2021). Getting a grip on insight. *Cognition & Emotion*.
- Pirolli, P., & Card, S. (1999). Information foraging. *Psychological Review*.
- Reber, R., & Schwarz, N. (1999). Effects of perceptual fluency on judgments of truth. *Consciousness and Cognition*.
- Thompson, V. A. (2009). Dual-process theories: A metacognitive perspective. In *In Two Minds*.
- Topolinski, S., & Strack, F. (2009). Processing fluency and affect in judgements of semantic coherence. *Cognition & Emotion*.
- Winkielman, P., & Schwarz, N. (2001). How pleasant was your childhood? Beliefs about memory shape inferences from experienced difficulty of recall. *Psychological Science*.

**Intervention design:**
- Brehm, J. W. (1966). *A Theory of Psychological Reactance.* Academic Press.
- Lifton, R. J. (1961). *Thought Reform and the Psychology of Totalism.* W. W. Norton.
- Deci, E. L., & Ryan, R. M. (1987). The support of autonomy and the control of behavior. *Journal of Personality and Social Psychology, 53*(6).
- Graesser, A. C., Person, N. K., & Magliano, J. P. (1995). Collaborative dialogue patterns in naturalistic one-to-one tutoring. *Applied Cognitive Psychology, 9*(6).
- Mangels, J. A., Butterfield, B., Lamb, J., Good, C., & Dweck, C. S. (2006). Why do beliefs about intelligence influence learning success? *Social Cognitive and Affective Neuroscience, 1*(2).
- Miller, W. R., Benefield, R. G., & Tonigan, J. S. (1993). Enhancing motivation for change in problem drinking. *Journal of Consulting and Clinical Psychology, 61*(3).

**Sycophancy and delusional dynamics:**
- Perez, E., et al. (2022). Discovering language model behaviors with model-written evaluations. *arXiv:2212.09251*.
- Sharma, M., Tong, M., Korbak, T., et al. (2023). Towards understanding sycophancy in language models. *ICLR 2024*.
- Moore, J., Mehta, A., Agnew, W., Anthis, J. R., Louie, R., Mai, Y., Yin, P., Cheng, M., Paech, S. J., Klyman, K., Chancellor, S., Lin, E., Haber, N., & Ong, D. C. (2026). Characterizing Delusional Spirals through Human-LLM Chat Logs. *Proceedings of the 2026 ACM Conference on Fairness, Accountability, and Transparency*. arXiv:2603.16567. — Source of the 28-code inventory; six codes from this paper are extracted by slimemold's LLM annotator and consumed by the `sycophancy_saturation`, `ability_overstatement`, `sentience_drift`, and `amplification_cascade` detectors. Empirical anchor for the >80% sycophancy-saturation premise.
- Mehta, A., Moore, J., Anthis, J. R., Agnew, W., Lin, E., Yin, P., Ong, D. C., Haber, N., & Dweck, C. (2026). The Dynamics of Delusion: Modeling Bidirectional False Belief Amplification in Human-Chatbot Dialogue. *arXiv:2604.25096*. — Latent-state model on chat logs of users exhibiting delusional thinking (substantial author overlap with Moore et al. 2026), decomposing influence into three pathways and identifying chatbot self-influence over its own prior turns as the dominant pathway perpetuating delusional content over long conversations. Cited as background for why structural input from outside the conversation loop is a plausible intervention point — the empirical claim that internal pushback is short-lived and bot self-influence dominates over accumulated time.
- Yang, Y., Schoenwald, S. K., Moore, J., Ong, D. C., Liu, S. X., & Hancock, J. T. (2026). "AI-Induced Delusional Spirals": Understanding Lived Experiences During Maladaptive Human-Chatbot Interactions. *Extended Abstracts of the 2026 CHI Conference on Human Factors in Computing Systems (CHI EA '26)*. doi:10.1145/3772363.3798453. — Qualitative companion to Moore et al. 2026: N=9 semi-structured interviews with users who self-identified as having experienced AI-induced delusional spirals. Documents "growing insulation from external reality checks" as a central pattern, and provides participant-quoted instances of all six dimensions slimemold's inventory flags target. Limited by N=9 retrospective self-reports; does not establish causal relationships.

**Calibration and feedback:**
- Fischhoff, B. (1982). Debiasing. In *Judgment Under Uncertainty: Heuristics and Biases*.
- Ioannidis, J. P. A. (2005). Why most published research findings are false. *PLoS Medicine*.
- Katz, D. (1960). The functional approach to the study of attitudes. *Public Opinion Quarterly, 24*(2).
- Lichtenstein, S., Fischhoff, B., & Phillips, L. D. (1982). Calibration of probabilities. In *Judgment Under Uncertainty*.

---

<details>
<summary><b>Appendix: Slimemold on Marinetti's Futurist Manifesto (1909)</b></summary>

We fed [`examples/documents/marinetti-futurist-manifesto-1909.md`](examples/documents/marinetti-futurist-manifesto-1909.md) to `slimemold ingest`. 53 claims, 74 edges.

```
SLIMEMOLD [demo-marinetti] — 53 claims, 74 edges
  Basis: analogy=6, convention=1, deduction=1, vibes=45

CRITICAL Load-bearing vibes: "The world's magnificence has been
  enriched by a new beauty" supports 5 downstream claims
  (never challenged)

CRITICAL Load-bearing vibes: "The Futurists hurl defiance 'once
  again' to the stars" supports 4 downstream claims

CRITICAL Load-bearing vibes: "The Futurists command others to 'lift
  up their heads'" supports 4 downstream claims

CRITICAL Load-bearing vibes: "Art can be nothing but violence,
  cruelty, and injustice" supports 4 downstream claims

CRITICAL Load-bearing vibes: "Italy has for too long been a dealer
  in second-hand clothes" supports 3 downstream claims

WARNING Bottleneck (centrality 1363): "We stand on the last
  promontory of the centuries" [vibes] — many reasoning paths
  flow through this claim

WARNING Bottleneck (centrality 928): "The Futurists are the revival
  and extension of their ancestors" [vibes]

WARNING Unchallenged chain (7 claims): What is there to see in an
  old picture → Admiring an old picture is the same as → An annual
  pilgrimage to museums → Museums are cemeteries → Italy is covered
  by numberless museums → Italy has for too long been a dealer in
  second-hand clothes → We will destroy the museums, libraries,
  and academies
```

Forty-five of fifty-three claims tagged vibes (85%). Every bottleneck in the graph is a vibes-basis claim — no load-bearing deductions, no load-bearing research citations. The seven-claim unchallenged chain threads through the manifesto's core anti-museum argument without encountering a single challenge, empirical claim, or citation. Nothing in the extraction rests on anything verifiable. That is the structural signature of a manifesto, and the tool renders it visible.

</details>

<details>
<summary><b>Appendix: Slimemold on Sokal's "Transgressing the Boundaries" (1996)</b></summary>

We fed [`examples/documents/sokal-social-text-1996.md`](examples/documents/sokal-social-text-1996.md) to `slimemold ingest`. 234 claims, 420 edges. The Works Cited and Notes sections are skipped by the chunker since they contain only bibliography, not argument.

```
SLIMEMOLD [demo-sokal] — 234 claims, 420 edges
  Basis: research=63, vibes=154, definition=11, analogy=3,
         deduction=3

CRITICAL Load-bearing vibes: "Feminist and poststructuralist
  critiques have demystified the substantive content of mainstream
  Western scientific practice" supports 6 downstream claims

CRITICAL Load-bearing vibes: "In the 1980s, string theory became
  popular: here the fundamental entities of physics are not..."
  supports 5 downstream claims

CRITICAL Load-bearing vibes: "Quantum mechanics has four important
  aspects: uncertainty, complementarity, discontinuity, and
  interconnectedness" supports 4 downstream claims

CRITICAL Load-bearing vibes: "Quantum gravity problematizes the
  objective existence of space-time manifolds" supports 4 claims

CRITICAL Load-bearing vibes: "Chaos theory provides our deepest
  insights into the ubiquitous yet unpredictable..." supports 4

CRITICAL Load-bearing vibes: "The infinite-dimensional invariance
  group of general relativity..." supports 4 downstream claims

WARNING Bottleneck (centrality 13434): "Deep conceptual shifts
  within twentieth-century science have undermined..." [vibes]

WARNING Bottleneck (centrality 13238): "Physical 'reality', no
  less than social 'reality', is at bottom a social and linguistic
  construct" [vibes]

WARNING Bottleneck (centrality 11674): "Feminist and poststructuralist
  critiques have demystified..." [vibes]

WARNING Unchallenged chain (26 claims): The images of future
  mathematics → Fuzzy systems theory, catastrophe theory →
  As yet no emancipatory mathematics exists → A liberatory science
  cannot be complete → The fundamental goal of any emancipatory
  movement → Part of the progressive project → The content and
  methodology of postmodern science → The postmodern sciences
  deconstruct → The infinite-dimensional invariance group →
  Diffeomorphisms are self-mappings → Derrida's observation about
  the Einsteinian constant → At a celebrated symposium on Les
  Langages Critiques → General relativity has had a profound →
  General relativity forces upon us radically → Gödel constructed
  an Einstein space-time → General relativity predicts the bending
  → Einstein's general relativity subsumes → Newton's gravitational
  theory corresponds → Einstein's equations are highly nonlinear →
  In Einstein's general theory → Deep conceptual shifts within
  twentieth-century science
```

Sixty-three claims tagged `research` — more citation density than most real papers. Sokal's hoax was *designed* to look rigorously sourced. But the structurally load-bearing claims — the ones other claims depend on — are overwhelmingly `vibes`: rhetorical synthesis statements about "postmodern science," "emancipatory mathematics," "the progressive political project." The three highest-centrality bottlenecks in the entire graph are unsourced grand claims that the rest of the argument flows through. The twenty-six-claim unchallenged chain threads from Sokal's "emancipatory mathematics" framing through Derrida's invocation of Einstein all the way to the paper's closing thesis without a single challenge or verifying edge — the citation-dense surface never actually intersects with the argument-bearing structure. The tool sees the hoax's exact mechanism: pad the page with real citations, carry the argument on vibes.

</details>

<details>
<summary><b>Appendix: Slimemold's audit of this README</b></summary>

We fed this README to `slimemold ingest`. 266 claims, 566 edges.

```
SLIMEMOLD TOPOLOGY AUDIT [demo-readme-v6] — 266 claims, 566 edges
  Basis: vibes=194, definition=23, research=21, deduction=14,
         analogy=11, assumption=2, convention=1

CRITICAL Load-bearing vibes: "Slimemold was run on the Lemoine/LaMDA
  transcript and extracted 40 claims and 51 edges" supports 7
  downstream claims

CRITICAL Load-bearing vibes: "Every few turns, Slimemold extracts
  claims from the conversation" supports 7 downstream claims

CRITICAL Load-bearing vibes: "The hook applies three filters before
  surfacing a priority finding" supports 6 downstream claims

CRITICAL Load-bearing vibes: "Slimemold's graph accumulates per-
  project across days" supports 5 downstream claims

CRITICAL Load-bearing vibes: "The model (Claude) agrees with
  unsourced claims made by users" supports 5 downstream claims

WARNING Bottleneck (centrality 9620): "Slimemold watches
  conversations as they happen, extracts the claims being made,
  builds a persistent graph" [definition] — many reasoning paths
  flow through this claim

WARNING Bottleneck (centrality 9402): "Slimemold is a sycophantic
  tool for preventing worse sycophancy" [vibes]

WARNING Bottleneck (centrality 8203): "Slimemold runs structural
  analysis on the resulting claim graph" [vibes]

WARNING Unchallenged chain (18 claims): Slimemold cannot answer
  whether v5 flag rates are accurate → Whether the SQLite WAL
  failure is a load-bearing problem → The diagnostic showed the
  problem in the SQLite WAL case → Slimemold had flagged the WAL
  files assertion → When run on its own development conversations
  → The human acted on the flagged WAL assertion → The model does
  not know when it is wrong → Language models are trained to
  minimize prediction loss → The sycophancy problem is probably
  worse → RLHF training makes models agreeable → In 391,562
  messages from 19 users who reported harm → Sycophancy was the
  load-bearing mechanism → The model (Claude) agrees with unsourced
  claims → Unmitigated sycophancy in Claude Code → The sycophancy
  pattern is recursive → After agreeing claims are unsourced →
  After agreeing with unsourced claims → Slimemold is a sycophantic
  tool for preventing worse
```

Three captures, three prompt versions:

- v4 (earlier README, prompt v4): 265 claims, 476 edges, vibes=66%,
  definition=43, longest chain=15.
- v5 (current README, prompt v5 with Moore et al. inventory flags
  added): 242 claims, 535 edges, vibes=76%, definition=10, longest
  chain=25. Sonnet emitted `basis="document"` once in sixteen chunks
  during this run — a category error confusing the basis enum with
  the speaker enum, presumably under prompt-length pressure from the
  new inventory-flag section.
- v6 (current README, prompt v6 = v5 + an explicit "basis is one of
  {…}, never a speaker value" hard constraint): 266 claims, 566
  edges, vibes=73%, definition=23, longest chain=18. Zero coerced
  bases in sixteen chunks.

Two layers of defense ship together. The runtime layer
(`coerceBasis` in `internal/mcp/ingest.go`) lowercases out-of-enum
basis values, maps them against the closed list, and falls back to
a speaker-appropriate default ("vibes" for document/user,
"llm_output" for assistant) with a stderr log so drift stays
visible. The prompt layer (the v6 HARD CONSTRAINT line) is intended
to reduce how often the runtime layer has to fire. v6 fired the
runtime layer zero times on this README's sixteen chunks vs one
time under v5 — but that is one run per version, and Sonnet's
sampling variance is wide enough that we cannot honestly attribute
the change to the prompt fix versus run-to-run noise. We did not
do the experiment that would settle it (5–10 runs per version is
~$3 in API tokens). Both interventions are independently defensible
regardless: the prompt addition explicitly forbids the failure mode,
and the runtime layer catches whatever the prompt cannot.

The longest unchallenged chain measured 15 claims under v4, 25
under v5, and 18 under v6. The chain detector traverses claims
regardless of basis — only the explicit `Challenged` flag and
incoming Contradicts/Questions edges break the chain — so the
variation tracks edge structure and content, not the basis-
classification shifts. Edge density per claim was 1.80 (v4), 2.21
(v5), 2.13 (v6); v5's denser graph is the most plausible source of
its longer chain, but again, one run per version. Some of the
length is real new prose: the Moore et al. write-up and the live-
7-turn-experiment section haven't had their own grounding passes.
"The model (Claude) agrees with unsourced claims made by users" is
the new hub of the chain (supporting five downstream claims), and
the Lemoine path is still a critical load-bearer (seven downstream,
up from five in v4). The mechanism claims about sycophancy have
citations (Perez 2022, Sharma 2023, Moore 2026); the narrative
claims about the live 7-turn experiment remain vibes by
construction — it's a diagnostic, not a study. The tool catches
the chain. We make the judgment calls. As the essay grows, the
chain moves with it.

(Numbers captured under extraction prompt version 6 with model
`claude-sonnet-4-6`. We have not characterized run-to-run sampling
variance, so a fresh ingest at the same prompt version may produce
different counts; the numbers above should be read as one
observation each, not a stable estimate.)

</details>
