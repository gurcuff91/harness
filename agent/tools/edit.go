package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/gurcuff91/harness/types"
)

// editEntry is one replacement in the edits[] array.
type editEntry struct {
	OldText string `json:"old_text"`
	NewText string `json:"new_text"`
}

// editInput accepts two shapes (like PI): a single replacement via the flat
// old_text/new_text fields, or several via the edits[] array. Neither is
// required by the schema; Execute validates that exactly one shape is present
// and folds the flat form into a one-element array.
type editInput struct {
	Path    string      `json:"path"`
	Edits   []editEntry `json:"edits,omitempty"`
	OldText string      `json:"old_text,omitempty"`
	NewText string      `json:"new_text,omitempty"`
}

func Edit() Tool {
	return Tool{
		Def: types.ToolDef{
			Name:        "Edit",
			Description: "Edit a file using exact text replacement. old_text must match a unique region of the file exactly (whitespace and newlines included). For a single change, pass 'old_text' and 'new_text'. To change several places in one file at once, pass an 'edits' array of {old_text, new_text} — each old_text is matched against the ORIGINAL file, so keep them disjoint and non-overlapping; merge nearby changes into one edit. Keep old_text minimal but unique. Prefer this over rewriting the whole file.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path": {"type": "string", "description": "Path to the file to edit"},
					"old_text": {"type": "string", "description": "Single edit: exact text to find (must be unique in the file). Use with new_text."},
					"new_text": {"type": "string", "description": "Single edit: replacement text. Use with old_text."},
					"edits": {
						"type": "array",
						"description": "Multiple edits at once: replacements applied to the original file. Keep them disjoint (non-overlapping); merge nearby changes into a single edit. Use this OR old_text/new_text, not both.",
						"items": {
							"type": "object",
							"properties": {
								"old_text": {"type": "string", "description": "Exact text to find (must be unique in the file)"},
								"new_text": {"type": "string", "description": "Replacement text"}
							},
							"required": ["old_text", "new_text"]
						}
					}
				},
				"required": ["path"]
			}`),
		},
		Execute: func(ctx context.Context, input json.RawMessage) (string, error) {
			var args editInput
			if err := json.Unmarshal(input, &args); err != nil {
				return fmt.Sprintf("Error parsing input: %v", err), err
			}
			// Dual shape: fold a flat old_text/new_text into the edits array. Reject
			// mixing both forms, or supplying neither.
			hasFlat := args.OldText != "" || args.NewText != ""
			hasArray := len(args.Edits) > 0
			switch {
			case hasFlat && hasArray:
				err := fmt.Errorf("provide either old_text/new_text or edits[], not both")
				return err.Error(), err
			case !hasFlat && !hasArray:
				err := fmt.Errorf("provide old_text/new_text for a single edit, or edits[] for multiple")
				return err.Error(), err
			case hasFlat:
				args.Edits = []editEntry{{OldText: args.OldText, NewText: args.NewText}}
			}

			data, err := os.ReadFile(args.Path)
			if err != nil {
				return fmt.Sprintf("Error reading file: %v", err), err
			}

			// Strip BOM, remember line ending, match in LF space, then restore.
			bom, content := stripBOM(string(data))
			ending := detectLineEnding(content)
			normalized := normalizeToLF(content)

			reps := make([]editReplacement, len(args.Edits))
			for i, e := range args.Edits {
				reps[i] = editReplacement{OldText: e.OldText, NewText: e.NewText}
			}

			newNormalized, err := applyEdits(normalized, reps, args.Path)
			if err != nil {
				return err.Error(), err
			}

			final := bom + restoreLineEndings(newNormalized, ending)
			// Preserve the original file mode instead of forcing 0644.
			mode := os.FileMode(0644)
			if info, statErr := os.Stat(args.Path); statErr == nil {
				mode = info.Mode()
			}
			if err := os.WriteFile(args.Path, []byte(final), mode); err != nil {
				return fmt.Sprintf("Error writing file: %v", err), err
			}

			n := len(args.Edits)
			plural := "s"
			if n == 1 {
				plural = ""
			}
			return fmt.Sprintf("Successfully replaced %d block%s in %s.", n, plural, args.Path), nil
		},
	}
}
