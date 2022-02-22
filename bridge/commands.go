package bridge

import (
	"context"
	"fmt"

	"github.com/alecthomas/kong"

	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"
	"maunium.net/go/mautrix/id"

	"gitlab.com/beeper/discord/consts"
	"gitlab.com/beeper/discord/remoteauth"
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

	Disconnect disconnectCmd `kong:"cmd,help='Disconnect from Discord'"`
	Help       helpCmd       `kong:"cmd,help='Displays this message.'"`
	Login      loginCmd      `kong:"cmd,help='Log in to Discord.'"`
	Logout     logoutCmd     `kong:"cmd,help='Log out of Discord.'"`
	Reconnect  reconnectCmd  `kong:"cmd,help='Reconnect to Discord'"`
	Version    versionCmd    `kong:"cmd,help='Displays the version of the bridge.'"`
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

type loginCmd struct{}

func (l *loginCmd) Run(g *globals) error {
	if g.user.LoggedIn() {
		fmt.Fprintf(g.context.Stdout, "You are already logged in")

		return fmt.Errorf("user already logged in")
	}

	client, err := remoteauth.New()
	if err != nil {
		return err
	}

	qrChan := make(chan string)
	doneChan := make(chan struct{})

	go func() {
		code := <-qrChan

		_, err := g.user.sendQRCode(g.bot, g.roomID, code)
		if err != nil {
			fmt.Fprintln(g.context.Stdout, "failed to generate the qrcode")

			return
		}
	}()

	ctx := context.Background()

	if err := client.Dial(ctx, qrChan, doneChan); err != nil {
		close(qrChan)
		close(doneChan)

		return err
	}

	<-doneChan

	user, err := client.Result()
	if err != nil {
		fmt.Println(g.context.Stdout, "failed to log in")

		return err
	}

	if err := g.user.Login(user.Token); err != nil {
		fmt.Println(g.context.Stdout, "failed to login", err)

		return err
	}

	g.user.ID = user.UserID
	g.user.Update()

	return nil
}

type logoutCmd struct{}

func (l *logoutCmd) Run(g *globals) error {
	if !g.user.LoggedIn() {
		fmt.Fprintln(g.context.Stdout, "You are not logged in")

		return fmt.Errorf("user is not logged in")
	}

	err := g.user.Logout()
	if err != nil {
		fmt.Fprintln(g.context.Stdout, "Failed to log out")

		return err
	}

	fmt.Fprintln(g.context.Stdout, "Successfully logged out")

	return nil
}

type disconnectCmd struct{}

func (d *disconnectCmd) Run(g *globals) error {
	if !g.user.Connected() {
		fmt.Fprintln(g.context.Stdout, "You are not connected")

		return fmt.Errorf("user is not connected")
	}

	if err := g.user.Disconnect(); err != nil {
		fmt.Fprintln(g.context.Stdout, "Failed to disconnect")

		return err
	}

	fmt.Fprintln(g.context.Stdout, "Successfully disconnected")

	return nil
}

type reconnectCmd struct{}

func (r *reconnectCmd) Run(g *globals) error {
	if g.user.Connected() {
		fmt.Fprintln(g.context.Stdout, "You are already connected")

		return fmt.Errorf("user is already connected")
	}

	if err := g.user.Connect(); err != nil {
		fmt.Fprintln(g.context.Stdout, "Failed to connect")

		return err
	}

	fmt.Fprintln(g.context.Stdout, "Successfully connected")

	return nil
}
