package bridge

import (
	"fmt"

	"github.com/alecthomas/kong"

	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"
	"maunium.net/go/mautrix/id"

	"gitlab.com/beeper/discord/consts"
	"gitlab.com/beeper/discord/version"
)

type globals struct {
	context *kong.Context

	bridge  *Bridge
	bot     *appservice.IntentAPI
	portal  *Portal
	handler *commandHandler
	roomID  id.RoomID
	user    *User
	replyTo id.EventID
}

func (g *globals) reply(msg string) {
	content := format.RenderMarkdown(msg, true, false)
	content.MsgType = event.MsgNotice
	intent := g.bot

	if g.portal != nil && g.portal.IsPrivateChat() {
		intent = g.portal.MainIntent()
	}

	_, err := intent.SendMessageEvent(g.roomID, event.EventMessage, content)
	if err != nil {
		g.handler.log.Warnfln("Failed to reply to command from %q: %v", g.user.MXID, err)
	}
}

type commands struct {
	globals

	Help    helpCmd    `kong:"cmd,help='Displays this message.'"`
	Version versionCmd `kong:"cmd,help='Displays the version of the bridge.'"`
}

type helpCmd struct {
	Command []string `kong:"arg,optional,help='The command to get help on.'"`
}

func (c *helpCmd) Run(g *globals) error {
	ctx, err := kong.Trace(g.context.Kong, c.Command)
	if err != nil {
		return err
	}

	if ctx.Error != nil {
		return err
	}

	err = ctx.PrintUsage(true)
	if err != nil {
		return err
	}

	fmt.Fprintln(g.context.Stdout)

	return nil
}

type versionCmd struct{}

func (c *versionCmd) Run(g *globals) error {
	fmt.Fprintln(g.context.Stdout, consts.Name, version.String)

	return nil
}
