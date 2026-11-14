package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gurcuff91/harness/client"
	"github.com/gurcuff91/harness/types"
)

// ── Colleague directory (injected) ─────────────────────────────────────────
//
// The registry of running harness instances is persisted in harness's
// settings store and written by the server package. agent/tools is public SDK
// and never imports internal/ or server, so — same pattern as the Schedule*
// tools' ScheduleStore — the tools receive the directory as an injected
// function (wired in agent.go over the settings manager).

// Colleague is one registered instance, as the colleague tools see it.
type Colleague struct {
	Version   string
	Transport string
	URL       string
	CWD       string
	PID       int
}

// ColleagueDirectory returns every registered instance, keyed by name. It
// must never fail: an unreadable registry is just "no colleagues".
type ColleagueDirectory func() map[string]Colleague

// ── ColleagueList ──────────────────────────────────────────────────────────

// colleagueEnvironments are the on-disk transport values a colleague is shown
// under by name: each one is a genuinely different place the colleague lives, and
// so a different thing it can do — a person at a terminal (tui), a Slack channel
// or a Telegram chat to post and reply in. Everything else — a headless `harness
// serve`, the CLI's own commands, an ACP bridge, any transport added later — is
// "server": an agent with tools and a working directory and nobody watching.
// Adding a new frontend that deserves its own name means one entry here.
var colleagueEnvironments = map[string]bool{
	"tui":      true,
	"slack":    true,
	"telegram": true,
}

// environmentLabel maps an on-disk transport value to what the model sees in
// ColleagueList: tui, slack and telegram keep their name, everything else is
// "server".
func environmentLabel(transport string) string {
	if colleagueEnvironments[transport] {
		return transport
	}
	return "server"
}

// colleagueListEntry is what ColleagueList shows the model — just enough to
// pick a colleague and a session of it for ColleagueAsk. JSON key "environment"
// (not the on-disk field name "transport") is what the model reasons over — see
// the "## Colleagues" system prompt section.
//
// There is deliberately no instance-level working directory: it belongs to each
// session (they normally all share the instance's, but a session may have been
// opened elsewhere), so it lives on colleagueSession and is never duplicated here.
type colleagueListEntry struct {
	Name        string `json:"name"`
	Environment string `json:"environment"`
	Version     string `json:"version"`
	// Sessions are the colleague's ACTIVE sessions — open right now, so they can be
	// prompted directly with ColleagueAsk's `session`, no resume. Empty when it has
	// none open (it can still be asked without `session`, which starts a new one).
	Sessions []colleagueSession `json:"sessions"`
}

// colleagueSession is one active session of a colleague.
type colleagueSession struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	CWD   string `json:"cwd"`
	Model string `json:"model"`
	// Busy: a turn is running in it right now. A ColleagueAsk to a busy session is
	// refused at once (it is never queued behind the running turn).
	Busy bool `json:"busy"`
}

// colleagueListTimeout bounds how long ColleagueList waits for any one colleague.
// One concurrent round, so the whole call costs about the slowest colleague, never
// the sum — a suspended or dead instance simply misses the deadline and is left out.
var colleagueListTimeout = 2 * time.Second

// queryColleague asks one colleague for its active sessions. The same request is
// the liveness probe: a colleague that cannot list its sessions in time is not one
// a ColleagueAsk would get an answer from either. ok is false for such a colleague.
func queryColleague(ctx context.Context, url string) (sessions []colleagueSession, ok bool) {
	ctx, cancel := context.WithTimeout(ctx, colleagueListTimeout)
	defer cancel()
	active, err := client.New(url, client.WithContext(ctx)).ListActiveSessions("")
	if err != nil {
		return nil, false
	}
	sessions = make([]colleagueSession, 0, len(active))
	for _, a := range active {
		sessions = append(sessions, colleagueSession{ID: a.ID, Name: a.Name, CWD: a.CWD, Model: a.Model, Busy: a.Busy})
	}
	return sessions, true
}

// ColleagueList returns a Tool that lists other REACHABLE colleague instances,
// each with its active sessions. The caller itself is excluded (matched by PID —
// the one field every registry entry carries that reliably identifies "not me").
//
// Every registered colleague is queried concurrently; the ones that don't answer
// within colleagueListTimeout — crashed without unregistering, or a process
// suspended with Ctrl+Z whose socket still accepts connections — are omitted, so
// the list only offers colleagues that were just seen alive.
func ColleagueList(directory ColleagueDirectory) Tool {
	return Tool{
		Def: types.ToolDef{
			Name:        ToolColleagueList,
			Description: "List the other colleague instances currently reachable — each is a live agent you can delegate to via ColleagueAsk. Returns each colleague's name (use it with ColleagueAsk), its environment (tui, slack, telegram or server), its version, and its ACTIVE sessions: for each one its id, name, working directory, model and whether it is busy right now. Pass a session id to ColleagueAsk to talk to that session directly. Returns an empty list if no colleagues are reachable — that's normal, not an error.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		},
		Execute: func(ctx context.Context, input json.RawMessage) (string, error) {
			self := os.Getpid()
			type candidate struct {
				name string
				info Colleague
			}
			var cands []candidate
			for name, info := range directory() {
				if info.PID == self || info.URL == "" {
					continue
				}
				cands = append(cands, candidate{name, info})
			}

			results := make([]*colleagueListEntry, len(cands))
			var wg sync.WaitGroup
			for i, c := range cands {
				wg.Add(1)
				go func() {
					defer wg.Done()
					sessions, ok := queryColleague(ctx, c.info.URL)
					if !ok {
						return
					}
					results[i] = &colleagueListEntry{
						Name:        c.name,
						Environment: environmentLabel(c.info.Transport),
						Version:     c.info.Version,
						Sessions:    sessions,
					}
				}()
			}
			wg.Wait()

			var out []colleagueListEntry
			for _, r := range results {
				if r != nil {
					out = append(out, *r)
				}
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
//
// Colleague and Prompt carry validate:"required" (checked via
// requireFields) AND their own explicit strings.TrimSpace checks below —
// same reasoning as Subagent.Prompt: the tag only catches "absent", not
// "all whitespace".
type colleagueAskInput struct {
	Colleague  string   `json:"colleague" validate:"required"`
	Prompt     string   `json:"prompt" validate:"required"`
	Images     []string `json:"images,omitempty"`
	Timeout    int      `json:"timeout,omitempty"`
	Background bool     `json:"background,omitempty"`
	// Session, when set, resumes an existing conversation with this
	// colleague instead of starting a new one — see askColleague's doc
	// comment for the full persistence contract.
	Session string `json:"session,omitempty"`
}

// colleagueAskTimeout is the default wait when Timeout isn't specified.
const colleagueAskTimeout = 120 * time.Second

// ColleagueAsk returns a Tool that delegates a prompt to another running
// harness instance by name, blocking for the response (or, with background:
// true, returning immediately with a path to a result file).
//
// The SYNCHRONOUS (foreground) path now genuinely respects the turn's own
// ctx — Session.Stop() cancelling it cancels every in-flight request to the
// colleague too, including ones with no per-call timeout of their own
// (CreateSession/ResumeSession/GetSettings), via client.WithContext (see
// askColleague). This closes a real, reproduced-live gap: a colleague whose
// PROCESS was suspended (SIGTSTP/Ctrl+Z, not crashed — its listening socket
// stayed open, so TCP connected fine but nothing ever answered) left a
// ColleagueAsk call hung indefinitely with no way to interrupt it — Esc/Stop
// did nothing because the ctx it cancelled was never wired to the HTTP
// request actually blocking. A short, ctx-derived liveness probe
// (colleagueLivenessTimeout) now also fails fast in that exact scenario
// instead of hanging until Ask's own (much longer) timeout.
//
// Background mode's goroutine (askColleagueBackground) deliberately does
// NOT use this ctx (or WithContext) — same reasoning as Subagent's
// background mode (see runSubagentBackground's doc comment in subagent.go):
// a turn's ctx is cancelled unconditionally the moment the turn itself
// ends, which defeats the entire purpose of "outlive this turn, check the
// result file later." The ctx parameter below is read for the
// SYNCHRONOUS (foreground) path only.
func ColleagueAsk(directory ColleagueDirectory) Tool {
	return Tool{
		Def: types.ToolDef{
			Name:        ToolColleagueAsk,
			Description: "Delegate a prompt to a specific colleague by name (see ColleagueList). The colleague answers using ITS OWN model, tools, and project context — not yours; this is real delegation, not a call back to yourself. Attach local image paths via `images` if relevant. Blocks until the colleague finishes and returns its final text — set `background: true` to get a result-file path immediately instead of waiting, if the task might take a while (background has no timeout: use it for genuinely slow tasks instead of passing a large `timeout`). Sessions: omit `session` to start a NEW conversation with the colleague; the reply ends with its session id. The conversation is closed when the colleague answers, but you can pick it up later — pass that id as `session` and it continues where it left off, with its full history. `session` also accepts the id of one of the colleague's ACTIVE sessions (from ColleagueList's `sessions`), which is then messaged directly and stays as it was; if that session is busy the message is NOT sent and you get an error, so try later or pick another. A `session` id that no longer exists on the colleague is a 'not found' error.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"colleague": {"type": "string", "description": "Name of the colleague instance (from ColleagueList)"},
					"prompt": {"type": "string", "description": "The prompt to delegate"},
					"images": {"type": "array", "items": {"type": "string"}, "description": "Local file paths of images to attach (png, jpg, jpeg, gif, webp)"},
					"timeout": {"type": "integer", "description": "Seconds to wait for a response (default: 120). Ignored when background is true — background waits as long as needed."},
					"background": {"type": "boolean", "description": "If true, return immediately with a path to a result file instead of blocking, and wait as long as needed (no timeout). Default false."},
					"session": {"type": "string", "description": "A session id to continue: the id at the end of a previous ColleagueAsk reply (a closed conversation is picked up where it left off), or one of the colleague's ACTIVE sessions from ColleagueList's 'sessions'. An error if the session no longer exists, or if it is busy (the message is never queued). Omit to start a new conversation with the colleague."}
				},
				"required": ["colleague", "prompt"]
			}`),
		},
		Execute: func(ctx context.Context, input json.RawMessage) (string, error) {
			var args colleagueAskInput
			if err := json.Unmarshal(input, &args); err != nil {
				return fmt.Sprintf("Error parsing input: %v", err), err
			}
			if err := requireFields(&args); err != nil {
				return err.Error(), err
			}
			if strings.TrimSpace(args.Colleague) == "" {
				err := fmt.Errorf("colleague: name is required")
				return err.Error(), err
			}
			if strings.TrimSpace(args.Prompt) == "" {
				err := fmt.Errorf("colleague: prompt is required")
				return err.Error(), err
			}

			url, ok := resolveColleagueURL(directory, args.Colleague)
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
				return askColleagueBackground(url, args.Colleague, args.Prompt, args.Session, images)
			}

			timeout := colleagueAskTimeout
			if args.Timeout > 0 {
				timeout = time.Duration(args.Timeout) * time.Second
			}
			return askColleague(ctx, url, args.Prompt, args.Session, images, timeout)
		},
	}
}

// resolveColleagueURL looks up a colleague by name in the instance registry,
// excluding the caller's own entry (same self-PID rule as ColleagueList).
func resolveColleagueURL(directory ColleagueDirectory, name string) (string, bool) {
	info, ok := directory()[name]
	if !ok || info.PID == os.Getpid() || info.URL == "" {
		return "", false
	}
	return info.URL, true
}

// colleagueLivenessTimeout bounds the liveness probe askColleague does
// before touching a colleague — a quick GET /api/server. Reproduced live:
// a colleague whose PROCESS was suspended (SIGTSTP/Ctrl+Z) keeps its
// listening socket open (TCP connects fine), but no goroutine is running
// to ever answer — every request against it, including ones with no
// per-call timeout of their own, hangs indefinitely. This probe fails
// fast in that case instead of discovering it only after Ask's own (much
// longer, colleagueAskTimeout-scale) timeout finally expires. Short
// because a healthy colleague's /api/server never legitimately takes
// long — it only reads a couple of static fields under a brief RLock.
const colleagueLivenessTimeout = 5 * time.Second

// colleagueOrigin tags every prompt a colleague sends. It is for the colleague's
// FRONTENDS only — a TUI shows the prompt with its own icon, like a scheduled one
// — and the model never sees it (the text is delivered unchanged), exactly as with
// the scheduler's origin.
const colleagueOrigin = "colleague"

// colleagueBusyMessage is what the model is told when it asks a busy session.
const colleagueBusyMessage = "That session is busy — a turn is running in it right now, so the message was NOT sent (it is never queued behind the running turn). Try again later, ask a different session, or omit `session` to start a new conversation with this colleague."

// askColleague talks to a colleague. A conversation is either NEW (no `session`) or
// an existing one (`session` = its id), and it is never left open behind the caller:
// a session this call opened is closed again before returning, so a caller that
// doesn't come back cannot leave orphan sessions piling up on the colleague. What
// makes a conversation continuable is not keeping it open but its id, which every
// reply carries: pass it back as `session` and the conversation is picked up where
// it was, with its full history.
//
//   - No `session`: a NEW session is created on the colleague — default name, the
//     colleague's own working directory, its own default model (never overridden,
//     so delegation respects its autonomy) — asked, and closed. The reply carries
//     its id.
//   - `session` ACTIVE on the colleague (open in a TUI, a transport, …): prompted
//     directly. It is left exactly as found — it is somebody's, not ours to close.
//   - `session` NOT active: it is resumed — it may be one of ours from an earlier
//     call, or any persisted session — then asked, and closed again if (and only
//     if) THIS call opened it. If it turns out to have been opened by someone else
//     in the meantime, it is treated as active: asked and left alone. Resume is
//     idempotent, so the colleague itself reports which case it was
//     (client.Session.Resumed); closing on a guess would end a live conversation
//     under the person using it.
//   - `session` that does not exist anywhere: "session not found", no fresh session
//     in its place.
//
// Every prompt is sent without queueing: a session with a turn running refuses it
// at once, and the tool reports that as an error rather than parking the message
// behind the turn (which would stall this call and put it into somebody else's
// conversation). The refusal is decided by the colleague, atomically with the send,
// so a session that turned busy after ColleagueList was read is still caught.
//
// ctx is the CALLING TURN's own context (cancelled by Session.Stop()) — every
// request this function makes, including the liveness probe, derives from it via
// client.WithContext, so Stop()/Esc genuinely interrupts a hung call at any point
// in the chain. askColleagueBackground deliberately does NOT pass the turn's ctx
// here — see its own doc comment.
func askColleague(ctx context.Context, url, prompt, session string, images []types.ImageData, timeout time.Duration) (string, error) {
	// Liveness probe FIRST, before touching anything — a colleague that can't even
	// answer GET /api/server (a couple of static fields, no session/agent work at
	// all) is not going to complete anything heavier either. Bounded by BOTH its
	// own short timeout AND the turn's ctx (whichever fires first) — this does NOT
	// touch the colleague registry; that cleanup only happens in RegisterInstance
	// (see server/instances.go). The same reply carries the colleague's working
	// directory, which a new session is created in.
	pingCtx, cancel := context.WithTimeout(ctx, colleagueLivenessTimeout)
	defer cancel()
	info, err := client.New(url, client.WithContext(pingCtx)).GetServerInfo()
	if err != nil {
		err := fmt.Errorf("colleague at %s is not responding: %w", url, err)
		return "Colleague is not responding (may have crashed or been suspended) — use ColleagueList to pick a different one.", err
	}

	c := client.New(url, client.WithContext(ctx))

	// An existing conversation.
	if session != "" {
		active, err := c.ListActiveSessions("")
		if err != nil {
			return fmt.Sprintf("Error reaching colleague: %v", err), err
		}
		for _, a := range active {
			if a.ID == session {
				// Somebody's open session: prompt it directly and leave it open.
				return sendToColleagueSession(c, session, prompt, images, timeout, noteActive)
			}
		}

		// Not active: resume it, and close it again only if this call opened it.
		sess, err := c.ResumeSession(session)
		if err != nil {
			err := fmt.Errorf("colleague session %q not found: %w", session, err)
			return fmt.Sprintf("Colleague session %q not found on that colleague (it may have been deleted). Omit `session` to start a new conversation.", session), err
		}
		if sess.Resumed != nil && *sess.Resumed {
			defer c.CloseSession(session) //nolint:errcheck
			return sendToColleagueSession(c, session, prompt, images, timeout, noteClosed)
		}
		// Opened by someone else between the check above and the resume: it is theirs.
		return sendToColleagueSession(c, session, prompt, images, timeout, noteActive)
	}

	// A new conversation, on the colleague's default model, in its working directory.
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
	sess, err := c.CreateSession(model, info.CWD, "")
	if err != nil {
		return fmt.Sprintf("Error creating session on colleague: %v", err), err
	}
	// Close (never delete), even if the ask below fails (timeout, colleague crash,
	// …) — defer always fires — so the conversation is on the colleague's disk and
	// can be picked up by its id.
	defer func() {
		c.CloseSession(sess.ID) //nolint:errcheck
	}()
	return sendToColleagueSession(c, sess.ID, prompt, images, timeout, noteClosed)
}

// What the reply tells the model about the session it just used, so it knows both
// the id to continue with and whether the session is still open on the colleague.
type sessionNote int

const (
	// noteClosed: the session was opened for this call and closed again; its id is
	// how to continue the conversation.
	noteClosed sessionNote = iota
	// noteActive: the session was already open on the colleague and still is.
	noteActive
)

// colleagueSessionFooter ends every reply that reaches the colleague's model: the
// session id (traceable, and the handle to continue with) and what state it is in.
func colleagueSessionFooter(sessID string, n sessionNote) string {
	if n == noteActive {
		return fmt.Sprintf("\n\n[colleague session: %s — active on the colleague; pass this id as `session` to keep talking to it]", sessID)
	}
	return fmt.Sprintf("\n\n[colleague session: %s — closed now that the colleague has answered; pass this id as `session` to continue this conversation, it is picked up where it left off]", sessID)
}

// sendToColleagueSession asks one session synchronously — never queued, marked as
// coming from a colleague — and shapes the reply. A busy session is an error.
func sendToColleagueSession(c *client.Client, sessID, prompt string, images []types.ImageData, timeout time.Duration, n sessionNote) (string, error) {
	opts := []client.PromptOption{client.WithOrigin(colleagueOrigin), client.WithoutQueue()}
	var (
		text string
		ask  error
	)
	if len(images) > 0 {
		text, ask = c.AskWithImages(sessID, prompt, images, timeout, opts...)
	} else {
		text, ask = c.Ask(sessID, prompt, timeout, opts...)
	}
	if ask != nil {
		if client.IsBusy(ask) {
			return colleagueBusyMessage, ask
		}
		// A timeout gets a clean, actionable message (no partial exists anyway —
		// the colleague runs in a separate process, nothing streamed back here).
		if isTimeout(ask) {
			msg := fmt.Sprintf("Timeout after %v — retry with a larger 'timeout', or 'background: true' for a genuinely slow task.", timeout)
			return msg, ask
		}
		return fmt.Sprintf("Colleague error: %v", ask), ask
	}
	return text + colleagueSessionFooter(sessID, n), nil
}

// askColleagueBackground runs askColleague in a goroutine and writes the
// result to a temp file, returning immediately with that path. No timeout — see
// the comment at the ColleagueAsk call site for why: nothing is blocked waiting
// on this goroutine, so there is nothing to protect by cutting it off. session
// behaves identically to the synchronous path (an active session is prompted
// directly and refused if busy; without it, a new conversation is started and
// closed when answered) — a busy session is reported in the file as an error.
//
// Uses context.Background() for the goroutine, NEVER the calling turn's
// ctx — see ColleagueAsk's doc comment (mirrors runSubagentBackground's
// identical, already-fixed reasoning in subagent.go): a turn's ctx is
// cancelled unconditionally the instant the turn itself ends, which would
// kill this goroutine before a genuinely slow colleague ever finishes —
// exactly the case background mode exists to tolerate. The liveness probe
// inside askColleague still applies here (bounded by its own short timeout
// derived from this same context.Background()), so a suspended/dead
// colleague still fails fast instead of hanging forever even in
// background mode.
func askColleagueBackground(url, colleagueName, prompt, session string, images []types.ImageData) (string, error) {
	f, err := os.CreateTemp("", "harness-colleague-*.txt")
	if err != nil {
		return fmt.Sprintf("Error creating result file: %v", err), err
	}
	path := f.Name()
	f.Close()

	go func() {
		text, err := askColleague(context.Background(), url, prompt, session, images, 0) // 0 = no timeout
		result := text
		if err != nil {
			result = fmt.Sprintf("Error: %v\n\n%s", err, text)
		}
		_ = os.WriteFile(path, []byte(result), 0644)
	}()

	return fmt.Sprintf("Delegated to %s in background.\nResult will be written to: %s\nRead that file later to see the response.", colleagueName, path), nil
}

// loadColleagueImages validates and base64-encodes local image paths, using
// the same extension check as the Read tool (isImagePath) to decide which
// paths are worth attempting — but the actual mime_type sent to the
// provider/persisted to the session's .jsonl comes from sniffImageMime
// (the real bytes), never the extension. See sniffImageMime's doc comment
// for the exact session-corrupting bug this prevents. Returns an error
// immediately if any path is missing, not a supported image type, or its
// content doesn't match a supported format — a partial delegation with
// silently-dropped or mislabeled images would be worse than failing
// clearly upfront.
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
		mime, ok := sniffImageMime(data)
		if !ok {
			return nil, fmt.Errorf("colleague: %q has an image extension but its content is not a supported image format (detected: %s)", p, mime)
		}
		images = append(images, types.ImageData{
			MimeType: mime,
			Base64:   base64.StdEncoding.EncodeToString(data),
		})
	}
	return images, nil
}
