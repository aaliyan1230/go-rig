package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/aaliyan1230/go-rig/internal/agent"
	"github.com/aaliyan1230/go-rig/internal/env"
	"github.com/aaliyan1230/go-rig/internal/message"
)

var editSchema = json.RawMessage(`{
	"type":"object",
	"properties":{
		"path":{"type":"string","description":"Path to the UTF-8 text file to edit, relative to the working directory or absolute."},
		"oldText":{"type":"string","description":"Exact text to replace once in the original file."},
		"newText":{"type":"string","description":"Replacement text; an empty string deletes the matched text."},
		"edits":{
			"type":"array",
			"description":"One or more exact replacements, all matched against the original file.",
			"items":{
				"type":"object",
				"properties":{
					"oldText":{"type":"string","description":"Exact text to replace once."},
					"newText":{"type":"string","description":"Replacement text; empty is allowed."}
				},
				"required":["oldText","newText"],
				"additionalProperties":false
			}
		}
	},
	"required":["path"],
	"additionalProperties":false
}`)

type editOperation struct {
	OldText *string `json:"oldText"`
	NewText *string `json:"newText"`
}

type editArgs struct {
	Path    string           `json:"path"`
	OldText *string          `json:"oldText"`
	NewText *string          `json:"newText"`
	Edits   *[]editOperation `json:"edits"`
}

type replacement struct {
	start   int
	end     int
	newText string
	index   int
}

// EditTool performs exact, validated text replacements. Every match is found
// in the original file before any replacement is applied, so a failed batch
// cannot leave behind a half-edited file.
type EditTool struct {
	env env.Env
}

var _ agent.Tool = (*EditTool)(nil)

func NewEditTool(environment env.Env) *EditTool {
	return &EditTool{env: environment}
}

func (t *EditTool) ToolName() string { return "edit" }

func (t *EditTool) ToolDescription() string {
	return "Edit one UTF-8 text file using exact unique text replacement. Supply oldText/newText for one replacement or edits for a batch; all matches must be unique and non-overlapping in the original file."
}

func (t *EditTool) ToolSchema() json.RawMessage { return editSchema }

func (t *EditTool) Execute(ctx context.Context, _ string, params json.RawMessage) (agent.ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return agent.ToolResult{}, err
	}

	args, operations, err := decodeEditArgs(params)
	if err != nil {
		return agent.ToolResult{}, err
	}
	path := strings.TrimSpace(args.Path)
	if path == "" {
		return agent.ToolResult{}, fmt.Errorf("invalid edit arguments: path is required")
	}
	for index, operation := range operations {
		oldText := *operation.OldText
		newText := *operation.NewText
		if oldText == "" {
			return agent.ToolResult{}, fmt.Errorf("edit %q: replacement %d oldText must be non-empty", path, index+1)
		}
		if !utf8.ValidString(oldText) || !utf8.ValidString(newText) {
			return agent.ToolResult{}, fmt.Errorf("edit %q: replacement %d contains invalid UTF-8", path, index+1)
		}
	}

	absPath := resolvePath(t.env, path)
	originalBytes, err := t.env.ReadFile(ctx, absPath)
	if err != nil {
		return agent.ToolResult{}, fmt.Errorf("edit %q: read file: %w", path, err)
	}
	if err := ctx.Err(); err != nil {
		return agent.ToolResult{}, err
	}
	if !utf8.Valid(originalBytes) {
		return agent.ToolResult{}, fmt.Errorf("edit %q: file is not valid UTF-8 text", path)
	}
	original := string(originalBytes)

	replacements := make([]replacement, 0, len(operations))
	for index, operation := range operations {
		oldText := *operation.OldText
		newText := *operation.NewText
		matches := findAllMatches(original, oldText)
		if len(matches) != 1 {
			return agent.ToolResult{}, fmt.Errorf("edit %q: replacement %d oldText must match exactly once (found %d matches)", path, index+1, len(matches))
		}
		replacements = append(replacements, replacement{
			start:   matches[0].start,
			end:     matches[0].end,
			newText: newText,
			index:   index,
		})
	}

	if err := validateReplacementRanges(replacements, path); err != nil {
		return agent.ToolResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return agent.ToolResult{}, err
	}

	sort.Slice(replacements, func(i, j int) bool {
		return replacements[i].start > replacements[j].start
	})
	updated := original
	for _, change := range replacements {
		updated = updated[:change.start] + change.newText + updated[change.end:]
	}
	if err := ctx.Err(); err != nil {
		return agent.ToolResult{}, err
	}
	if err := t.env.WriteFile(ctx, absPath, []byte(updated), 0o644); err != nil {
		return agent.ToolResult{}, fmt.Errorf("edit %q: write file: %w", path, err)
	}
	if err := ctx.Err(); err != nil {
		return agent.ToolResult{}, err
	}

	return agent.ToolResult{Content: []message.ContentBlock{message.TextContent{
		Text: fmt.Sprintf("applied %d replacement(s) to %s", len(replacements), path),
	}}}, nil
}

func decodeEditArgs(params json.RawMessage) (editArgs, []editOperation, error) {
	var args editArgs
	if err := decodeToolArgs(params, "edit", &args); err != nil {
		return editArgs{}, nil, err
	}

	singleProvided := jsonFieldPresent(params, "oldText") || jsonFieldPresent(params, "newText")
	batchProvided := jsonFieldPresent(params, "edits")
	if singleProvided && batchProvided {
		return editArgs{}, nil, fmt.Errorf("invalid edit arguments: supply either oldText/newText or edits, not both")
	}
	if !singleProvided && !batchProvided {
		return editArgs{}, nil, fmt.Errorf("invalid edit arguments: supply oldText and newText, or a non-empty edits array")
	}
	if singleProvided {
		if args.OldText == nil || args.NewText == nil {
			return editArgs{}, nil, fmt.Errorf("invalid edit arguments: oldText and newText must both be strings")
		}
		return args, []editOperation{{OldText: args.OldText, NewText: args.NewText}}, nil
	}
	if args.Edits == nil {
		return editArgs{}, nil, fmt.Errorf("invalid edit arguments: edits must be an array")
	}
	if len(*args.Edits) == 0 {
		return editArgs{}, nil, fmt.Errorf("invalid edit arguments: edits must contain at least one replacement")
	}
	for index, operation := range *args.Edits {
		if operation.OldText == nil || operation.NewText == nil {
			return editArgs{}, nil, fmt.Errorf("invalid edit arguments: edits[%d] oldText and newText must both be strings", index)
		}
	}
	return args, *args.Edits, nil
}

type matchRange struct {
	start int
	end   int
}

func findAllMatches(text, target string) []matchRange {
	if target == "" {
		return nil
	}
	matches := make([]matchRange, 0, 1)
	for start := 0; start <= len(text)-len(target); {
		relative := strings.Index(text[start:], target)
		if relative < 0 {
			break
		}
		matchStart := start + relative
		matches = append(matches, matchRange{start: matchStart, end: matchStart + len(target)})
		// Advance by one byte rather than len(target) so overlapping occurrences
		// (for example "aa" in "aaa") are counted and rejected.
		start = matchStart + 1
	}
	return matches
}

func validateReplacementRanges(replacements []replacement, path string) error {
	ordered := append([]replacement(nil), replacements...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].start == ordered[j].start {
			return ordered[i].end < ordered[j].end
		}
		return ordered[i].start < ordered[j].start
	})
	for i := 1; i < len(ordered); i++ {
		previous, current := ordered[i-1], ordered[i]
		if current.start < previous.end {
			return fmt.Errorf("edit %q: replacements %d and %d overlap or are nested", path, previous.index+1, current.index+1)
		}
	}
	return nil
}
