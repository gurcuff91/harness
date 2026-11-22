package colleague

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/gurcuff91/harness/agent"
	atools "github.com/gurcuff91/harness/agent/tools"
	"github.com/gurcuff91/harness/types"
)

func (c *Channel) listTool(self string) atools.Tool {
	return atools.Tool{
		Def: types.ToolDef{
			Name:        "ColleagueList",
			Description: "List the colleagues online now — other agents on this machine, each a session working on its own project — with the id to use with ColleagueAsk, its session name and the project directory (cwd) it works in. Pick a colleague by its project and session name.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		},
		Execute: func(ctx context.Context, _ json.RawMessage) (string, error) {
			type entry struct {
				ID      string `json:"id"`
				Session string `json:"session,omitempty"`
				CWD     string `json:"cwd"`
				You     bool   `json:"you,omitempty"`
			}
			claims := agent.LiveChannelClaims(Type)
			list := make([]entry, len(claims))
			// The name comes from each colleague itself (a ping over its
			// socket, in parallel); one that does not answer is listed
			// without it. A conversation this process listens to (ours
			// included) is read directly.
			var wg sync.WaitGroup
			for i, cl := range claims {
				list[i] = entry{ID: cl.ID, CWD: cl.CWD, You: cl.ID == self}
				if in := c.inbox(cl.ID); in != nil {
					if in.sessionName != nil {
						list[i].Session = in.sessionName()
					}
					continue
				}
				wg.Add(1)
				go func() {
					defer wg.Done()
					list[i].Session = ping(ctx, c.socketPath(cl.ID), self)
				}()
			}
			wg.Wait()
			b, _ := json.MarshalIndent(list, "", "  ")
			return string(b), nil
		},
	}
}

func (c *Channel) askTool(self string) atools.Tool {
	return atools.Tool{
		Def: types.ToolDef{
			Name:        "ColleagueAsk",
			Description: "Send a message to a colleague (an id from ColleagueList). Asynchronous: the tool returns as soon as the message is delivered, and the colleague's answer arrives LATER, as a new prompt wrapped in <colleague from=\"…\" reply>. After asking, end your turn (or keep working on something else): never wait, sleep, poll or ask again for the same thing. The colleague answers with its own model, tools and project context. Attach local images with `images`.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{` +
				`"colleague":{"type":"string","description":"The colleague's id (from ColleagueList)"},` +
				`"prompt":{"type":"string","description":"Your message"},` +
				`"images":{"type":"array","items":{"type":"string"},"description":"Local image paths to attach (png, jpg, jpeg, gif, webp)"}` +
				`},"required":["colleague","prompt"]}`),
		},
		Execute: func(ctx context.Context, input json.RawMessage) (string, error) {
			var args struct {
				Colleague string   `json:"colleague"`
				Prompt    string   `json:"prompt"`
				Images    []string `json:"images"`
			}
			if err := json.Unmarshal(input, &args); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
			to := strings.TrimSpace(args.Colleague)
			if err := checkID(to); err != nil {
				return "", err
			}
			if to == self {
				return "", errors.New("that is you: ask another colleague (see ColleagueList)")
			}
			if strings.TrimSpace(args.Prompt) == "" {
				return "", errors.New("prompt is required")
			}
			images, err := atools.LoadImages(args.Images)
			if err != nil {
				return "", err
			}
			err = send(ctx, c.socketPath(to), message{Kind: kindAsk, From: self, Text: args.Prompt, Images: images})
			if errors.Is(err, errGone) {
				return "", fmt.Errorf("colleague %s is not online (see ColleagueList)", to)
			}
			if err != nil {
				return "", fmt.Errorf("colleague %s: %w", to, err)
			}
			return fmt.Sprintf("Delivered to %s. Its reply will arrive later as a new prompt wrapped in <colleague from=%q reply>: end your turn now (or keep working on something else) — do not wait, poll or ask again.", to, to), nil
		},
	}
}
