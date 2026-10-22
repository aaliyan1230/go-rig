package tools

import "github.com/aaliyan1230/go-rig/internal/prompt"

// System prompt contributions for the built-in tools. Snippets are one-line
// summaries for the prompt's tool list; guidelines become rules bullets. The
// longer ToolDescription text is sent separately with each tool's schema.

var (
	_ prompt.Contributor = (*ReadTool)(nil)
	_ prompt.Contributor = (*WriteTool)(nil)
	_ prompt.Contributor = (*EditTool)(nil)
	_ prompt.Contributor = (*BashTool)(nil)
)

func (t *ReadTool) PromptSnippet() string { return "Read file contents" }

func (t *ReadTool) PromptGuidelines() []string {
	return []string{"Use read to examine files instead of cat or sed."}
}

func (t *WriteTool) PromptSnippet() string { return "Create or overwrite files" }

func (t *WriteTool) PromptGuidelines() []string {
	return []string{"Use write only for new files or complete rewrites."}
}

func (t *EditTool) PromptSnippet() string {
	return "Make precise file edits with exact text replacement, including multiple disjoint edits in one call"
}

func (t *EditTool) PromptGuidelines() []string {
	return []string{
		"Use edit for precise changes (oldText must match exactly)",
		"When changing multiple separate locations in one file, use one edit call with multiple entries in edits instead of multiple edit calls",
		"Each oldText is matched against the original file, not after earlier edits are applied. Do not emit overlapping or nested edits. Merge nearby changes into one edit.",
		"Keep oldText as small as possible while still being unique in the file. Do not pad with large unchanged regions.",
	}
}

func (t *BashTool) PromptSnippet() string { return "Execute bash commands (ls, grep, find, etc.)" }

func (t *BashTool) PromptGuidelines() []string { return nil }
