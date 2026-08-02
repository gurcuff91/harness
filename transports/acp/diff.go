package acp

import (
	"encoding/json"
	"os"
)

// pendingEdit tracks the "before" snapshot of a file the Edit tool is about
// to touch, captured at EventToolCall (before the tool actually runs) so
// EventToolResult can pair it with the "after" content and build a diff
// ToolCallContent block. Keyed by tool call ID, since multiple tool calls
// can be in flight concurrently (the ReAct loop runs a turn's tool calls in
// parallel — see agent/session.go).
//
// This is purely observational: it reads the file from disk on the side,
// exactly like a spectator watching the Edit tool work — it never touches
// agent/tools/edit.go, never intercepts or wraps the tool's own execution.
type pendingEdit struct {
	path    string
	oldText string
	hadFile bool // false if the file didn't exist yet (a brand-new file)
}

// capturePreEditState reads a tool call's target file BEFORE the tool runs,
// for later diffing. Only meaningful for tools that take a "path" field and
// mutate that file (currently: Edit, Write). Returns ok=false if toolArgs
// doesn't carry a usable path or the read fails for a reason other than "the
// file doesn't exist yet" (in which case hadFile=false and oldText="" is
// still a valid, useful snapshot — a brand-new file).
func capturePreEditState(toolArgs string) (edit pendingEdit, ok bool) {
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(toolArgs), &args); err != nil || args.Path == "" {
		return pendingEdit{}, false
	}
	edit.path = args.Path
	b, err := os.ReadFile(args.Path)
	if err != nil {
		if !os.IsNotExist(err) {
			return pendingEdit{}, false
		}
		edit.hadFile = false
		return edit, true
	}
	edit.hadFile = true
	edit.oldText = string(b)
	return edit, true
}

// buildDiffContent reads the file AFTER the tool ran and pairs it with the
// pre-edit snapshot to build a "diff" ToolCallContent block. Returns ok=false
// if the post-edit read fails, in which case the caller should fall back to
// a plain text content block instead.
func buildDiffContent(pre pendingEdit) (toolCallContent, bool) {
	newBytes, err := os.ReadFile(pre.path)
	if err != nil {
		return toolCallContent{}, false
	}
	var oldText *string
	if pre.hadFile {
		s := pre.oldText
		oldText = &s
	} // else leave nil — ACP's "oldText: null" means "this is a new file"
	return toolCallContent{
		Type:    "diff",
		Path:    pre.path,
		OldText: oldText,
		NewText: string(newBytes),
	}, true
}
