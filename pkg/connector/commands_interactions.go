// mautrix-discord - A Matrix-Discord puppeting bridge.
// Copyright (C) 2023 Tulir Asokan
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package connector

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/google/shlex"
	"maunium.net/go/mautrix/bridgev2/commands"

	"go.mau.fi/mautrix-discord/pkg/discordid"
)

var helpSectionDiscordBots = commands.HelpSection{Name: "Discord bot interaction", Order: 30}
var cmdCommands = &commands.FullHandler{
	Name: "commands", Aliases: []string{"cmds", "cs"}, Func: fnCommands,
	RequiresPortal: true, RequiresLogin: true,
	Help: commands.HelpMeta{Section: helpSectionDiscordBots, Description: "View Discord bot commands and their parameters.", Args: "search <query> OR help <command>"},
}
var cmdExec = &commands.FullHandler{
	Name: "exec", Aliases: []string{"command", "cmd", "c", "e"}, Func: fnExec,
	RequiresPortal: true, RequiresLogin: true,
	Help: commands.HelpMeta{Section: helpSectionDiscordBots, Description: "Run a Discord bot command.", Args: "<command> [arg=value ...]"},
}

func getCommandOptionTypeName(optType discordgo.ApplicationCommandOptionType) string {
	switch optType {
	case discordgo.ApplicationCommandOptionSubCommand:
		return "subcommand"
	case discordgo.ApplicationCommandOptionSubCommandGroup:
		return "subcommand group (unsupported)"
	case discordgo.ApplicationCommandOptionString:
		return "string"
	case discordgo.ApplicationCommandOptionInteger:
		return "integer"
	case discordgo.ApplicationCommandOptionBoolean:
		return "boolean"
	case discordgo.ApplicationCommandOptionUser:
		return "user (unsupported)"
	case discordgo.ApplicationCommandOptionChannel:
		return "channel (unsupported)"
	case discordgo.ApplicationCommandOptionRole:
		return "role (unsupported)"
	case discordgo.ApplicationCommandOptionMentionable:
		return "mentionable (unsupported)"
	case discordgo.ApplicationCommandOptionNumber:
		return "number"
	case discordgo.ApplicationCommandOptionAttachment:
		return "attachment (unsupported)"
	default:
		return fmt.Sprintf("unknown type %d", optType)
	}
}

func parseCommandOptionValue(optType discordgo.ApplicationCommandOptionType, value string) (any, error) {
	switch optType {
	case discordgo.ApplicationCommandOptionSubCommandGroup:
		return nil, fmt.Errorf("subcommand groups aren't supported")
	case discordgo.ApplicationCommandOptionString:
		return value, nil
	case discordgo.ApplicationCommandOptionInteger:
		return strconv.ParseInt(value, 10, 64)
	case discordgo.ApplicationCommandOptionBoolean:
		return strconv.ParseBool(value)
	case discordgo.ApplicationCommandOptionUser:
		return nil, fmt.Errorf("user options aren't supported")
	case discordgo.ApplicationCommandOptionChannel:
		return nil, fmt.Errorf("channel options aren't supported")
	case discordgo.ApplicationCommandOptionRole:
		return nil, fmt.Errorf("role options aren't supported")
	case discordgo.ApplicationCommandOptionMentionable:
		return nil, fmt.Errorf("mentionable options aren't supported")
	case discordgo.ApplicationCommandOptionNumber:
		return strconv.ParseFloat(value, 64)
	case discordgo.ApplicationCommandOptionAttachment:
		return nil, fmt.Errorf("attachment options aren't supported")
	default:
		return nil, fmt.Errorf("unknown option type %d", optType)
	}
}

func indent(text, with string) string {
	split := strings.Split(text, "\n")
	for i, part := range split {
		split[i] = with + part
	}
	return strings.Join(split, "\n")
}

func formatOption(opt *discordgo.ApplicationCommandOption) string {
	argText := fmt.Sprintf("* `%s`: %s", opt.Name, getCommandOptionTypeName(opt.Type))
	if strings.ToLower(opt.Description) != opt.Name {
		argText += fmt.Sprintf(" - %s", opt.Description)
	}
	if opt.Required {
		argText += " (required)"
	}
	if len(opt.Options) > 0 {
		subopts := make([]string, len(opt.Options))
		for i, subopt := range opt.Options {
			subopts[i] = indent(formatOption(subopt), "  ")
		}
		argText += "\n" + strings.Join(subopts, "\n")
	}
	return argText
}

func formatCommand(cmd *discordgo.ApplicationCommand) string {
	baseText := fmt.Sprintf("$cmdprefix exec %s", cmd.Name)
	if len(cmd.Options) > 0 {
		args := make([]string, len(cmd.Options))
		argPlaceholder := "[arg=value ...]"
		for i, opt := range cmd.Options {
			args[i] = formatOption(opt)
			if opt.Required {
				argPlaceholder = "<arg=value ...>"
			}
		}
		baseText = fmt.Sprintf("`%s %s` - %s\n%s", baseText, argPlaceholder, cmd.Description, strings.Join(args, "\n"))
	} else {
		baseText = fmt.Sprintf("`%s` - %s", baseText, cmd.Description)
	}
	return baseText
}

func parseCommandOptions(opts []*discordgo.ApplicationCommandOption, subcommands []string, namedArgs map[string]string) (res []*discordgo.ApplicationCommandOptionInput, err error) {
	subcommandDone := false
	for _, opt := range opts {
		optRes := &discordgo.ApplicationCommandOptionInput{
			Type: opt.Type,
			Name: opt.Name,
		}
		if opt.Type == discordgo.ApplicationCommandOptionSubCommand {
			if !subcommandDone && len(subcommands) > 0 && subcommands[0] == opt.Name {
				subcommandDone = true
				optRes.Options, err = parseCommandOptions(opt.Options, subcommands[1:], namedArgs)
				if err != nil {
					err = fmt.Errorf("error parsing subcommand %s: %v", opt.Name, err)
					break
				}
				subcommands = subcommands[1:]
			} else {
				continue
			}
		} else if argVal, ok := namedArgs[opt.Name]; ok {
			optRes.Value, err = parseCommandOptionValue(opt.Type, argVal)
			if err != nil {
				err = fmt.Errorf("error parsing parameter %s: %v", opt.Name, err)
				break
			}
		} else if opt.Required {
			switch opt.Type {
			case discordgo.ApplicationCommandOptionSubCommandGroup, discordgo.ApplicationCommandOptionUser,
				discordgo.ApplicationCommandOptionChannel, discordgo.ApplicationCommandOptionRole,
				discordgo.ApplicationCommandOptionMentionable, discordgo.ApplicationCommandOptionAttachment:
				err = fmt.Errorf("missing required parameter %s (which is not supported by the bridge)", opt.Name)
			default:
				err = fmt.Errorf("missing required parameter %s", opt.Name)
			}
			break
		} else {
			continue
		}
		res = append(res, optRes)
	}
	if len(subcommands) > 0 {
		err = fmt.Errorf("unparsed subcommands left over (did you forget quoting for parameters with spaces?)")
	}
	return
}

func executeCommand(cmd *discordgo.ApplicationCommand, args []string) (res []*discordgo.ApplicationCommandOptionInput, err error) {
	namedArgs := map[string]string{}
	n := 0
	for _, arg := range args {
		name, value, isNamed := strings.Cut(arg, "=")
		if isNamed {
			namedArgs[name] = value
		} else {
			args[n] = arg
			n++
		}
	}
	return parseCommandOptions(cmd.Options, args[:n], namedArgs)
}

func (d *DiscordClient) cacheCommands(channelID string, results []*discordgo.ApplicationCommand) {
	d.commandsLock.Lock()
	defer d.commandsLock.Unlock()
	if d.commandCache == nil {
		d.commandCache = make(map[string]map[string]*discordgo.ApplicationCommand)
	}
	if d.commandCache[channelID] == nil {
		d.commandCache[channelID] = make(map[string]*discordgo.ApplicationCommand)
	}
	for _, result := range results {
		d.commandCache[channelID][result.Name] = result
	}
}

func (d *DiscordClient) getCommand(ctx context.Context, guildID, channelID, name string) (*discordgo.ApplicationCommand, error) {
	d.commandsLock.Lock()
	cmd := d.commandCache[channelID][name]
	d.commandsLock.Unlock()
	if cmd != nil {
		return cmd, nil
	}
	results, err := d.Session.ApplicationCommandsSearch(channelID, name, makeDiscordReferer(guildID, channelID, ""), discordgo.WithContext(ctx))
	if err != nil {
		return nil, err
	}
	d.cacheCommands(channelID, results)
	for _, result := range results {
		if result.Name == name {
			return result, nil
		}
	}
	return nil, nil
}

func fnCommands(ce *commands.Event) {
	if len(ce.Args) < 2 {
		ce.Reply("Usage: `$cmdprefix commands search <query>` OR `$cmdprefix commands help <command>`")
		return
	}
	client := commandClient(ce, true)
	if client == nil {
		return
	}
	guildID := ce.Portal.Metadata.(*discordid.PortalMetadata).GuildID
	channelID := discordid.ParseChannelPortalID(ce.Portal.ID)
	switch strings.ToLower(ce.Args[0]) {
	case "search":
		results, err := client.Session.ApplicationCommandsSearch(channelID, ce.Args[1], makeDiscordReferer(guildID, channelID, ""), discordgo.WithContext(ce.Ctx))
		if err != nil {
			ce.Reply("Error searching for commands: %v", err)
			return
		}
		client.cacheCommands(channelID, results)
		formatted := make([]string, len(results))
		for i, result := range results {
			formatted[i] = "* " + strings.TrimPrefix(indent(formatCommand(result), "  "), "  ")
		}
		ce.Reply("Found results:\n%s", strings.Join(formatted, "\n"))
	case "help":
		name := strings.ToLower(ce.Args[1])
		cmd, err := client.getCommand(ce.Ctx, guildID, channelID, name)
		if err != nil {
			ce.Reply("Error searching for commands: %v", err)
		} else if cmd == nil {
			ce.Reply("Command %q not found", name)
		} else {
			ce.Reply("%s", formatCommand(cmd))
		}
	default:
		ce.Reply("Usage: `$cmdprefix commands search <query>` OR `$cmdprefix commands help <command>`")
	}
}

func fnExec(ce *commands.Event) {
	args, err := shlex.Split(ce.RawArgs)
	if err != nil {
		ce.Reply("Error parsing arguments: %v", err)
		return
	}
	if len(args) == 0 {
		ce.Reply("Usage: `$cmdprefix exec <command> [arg=value ...]`")
		return
	}
	client := commandClient(ce, true)
	if client == nil {
		return
	}
	guildID := ce.Portal.Metadata.(*discordid.PortalMetadata).GuildID
	channelID := discordid.ParseChannelPortalID(ce.Portal.ID)
	cmd, err := client.getCommand(ce.Ctx, guildID, channelID, strings.ToLower(args[0]))
	if err != nil {
		ce.Reply("Error searching for commands: %v", err)
		return
	}
	if cmd == nil {
		ce.Reply("Command %q not found", args[0])
		return
	}
	options, err := executeCommand(cmd, args[1:])
	if err != nil {
		ce.Reply("Error parsing arguments: %v\n\nUsage: %s", err, formatCommand(cmd))
		return
	}
	nonce := discordid.GenerateNonce()
	pendingEvent := *ce
	pendingEvent.Ctx = context.WithoutCancel(ce.Ctx)
	client.pendingInteractionsLock.Lock()
	if client.pendingInteractions == nil {
		client.pendingInteractions = make(map[string]*pendingInteraction)
	}
	client.pendingInteractions[nonce] = &pendingInteraction{event: &pendingEvent}
	client.pendingInteractionsLock.Unlock()
	err = client.Session.SendInteractions(guildID, channelID, cmd, options, nonce, makeDiscordReferer(guildID, channelID, ""), discordgo.WithContext(ce.Ctx))
	if err != nil {
		if pending := client.takeInteraction(nonce); pending != nil {
			pending.Reply("Error sending interaction: %v", err)
		}
		return
	}
	client.pendingInteractionsLock.Lock()
	if pending := client.pendingInteractions[nonce]; pending != nil {
		pending.timer = time.AfterFunc(10*time.Second, func() {
			if pending := client.takeInteraction(nonce); pending != nil {
				pending.Reply("Timed out waiting for interaction success")
			}
		})
	}
	client.pendingInteractionsLock.Unlock()
}

type pendingInteraction struct {
	event *commands.Event
	timer *time.Timer
}

func (d *DiscordClient) takeInteraction(nonce string) *commands.Event {
	d.pendingInteractionsLock.Lock()
	defer d.pendingInteractionsLock.Unlock()
	pending := d.pendingInteractions[nonce]
	if pending == nil {
		return nil
	}
	delete(d.pendingInteractions, nonce)
	if pending.timer != nil {
		pending.timer.Stop()
	}
	return pending.event
}

func (d *DiscordClient) handleInteractionSuccess(evt *discordgo.InteractionSuccess) {
	if pending := d.takeInteraction(evt.Nonce); pending != nil {
		pending.React("✅")
	}
}
