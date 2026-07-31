package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gurcuff91/harness/client"
	"github.com/gurcuff91/harness/types"
)

// ── Instance registry reader (minimal, self-contained) ──────────────────────
//
// ~/.harness/instances.json is written by internal/server (RegisterInstance/
// UnregisterInstance, guarded by a cross-process file lock) every time a
// `harness serve`-style process starts/stops. That file — its path and JSON
// shape — is the interop contract, not a shared Go type: these tools read it
// directly with their own tiny parser instead of importing internal/server
// (which agent/tools must never do) or promoting the whole registry
// implementation (name generator, file-locking, liveness probing) to a public
// package just to hand over a struct. If the on-disk shape ever changes,
// update this struct to match — it intentionally only declares the fields
// these tools actually use.

// instanceEntry mirrors the subset of internal/server's InstanceInfo this
// package needs. Extra fields on disk (if any) are ignored by json.Unmarshal.
type instanceEntry struct {
	Version   string `json:"version"`
	Transport string `json:"transport"`
	URL       string `json:"url"`
	CWD       string `json:"cwd"`
	PID       int    `json:"pid"`
}

// readInstances loads ~/.harness/instances.json as-is (no liveness checks —
// that's RegisterInstance's job when a name collides). Missing file or bad
// JSON both yield an empty map, never an error: colleague discovery degrading
// to "no colleagues" is the right failure mode for a tool call, not a hard error.
func readInstances() map[string]instanceEntry {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(home, ".harness", "instances.json"))
	if err != nil {
		return nil
	}
	var instances map[string]instanceEntry
	_ = json.Unmarshal(data, &instances)
	return instances
}

// ── ColleagueList ──────────────────────────────────────────────────────────

// chatPlatformEnvironments are the on-disk transport values that grant a
// colleague a genuinely distinct capability — posting/replying on an external
// chat platform. Everything else (tui, server, cli, and any transport not
// listed here) collapses to "generic": from the model's perspective they're
// all "an agent with tools and a working directory, no special messaging
// capability" — there is nothing operationally different between them worth
// naming individually. Adding a new chat-platform transport (e.g. discord)
// later only means adding one entry here, no system-prompt change needed.
var chatPlatformEnvironments = map[string]bool{
	"slack":    true,
	"telegram": true,
}

// environmentLabel maps an on-disk transport value to what the model sees in
// ColleagueList. Chat platforms keep their name (it's the whole point — the
// model reasons about "a colleague in slack can post to Slack"); anything
// else collapses to "generic".
func environmentLabel(transport string) string {
	if chatPlatformEnvironments[transport] {
		return transport
	}
	return "generic"
}

// colleagueListEntry is what ColleagueList shows the model — just enough to
// pick a name for ColleagueAsk and understand what that colleague can offer.
// JSON key "environment" (not the on-disk field name "transport") is what the
// model reasons over — see the "## Colleagues" system prompt section for why
// this field matters as much as cwd when picking who to delegate to.
type colleagueListEntry struct {
	Name        string `json:"name"`
	Environment string `json:"environment"`
	CWD         string `json:"cwd"`
	Version     string `json:"version"`
}

// ColleagueList returns a Tool that lists other reachable colleague
// instances, excluding the caller itself (matched by PID — the one field
// every registry entry carries that reliably identifies "not me" without any
// extra plumbing).
func ColleagueList() Tool {
	return Tool{
		Def: types.ToolDef{
			Name:        ToolColleagueList,
			Description: "List other colleague instances currently reachable — each is a live agent you can delegate to via ColleagueAsk. Returns each colleague's name (use it with ColleagueAsk), environment, working directory, and version. Returns an empty list if no colleagues are reachable — that's normal, not an error.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		},
		Execute: func(ctx context.Context, input json.RawMessage) (string, error) {
			self := os.Getpid()
			var out []colleagueListEntry
			for name, info := range readInstances() {
				if info.PID == self {
					continue
				}
				out = append(out, colleagueListEntry{
					Name:        name,
					Environment: environmentLabel(info.Transport),
					CWD:         info.CWD,
					Version:     info.Version,
				})
			}
			if len(out) == 0 {
				return "No other colleagues are currently reachable.", nil
			}
			sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
			b, err := json.MarshalIndent(out, "", "  ")
			if err != nil {
				return fmt.Sprintf("Error formatting colleagues: %v", err), err
			}
			return string(b), nil
		},
	}
}

// ── ColleagueAsk ───────────────────────────────────────────────────────────

// colleagueAskInput is the JSON input schema for the ColleagueAsk tool.
type colleagueAskInput struct {
	Colleague  string   `json:"colleague"`
	Prompt     string   `json:"prompt"`
	Images     []string `json:"images,omitempty"`
	Timeout    int      `json:"timeout,omitempty"`
	Background bool     `json:"background,omitempty"`
}

// colleagueAskTimeout is the default wait when Timeout isn't specified.
const colleagueAskTimeout = 60 * time.Second

// ColleagueAsk returns a Tool that delegates a prompt to another running
// harness instance by name, blocking for the response (or, with background:
// true, returning immediately with a path to a result file).
func ColleagueAsk() Tool {
	return Tool{
		Def: types.ToolDef{
			Name:        ToolColleagueAsk,
			Description: "Delegate a prompt to a specific colleague by name (see ColleagueList). The colleague answers using ITS OWN model, tools, and project context — not yours; this is real delegation, not a call back to yourself. Attach local image paths via `images` if relevant. Blocks until the colleague finishes and returns its final text — set `background: true` to get a result-file path immediately instead of waiting, if the task might take a while (background has no timeout: use it for genuinely slow tasks instead of passing a large `timeout`).",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"colleague": {"type": "string", "description": "Name of the colleague instance (from ColleagueList)"},
					"prompt": {"type": "string", "description": "The prompt to delegate"},
					"images": {"type": "array", "items": {"type": "string"}, "description": "Local file paths of images to attach (png, jpg, jpeg, gif, webp)"},
					"timeout": {"type": "integer", "description": "Seconds to wait for a response (default: 60). Ignored when background is true — background waits as long as needed."},
					"background": {"type": "boolean", "description": "If true, return immediately with a path to a result file instead of blocking, and wait as long as needed (no timeout). Default false."}
				},
				"required": ["colleague", "prompt"]
			}`),
		},
		Execute: func(ctx context.Context, input json.RawMessage) (string, error) {
			var args colleagueAskInput
			if err := json.Unmarshal(input, &args); err != nil {
				return fmt.Sprintf("Error parsing input: %v", err), err
			}
			if strings.TrimSpace(args.Colleague) == "" {
				err := fmt.Errorf("colleague: name is required")
				return err.Error(), err
			}
			if strings.TrimSpace(args.Prompt) == "" {
				err := fmt.Errorf("colleague: prompt is required")
				return err.Error(), err
			}

			url, ok := resolveColleagueURL(args.Colleague)
			if !ok {
				err := fmt.Errorf("colleague %q not found", args.Colleague)
				return fmt.Sprintf("Colleague %q not found or not running. Use ColleagueList to see who's online.", args.Colleague), err
			}

			images, err := loadColleagueImages(args.Images)
			if err != nil {
				return err.Error(), err
			}

			// Timeout only applies to the blocking (foreground) path — it exists
			// to protect the CALLER from waiting indefinitely. In background
			// mode nothing is waiting: the tool already returned, and the
			// goroutine below is the only thing watching the request. Applying
			// the same timeout there would silently cut off exactly the slow
			// task background was meant to tolerate — and force the model to
			// compensate by passing artificially large `timeout` values that
			// don't actually belong to the concept of "don't block".
			if args.Background {
				return askColleagueBackground(url, args.Colleague, args.Prompt, images)
			}

			timeout := colleagueAskTimeout
			if args.Timeout > 0 {
				timeout = time.Duration(args.Timeout) * time.Second
			}
			return askColleague(url, args.Prompt, images, timeout)
		},
	}
}

// resolveColleagueURL looks up a colleague by name in the instance registry,
// excluding the caller's own entry (same self-PID rule as ColleagueList).
func resolveColleagueURL(name string) (string, bool) {
	info, ok := readInstances()[name]
	if !ok || info.PID == os.Getpid() || info.URL == "" {
		return "", false
	}
	return info.URL, true
}

// askColleague creates a session on the colleague (using ITS OWN default
// model — never overridden by the caller, so delegation respects the
// colleague's autonomy), asks synchronously, and closes the session.
func askColleague(url, prompt string, images []types.ImageData, timeout time.Duration) (string, error) {
	c := client.New(url)

	settings, err := c.GetSettings()
	if err != nil {
		return fmt.Sprintf("Error reaching colleague: %v", err), err
	}
	model := settings.ActiveModel
	if model == "" {
		models, err := c.ListModels()
		if err != nil || len(models) == 0 {
			err := fmt.Errorf("colleague has no active model configured")
			return err.Error(), err
		}
		model = models[0].Model
	}

	sess, err := c.CreateSession(model, "", "")
	if err != nil {
		return fmt.Sprintf("Error creating session on colleague: %v", err), err
	}
	// Close deactivates the session (removes it from the colleague's active
	// set); it does NOT remove its .jsonl/.meta.json from disk. Delegation
	// sessions are purely ephemeral — nothing in them is worth keeping once the
	// answer is back — so also delete, or every ColleagueAsk call leaves
	// permanent litter in the colleague's ~/.harness/agent/sessions/. Runs even
	// if Ask/AskWithImages below errors (timeout, colleague crash, etc.) —
	// defer always fires.
	defer func() {
		c.CloseSession(sess.ID)  //nolint:errcheck
		c.DeleteSession(sess.ID) //nolint:errcheck
	}()

	var (
		text string
		ask  error
	)
	if len(images) > 0 {
		text, ask = c.AskWithImages(sess.ID, prompt, images, timeout)
	} else {
		text, ask = c.Ask(sess.ID, prompt, timeout)
	}
	if ask != nil {
		return fmt.Sprintf("Colleague error: %v", ask), ask
	}
	return text, nil
}

// askColleagueBackground runs askColleague in a goroutine and writes the
// result to a temp file, returning immediately with that path. No timeout —
// see the comment at the ColleagueAsk call site for why: nothing is blocked
// waiting on this goroutine, so there is nothing to protect by cutting it off.
func askColleagueBackground(url, colleagueName, prompt string, images []types.ImageData) (string, error) {
	f, err := os.CreateTemp("", "harness-colleague-*.txt")
	if err != nil {
		return fmt.Sprintf("Error creating result file: %v", err), err
	}
	path := f.Name()
	f.Close()

	go func() {
		text, err := askColleague(url, prompt, images, 0) // 0 = no timeout
		result := text
		if err != nil {
			result = fmt.Sprintf("Error: %v\n\n%s", err, text)
		}
		_ = os.WriteFile(path, []byte(result), 0644)
	}()

	return fmt.Sprintf("Delegated to %s in background.\nResult will be written to: %s\nRead that file later to see the response.", colleagueName, path), nil
}

// loadColleagueImages validates and base64-encodes local image paths, using
// the same extension check as the Read tool (isImagePath/imageExtToMime).
// Returns an error immediately if any path is missing or not a supported
// image type — a partial delegation with silently-dropped images would be
// worse than failing clearly upfront.
func loadColleagueImages(paths []string) ([]types.ImageData, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	images := make([]types.ImageData, 0, len(paths))
	for _, p := range paths {
		if !isImagePath(p) {
			return nil, fmt.Errorf("colleague: %q is not a supported image type (png, jpg, jpeg, gif, webp)", p)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("colleague: reading image %q: %w", p, err)
		}
		ext := strings.ToLower(filepath.Ext(p))
		images = append(images, types.ImageData{
			MimeType: imageExtToMime[ext],
			Base64:   base64.StdEncoding.EncodeToString(data),
		})
	}
	return images, nil
}
