// Package prompt builds the system prompt sent to the model at the start of
// every request: base instructions, the tool list and rules, project context
// files, and the working directory.
//
// Build is deterministic: the same options always produce the same text.
// There is deliberately no date or other clock input, so a resumed session
// sees the same prompt prefix as the run that created it.
package prompt

import (
	"fmt"
	"strings"

	"github.com/aaliyan1230/go-rig/internal/agent"
)

const defaultPreamble = "You are an expert coding assistant operating inside go-rig, a coding agent harness. You help users by reading files, executing commands, editing code, and writing new files."

// Contributor is an optional interface a tool implements to appear in the
// system prompt. The snippet is a one-line summary for the tool list; the
// guidelines become bullets in the rules section. Both are separate from the
// longer ToolDescription, which the provider already receives with the schema.
type Contributor interface {
	PromptSnippet() string
	PromptGuidelines() []string
}

// ContextFile is a project instruction file such as AGENTS.md.
type ContextFile struct {
	Path    string
	Content string
}

type Options struct {
	// CustomPrompt, when non-empty, replaces the preamble, tool list, and
	// rules. Project context and cwd are still appended.
	CustomPrompt string
	Tools        []agent.Tool
	ContextFiles []ContextFile
	Cwd          string
}

// Build renders the system prompt: an untagged preamble followed by sections
// wrapped in XML-style tags, separated by blank lines. Empty sections are
// omitted.
func Build(opts Options) string {
	parts := make([]string, 0, 5)
	if opts.CustomPrompt != "" {
		parts = append(parts, opts.CustomPrompt)
	} else {
		parts = append(parts,
			defaultPreamble,
			tagged("tools", renderTools(opts.Tools)),
			tagged("rules", renderRules(opts.Tools)),
		)
	}
	if len(opts.ContextFiles) > 0 {
		parts = append(parts, tagged("project_context", renderProjectContext(opts.ContextFiles)))
	}
	if opts.Cwd != "" {
		parts = append(parts, tagged("cwd", strings.ReplaceAll(opts.Cwd, `\`, "/")))
	}
	return strings.Join(parts, "\n\n")
}

func tagged(name, content string) string {
	return fmt.Sprintf("<%s>\n%s\n</%s>", name, content, name)
}

// renderTools lists only tools that provide a snippet. Tools without one are
// still callable; they are simply not advertised here.
func renderTools(tools []agent.Tool) string {
	lines := make([]string, 0, len(tools))
	for _, t := range tools {
		c, ok := t.(Contributor)
		if !ok || c.PromptSnippet() == "" {
			continue
		}
		lines = append(lines, fmt.Sprintf("- %s: %s", t.ToolName(), c.PromptSnippet()))
	}
	list := "(none)"
	if len(lines) > 0 {
		list = strings.Join(lines, "\n")
	}
	return list + "\n\nIn addition to the tools above, you may have access to other custom tools depending on the project."
}

// renderRules collects tool guidelines in tool order, then the fixed rules.
// Duplicates are dropped after trimming so two tools can share a guideline.
func renderRules(tools []agent.Tool) string {
	var rules []string
	seen := make(map[string]bool)
	add := func(rule string) {
		rule = strings.TrimSpace(rule)
		if rule == "" || seen[rule] {
			return
		}
		seen[rule] = true
		rules = append(rules, "- "+rule)
	}

	names := make(map[string]bool, len(tools))
	for _, t := range tools {
		names[t.ToolName()] = true
	}
	if names["bash"] && !names["grep"] && !names["find"] && !names["ls"] {
		add("Use bash for file operations like ls, rg, find")
	}
	for _, t := range tools {
		if c, ok := t.(Contributor); ok {
			for _, g := range c.PromptGuidelines() {
				add(g)
			}
		}
	}
	add("Be concise in your responses")
	add("Show file paths clearly when working with files")
	return strings.Join(rules, "\n")
}

func renderProjectContext(files []ContextFile) string {
	parts := make([]string, 0, len(files)+1)
	parts = append(parts, "Project-specific instructions and guidelines:")
	for _, f := range files {
		parts = append(parts, fmt.Sprintf("<project_instructions path=%q>\n%s\n</project_instructions>", f.Path, f.Content))
	}
	return strings.Join(parts, "\n\n")
}
