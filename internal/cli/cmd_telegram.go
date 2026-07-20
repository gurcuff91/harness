package cli

import (
	"flag"
	"os"
	"strconv"
	"strings"

	"github.com/gurcuff91/harness/internal/transport/telegram"
)

// cmdTelegram runs the Telegram bot transport, or one of its config subcommands.
//
//	harness telegram pair <chat_id>     allow a chat
//	harness telegram unpair <chat_id>   revoke a chat (and drop its session)
//	harness telegram list               list paired chats
//	harness telegram [--token ...] [--model ...] [--thinking ...]
//	                 [--scheduler] [--allow-unpair]
func cmdTelegram(args []string) error {
	// Config subcommands: pure edits to telegram.json, no token/server needed.
	if len(args) > 0 {
		switch args[0] {
		case "pair", "unpair":
			if len(args) < 2 {
				return errUsage("telegram " + args[0] + " <chat_id>")
			}
			id, err := strconv.ParseInt(strings.TrimSpace(args[1]), 10, 64)
			if err != nil {
				return errf("invalid chat id: %s", args[1])
			}
			if args[0] == "pair" {
				return telegram.Pair(id)
			}
			return telegram.Unpair(id)
		case "list":
			return telegram.ListPaired()
		}
	}

	fs := flag.NewFlagSet("telegram", flag.ContinueOnError)
	token := fs.String("token", os.Getenv("TELEGRAM_BOT_TOKEN"), "bot token (or set TELEGRAM_BOT_TOKEN)")
	model := fs.String("model", "", "model override (provider/model)")
	thinking := fs.String("thinking", "", "thinking level override")
	scheduler := fs.Bool("scheduler", false, "run the cron scheduler engine")
	allowUnpair := fs.Bool("allow-unpair", false, "accept any chat, auto-pairing on first contact")
	if err := fs.Parse(args); err != nil {
		return err
	}

	a := newTelegramAgent(*scheduler)
	defer a.Close()
	ctx, cancel := signalContext()
	defer cancel()

	return telegram.Run(ctx, a, telegram.Options{
		Token:       *token,
		Model:       *model,
		Thinking:    *thinking,
		Scheduler:   *scheduler,
		AllowUnpair: *allowUnpair,
	})
}
