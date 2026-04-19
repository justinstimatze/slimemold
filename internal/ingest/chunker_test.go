package ingest

import (
	"strings"
	"testing"
)

func TestChunkMarkdownHeadings(t *testing.T) {
	content := `# Title

Intro paragraph under title.

## Section A

Section A first paragraph.

Section A second paragraph.

## Section B

Section B paragraph.

### Subsection B.1

Subsection content.
`
	chunks := Chunk(content, 0)
	if len(chunks) < 3 {
		t.Fatalf("expected at least 3 chunks, got %d", len(chunks))
	}

	foundA := false
	foundB1 := false
	for _, c := range chunks {
		if len(c.HeadingPath) >= 2 && c.HeadingPath[1] == "Section A" {
			foundA = true
		}
		if len(c.HeadingPath) >= 3 && c.HeadingPath[2] == "Subsection B.1" {
			foundB1 = true
		}
	}
	if !foundA {
		t.Errorf("missing chunk under 'Section A'")
	}
	if !foundB1 {
		t.Errorf("missing chunk under 'Subsection B.1'")
	}
}

func TestChunkPlainTextParagraphMerge(t *testing.T) {
	para := strings.Repeat("Word ", 400) // ~2000 chars per paragraph
	content := para + "\n\n" + para + "\n\n" + para + "\n\n" + para

	// Very small budget to force multiple chunks at paragraph boundaries.
	chunks := Chunk(content, 3000)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks with small budget, got %d", len(chunks))
	}
	for i, c := range chunks {
		if len(c.Text) > 3500 { // budget + one paragraph slop
			t.Errorf("chunk %d exceeds budget by too much: %d chars", i, len(c.Text))
		}
		if len(c.HeadingPath) != 0 {
			t.Errorf("plain text chunk %d should have empty heading path, got %v", i, c.HeadingPath)
		}
	}
}

func TestChunkStripsYAMLFrontmatter(t *testing.T) {
	content := `---
name: test
description: should be stripped
---

# Actual Title

Body.
`
	chunks := Chunk(content, 0)
	for _, c := range chunks {
		if strings.Contains(c.Text, "description: should be stripped") {
			t.Errorf("YAML frontmatter leaked into chunk text: %s", c.Text)
		}
	}
}

func TestChunkStripsAttributionHeader(t *testing.T) {
	content := `# Document Title

**Author:** Some Author
**Source:** Somewhere
**Status:** Public domain

---

First body paragraph here.

Second body paragraph here.
`
	chunks := Chunk(content, 0)
	if len(chunks) == 0 {
		t.Fatal("expected at least one chunk")
	}
	for _, c := range chunks {
		if strings.Contains(c.Text, "**Author:**") {
			t.Errorf("attribution header leaked into chunk text")
		}
	}
}

func TestChunkContentHashStable(t *testing.T) {
	content := "# T\n\nSome body text.\n"
	a := Chunk(content, 0)
	b := Chunk(content, 0)
	if len(a) != len(b) {
		t.Fatalf("chunk count differs across runs: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i].ContentHash != b[i].ContentHash {
			t.Errorf("chunk %d content hash changed across runs", i)
		}
	}
}

func TestChunkOversizedParagraphHardSplit(t *testing.T) {
	// Single paragraph larger than budget, multiple sentences.
	long := strings.Repeat("This is a sentence. ", 500) // ~10K chars, one paragraph
	chunks := Chunk(long, 3000)
	if len(chunks) < 2 {
		t.Fatalf("expected hard-split into multiple chunks, got %d", len(chunks))
	}
}
