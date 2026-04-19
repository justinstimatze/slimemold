// Package ingest turns authored documents into chunks suitable for claim extraction.
package ingest

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

// DocumentChunk is one extractable section of a document, with enough
// structural context that the extractor can attribute claims correctly.
type DocumentChunk struct {
	Text        string
	HeadingPath []string // section titles from root to this chunk's leaf, empty for plain text
	ChunkIndex  int
	ContentHash string // sha256 of Text, for caching
}

// DefaultMaxChars is the soft target for chunk size. 12K chars ≈ 3K tokens
// leaves comfortable room under the extractor's 200K input and 16K output budget.
const DefaultMaxChars = 12000

var atxHeading = regexp.MustCompile(`^(#{1,6})\s+(.+?)\s*$`)

// Chunk splits a document into extractable chunks. If the content has markdown
// ATX headings, it chunks along section boundaries and subdivides large sections
// at paragraph boundaries. Otherwise it falls back to paragraph-boundary greedy
// merge with no heading path.
func Chunk(content string, maxChars int) []DocumentChunk {
	if maxChars <= 0 {
		maxChars = DefaultMaxChars
	}

	content = stripFrontmatter(content)

	if hasHeadings(content) {
		return chunkByHeadings(content, maxChars)
	}
	return chunkByParagraphs(content, nil, maxChars, 0)
}

// stripFrontmatter removes a YAML or attribution header from the document start.
// We accept two forms: a fenced YAML block (`---\n...\n---`) and an attribution
// block that ends in a horizontal rule (`---`) preceded by bold key-value lines.
// The goal is to avoid extracting claims from the file's own metadata header.
func stripFrontmatter(content string) string {
	trimmed := strings.TrimLeft(content, "\n")

	if strings.HasPrefix(trimmed, "---\n") || strings.HasPrefix(trimmed, "---\r\n") {
		rest := trimmed[4:]
		if idx := strings.Index(rest, "\n---"); idx >= 0 {
			end := idx + 4
			if end < len(rest) {
				return strings.TrimLeft(rest[end:], "\n")
			}
			return ""
		}
	}

	// Heuristic: attribution block at the top ending with a `---` line. Only
	// triggers if the first non-blank line is **Author:** or **Title:** style.
	lines := strings.Split(trimmed, "\n")
	if len(lines) > 0 && looksLikeAttributionHeader(lines) {
		for i, line := range lines {
			if strings.TrimSpace(line) == "---" {
				return strings.Join(lines[i+1:], "\n")
			}
		}
	}
	return content
}

func looksLikeAttributionHeader(lines []string) bool {
	for _, line := range lines[:min(10, len(lines))] {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "**") && strings.Contains(trimmed, ":**") {
			return true
		}
		return false
	}
	return false
}

func hasHeadings(content string) bool {
	for _, line := range strings.Split(content, "\n") {
		if atxHeading.MatchString(line) {
			return true
		}
	}
	return false
}

// chunkByHeadings walks the document section by section, tracking the heading
// path. Each section becomes one chunk unless it exceeds maxChars, in which
// case the section body is subdivided at paragraph boundaries (preserving the
// heading path across the resulting chunks).
func chunkByHeadings(content string, maxChars int) []DocumentChunk {
	var out []DocumentChunk
	lines := strings.Split(content, "\n")

	// headingStack[i] holds the current heading at depth i+1. Level-1 heading
	// replaces the whole stack; level-n heading truncates to depth n-1 first.
	var headingStack []string
	var body []string
	chunkIdx := 0

	flush := func() {
		if len(body) == 0 {
			return
		}
		text := strings.TrimSpace(strings.Join(body, "\n"))
		body = body[:0]
		if text == "" {
			return
		}

		pathCopy := append([]string(nil), headingStack...)
		subs := subdivideIfLarge(text, pathCopy, maxChars, chunkIdx)
		chunkIdx += len(subs)
		out = append(out, subs...)
	}

	for _, line := range lines {
		if m := atxHeading.FindStringSubmatch(line); m != nil {
			flush()
			depth := len(m[1])
			title := m[2]
			if depth-1 < len(headingStack) {
				headingStack = headingStack[:depth-1]
			}
			for len(headingStack) < depth-1 {
				headingStack = append(headingStack, "")
			}
			headingStack = append(headingStack, title)
			continue
		}
		body = append(body, line)
	}
	flush()
	return out
}

// subdivideIfLarge returns either the section as a single chunk or, if the
// section is larger than maxChars, multiple paragraph-merged subchunks that
// share the same heading path.
func subdivideIfLarge(text string, path []string, maxChars, startIdx int) []DocumentChunk {
	if len(text) <= maxChars {
		return []DocumentChunk{makeChunk(text, path, startIdx)}
	}
	return chunkByParagraphs(text, path, maxChars, startIdx)
}

// chunkByParagraphs greedy-merges paragraphs up to maxChars per chunk. A
// "paragraph" is text separated by a blank line; single paragraphs larger than
// maxChars are hard-split at sentence boundaries as a last resort.
func chunkByParagraphs(text string, path []string, maxChars, startIdx int) []DocumentChunk {
	paragraphs := splitParagraphs(text)
	var out []DocumentChunk
	var buf []string
	bufLen := 0
	idx := startIdx

	commit := func() {
		if bufLen == 0 {
			return
		}
		joined := strings.TrimSpace(strings.Join(buf, "\n\n"))
		buf = buf[:0]
		bufLen = 0
		if joined == "" {
			return
		}
		out = append(out, makeChunk(joined, path, idx))
		idx++
	}

	for _, para := range paragraphs {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}

		// Oversized paragraph: hard-split.
		if len(para) > maxChars {
			commit()
			for _, piece := range hardSplitSentences(para, maxChars) {
				out = append(out, makeChunk(piece, path, idx))
				idx++
			}
			continue
		}

		// Would adding this paragraph exceed the budget? Commit first.
		if bufLen > 0 && bufLen+len(para)+2 > maxChars {
			commit()
		}
		buf = append(buf, para)
		bufLen += len(para) + 2
	}
	commit()
	return out
}

// splitParagraphs splits on blank-line boundaries, preserving internal newlines
// within each paragraph (e.g., for list items or quoted blocks).
func splitParagraphs(text string) []string {
	var out []string
	var cur []string
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "" {
			if len(cur) > 0 {
				out = append(out, strings.Join(cur, "\n"))
				cur = nil
			}
			continue
		}
		cur = append(cur, line)
	}
	if len(cur) > 0 {
		out = append(out, strings.Join(cur, "\n"))
	}
	return out
}

// hardSplitSentences splits a single oversized paragraph at sentence boundaries
// to keep each piece under maxChars. Used only as a fallback for pathologically
// long paragraphs — most prose won't trigger it.
func hardSplitSentences(para string, maxChars int) []string {
	sentences := splitSentences(para)
	var out []string
	var buf strings.Builder
	for _, s := range sentences {
		if buf.Len() > 0 && buf.Len()+len(s) > maxChars {
			out = append(out, strings.TrimSpace(buf.String()))
			buf.Reset()
		}
		// Single sentence longer than budget: emit it standalone, oversize.
		// We prefer a too-big chunk to a mid-sentence cut.
		if len(s) > maxChars {
			if buf.Len() > 0 {
				out = append(out, strings.TrimSpace(buf.String()))
				buf.Reset()
			}
			out = append(out, s)
			continue
		}
		if buf.Len() > 0 {
			buf.WriteByte(' ')
		}
		buf.WriteString(s)
	}
	if buf.Len() > 0 {
		out = append(out, strings.TrimSpace(buf.String()))
	}
	return out
}

var sentenceEnd = regexp.MustCompile(`([.!?])\s+`)

func splitSentences(para string) []string {
	idxs := sentenceEnd.FindAllStringIndex(para, -1)
	if len(idxs) == 0 {
		return []string{para}
	}
	var out []string
	start := 0
	for _, loc := range idxs {
		end := loc[0] + 1 // include the punctuation
		out = append(out, para[start:end])
		start = loc[1]
	}
	if start < len(para) {
		out = append(out, para[start:])
	}
	return out
}

func makeChunk(text string, path []string, idx int) DocumentChunk {
	sum := sha256.Sum256([]byte(text))
	return DocumentChunk{
		Text:        text,
		HeadingPath: path,
		ChunkIndex:  idx,
		ContentHash: hex.EncodeToString(sum[:]),
	}
}
