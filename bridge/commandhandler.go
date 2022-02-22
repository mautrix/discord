package bridge

import (
	"fmt"
	"strings"

	"github.com/alecthomas/kong"
	"github.com/google/shlex"

	"maunium.net/go/maulogger/v2"
	"maunium.net/go/mautrix/id"
)

type commandHandler struct {
	bridge *Bridge
	log    maulogger.Logger
}

func newCommandHandler(bridge *Bridge) *commandHandler {
	return &commandHandler{
		bridge: bridge,
		log:    bridge.log.Sub("Commands"),
	}
}

func commandsHelpPrinter(options kong.HelpOptions, ctx *kong.Context) error {
	selected := ctx.Selected()

	if selected == nil {
		for _, cmd := range ctx.Model.Leaves(true) {
			fmt.Fprintf(ctx.Stdout, " * %s - %s\n", cmd.Path(), cmd.Help)
		}
	} else {
		fmt.Fprintf(ctx.Stdout, "%s - %s\n", selected.Path(), selected.Help)
		if selected.Detail != "" {
			fmt.Fprintf(ctx.Stdout, "\n%s\n", selected.Detail)
		}
		if len(selected.Positional) > 0 {
			fmt.Fprintf(ctx.Stdout, "\nArguments:\n")
			for _, arg := range selected.Positional {
				fmt.Fprintf(ctx.Stdout, "%s %s\n", arg.Summary(), arg.Help)
			}
		}
	}

	return nil
}

func (h *commandHandler) handle(roomID id.RoomID, user *User, message string, replyTo id.EventID) {
	cmd := commands{
		globals: globals{
			bot:     h.bridge.bot,
			bridge:  h.bridge,
			portal:  h.bridge.GetPortalByMXID(roomID),
			handler: h,
			roomID:  roomID,
			user:    user,
			replyTo: replyTo,
		},
	}

	buf := &strings.Builder{}

	parse, err := kong.New(
		&cmd,
		kong.Exit(func(int) {}),
		kong.NoDefaultHelp(),
		kong.Writers(buf, buf),
		kong.Help(commandsHelpPrinter),
	)

	if err != nil {
		h.log.Warnf("Failed to create argument parser for %q: %v", roomID, err)

		cmd.globals.reply("unexpected error, please try again shortly")

		return
	}

	args, err := shlex.Split(message)
	if err != nil {
		h.log.Warnf("Failed to split message %q: %v", message, err)

		cmd.globals.reply("failed to process the command")

		return
	}

	ctx, err := parse.Parse(args)
	if err != nil {
		h.log.Warnf("Failed to parse command %q: %v", message, err)

		cmd.globals.reply("failed to process the command")

		return
	}

	cmd.globals.context = ctx

	err = ctx.Run(&cmd.globals)
	if err != nil {
		h.log.Warnf("Command %q failed: %v", message, err)

		output := buf.String()
		if output != "" {
			cmd.globals.reply(output)
		} else {
			cmd.globals.reply("unexpected failure")
		}

		return
	}

	if buf.Len() > 0 {
		cmd.globals.reply(buf.String())
	}
}
