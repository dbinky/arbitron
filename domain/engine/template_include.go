package engine

import (
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/google/uuid"
)

// ErrTemplateInclude is returned when a template's {{include:...}} directive
// cannot be expanded. Callers match it with errors.Is.
var ErrTemplateInclude = errors.New("engine: template include")

const (
	// includeOpen/includeClose delimit an include directive:
	// {{include:_common.md}}. The colon is what separates it from an ordinary
	// {{dot.path}} variable, which never contains one.
	includeOpen  = "{{include:"
	includeClose = "}}"

	// maxIncludeDepth bounds nesting. A shared preamble is one level deep in
	// practice; anything beyond this is a mistake, and the bound guarantees
	// termination even if cycle detection is ever weakened.
	maxIncludeDepth = 8

	// maxIncludeBytes and maxIncludeCount bound the TOTAL work one expansion may
	// do. Depth and cycle checks alone do not: the same file may legitimately
	// expand more than once per level, so fan-out multiplies at every level and
	// <1 KB of templates can expand to gigabytes ("billion laughs") inside the
	// shared server process. Both caps sit far above any real prompt
	// (_common.md is ~4 KB, included once per persona).
	maxIncludeBytes = 2 << 20 // 2 MiB of included content, summed across every read
	maxIncludeCount = 64      // include directives resolved, summed across every level
)

// includeBudget is the remaining allowance for one top-level expansion. It is
// shared (by pointer) across every recursive branch so fan-out is charged in
// full, not per branch.
type includeBudget struct {
	bytes    int
	includes int
}

// expandIncludes replaces every {{include:<relative/path>}} directive in content
// with the referenced file's contents, recursively.
//
// WHY THIS EXISTS: a node receives only its OWN templateFile. Templates that
// shared a preamble by referring the agent to a sibling file ("use the patterns
// in _common.md") were naming a file the agent never received — the instructions
// silently arrived half-complete, and nothing about the run looked wrong. One
// pr-review persona eventually posted nothing at all because the words
// "Read-only" reached it without the sentence in _common.md that defines them.
// An include is resolved HERE, by the engine, so the agent gets one complete
// prompt.
//
// Ordering: expansion happens BEFORE variable substitution, so an included
// file's own {{vars}} resolve exactly as if they had been written inline.
//
// FAIL-CLOSED: a missing or unreadable include fails the node. Dropping it
// silently would reproduce the very defect this closes — an agent running on
// half its brief with no signal that anything is missing.
//
// SYNTAX: a directive is recognized ANYWHERE in the template text and there is
// no escape. A template that merely mentions the syntax in prose (e.g. a
// prompt teaching an agent how to write templates) is read as a real directive
// and, being fail-closed, fails the node on a file that was never meant to
// exist. Adding an escape later must be a deliberate change; a test pins this.
//
// SECURITY: paths are resolved through the FileReader port, whose adapter
// confines every read to the workflow's own directory, so an include cannot
// escape it or reach another workflow's files. Total expansion is bounded by
// maxIncludeBytes and maxIncludeCount so a small fan-out graph cannot exhaust
// server memory; exceeding either fails closed with ErrTemplateInclude.
func expandIncludes(fr FileReader, owner uuid.UUID, workflowName, content string) (string, error) {
	budget := &includeBudget{bytes: maxIncludeBytes, includes: maxIncludeCount}
	return expandIncludesDepth(fr, owner, workflowName, content, nil, 0, budget)
}

// expandIncludesDepth carries the active include stack so a cycle is reported
// against the path that closes it, the depth so nesting stays bounded, and the
// shared budget so total output stays bounded.
func expandIncludesDepth(
	fr FileReader, owner uuid.UUID, workflowName, content string, stack []string, depth int,
	budget *includeBudget,
) (string, error) {
	if !strings.Contains(content, includeOpen) {
		return content, nil // fast path: the overwhelming majority of templates
	}
	if depth > maxIncludeDepth {
		return "", fmt.Errorf("%w: nesting exceeded %d levels via %s",
			ErrTemplateInclude, maxIncludeDepth, strings.Join(stack, " -> "))
	}

	var b strings.Builder
	b.Grow(len(content))
	rest := content
	for {
		start := strings.Index(rest, includeOpen)
		if start == -1 {
			b.WriteString(rest)
			break
		}
		b.WriteString(rest[:start])
		tail := rest[start+len(includeOpen):]

		end := strings.Index(tail, includeClose)
		if end == -1 {
			return "", fmt.Errorf("%w: unterminated directive (missing %q)", ErrTemplateInclude, includeClose)
		}
		incPath := strings.TrimSpace(tail[:end])
		rest = tail[end+len(includeClose):]

		if incPath == "" {
			return "", fmt.Errorf("%w: directive names no file", ErrTemplateInclude)
		}
		// Key the cycle check on the cleaned path so a.md, ./a.md and
		// sub/../a.md are recognized as the same file.
		key := path.Clean(incPath)
		for _, seen := range stack {
			if seen == key {
				return "", fmt.Errorf("%w: cycle %s -> %s",
					ErrTemplateInclude, strings.Join(stack, " -> "), key)
			}
		}

		budget.includes--
		if budget.includes < 0 {
			return "", fmt.Errorf("%w: more than %d includes resolved (at %q)",
				ErrTemplateInclude, maxIncludeCount, incPath)
		}
		raw, err := fr.ReadFile(owner, workflowName, incPath)
		if err != nil {
			return "", fmt.Errorf("%w: reading %q: %w", ErrTemplateInclude, incPath, err)
		}
		// Charge the read BEFORE recursing. Output is at most the top-level
		// content plus every included read, so this bounds the result too.
		budget.bytes -= len(raw)
		if budget.bytes < 0 {
			return "", fmt.Errorf("%w: expansion exceeded %d bytes (at %q)",
				ErrTemplateInclude, maxIncludeBytes, incPath)
		}
		// The stack is per-BRANCH, not global: the same file included twice at
		// the same level is repetition, not a cycle, and must expand both times.
		nested, err := expandIncludesDepth(fr, owner, workflowName, string(raw), append(stack, key), depth+1, budget)
		if err != nil {
			return "", err
		}
		b.WriteString(nested)
	}
	return b.String(), nil
}

// loadTemplate reads a node's prompt file and expands its includes. It is the
// ONE place the four template-driven executors (agent, llm, interactive, spawn)
// read a prompt — including the interactive executor's orchestration file — so
// an include behaves identically on every node kind and every prompt file.
func loadTemplate(fr FileReader, owner uuid.UUID, workflowName, templateFile string) (string, error) {
	raw, err := fr.ReadFile(owner, workflowName, templateFile)
	if err != nil {
		return "", err
	}
	return expandIncludes(fr, owner, workflowName, string(raw))
}
