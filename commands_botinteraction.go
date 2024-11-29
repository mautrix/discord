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

package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/google/shlex"

	"maunium.net/go/mautrix/bridge/commands"
)

var HelpSectionDiscordBots = commands.HelpSection{Name: "Discord bot interaction", Order: 30}

var cmdCommands = &commands.FullHandler{
	Func:    wrapCommand(fnCommands),
	Name:    "commands",
	Aliases: []string{"cmds", "cs"},
	Help: commands.HelpMeta{
		Section:     HelpSectionDiscordBots,
		Description: "View parameters of bot interaction commands on Discord",
		Args:        "search <_query_> OR help <_command_>",
	},
	RequiresPortal: true,
	RequiresLogin:  true,
}

var cmdExec = &commands.FullHandler{
	Func:    wrapCommand(fnExec),
	Name:    "exec",
	Aliases: []string{"command", "cmd", "c", "exec", "e"},
	Help: commands.HelpMeta{
		Section:     HelpSectionDiscordBots,
		Description: "Run bot interaction commands on Discord",
		Args:        "<_command_> [_arg=value ..._]",
	},
	RequiresLogin:  true,
	RequiresPortal: true,
}

func (portal *Portal) getCommand(user *User, command string) (*discordgo.ApplicationCommand, error) {
	portal.commandsLock.Lock()
	defer portal.commandsLock.Unlock()
	cmd, ok := portal.commands[command]
	if !ok {
		results, err := user.Session.ApplicationCommandsSearch(portal.Key.ChannelID, command, portal.RefererOpt(""))
		if err != nil {
			return nil, err
		}
		for _, result := range results {
			if result.Name == command {
				portal.commands[result.Name] = result
				cmd = result
				break
			}
		}
		if cmd == nil {
			return nil, nil
		}
	}
	return cmd, nil
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

func fnCommands(ce *WrappedCommandEvent) {
	if len(ce.Args) < 2 {
		ce.Reply("**Usage**: `$cmdprefix commands search <_query_>` OR `$cmdprefix commands help <_command_>`")
		return
	}
	subcmd := strings.ToLower(ce.Args[0])
	if subcmd == "search" {
		results, err := ce.User.Session.ApplicationCommandsSearch(ce.Portal.Key.ChannelID, ce.Args[1], ce.Portal.RefererOpt(""))
		if err != nil {
			ce.Reply("Error searching for commands: %v", err)
			return
		}
		formatted := make([]string, len(results))
		ce.Portal.commandsLock.Lock()
		for i, result := range results {
			ce.Portal.commands[result.Name] = result
			formatted[i] = indent(formatCommand(result), "  ")
			formatted[i] = "*" + formatted[i][1:]
		}
		ce.Portal.commandsLock.Unlock()
		ce.Reply("Found results:\n" + strings.Join(formatted, "\n"))
	} else if subcmd == "help" {
		command := strings.ToLower(ce.Args[1])
		cmd, err := ce.Portal.getCommand(ce.User, command)
		if err != nil {
			ce.Reply("Error searching for commands: %v", err)
		} else if cmd == nil {
			ce.Reply("Command %q not found", command)
		} else {
			ce.Reply(formatCommand(cmd))
		}
	}
}

func fnExec(ce *WrappedCommandEvent) {
	if len(ce.Args) == 0 {
		ce.Reply("**Usage**: `$cmdprefix exec <command> [arg=value ...]`")
		return
	}
	args, err := shlex.Split(ce.RawArgs)
	if err != nil {
		ce.Reply("Error parsing args with shlex: %v", err)
		return
	}
	command := strings.ToLower(args[0])
	cmd, err := ce.Portal.getCommand(ce.User, command)
	if err != nil {
		ce.Reply("Error searching for commands: %v", err)
	} else if cmd == nil {
		ce.Reply("Command %q not found", command)
	} else if options, err := executeCommand(cmd, args[1:]); err != nil {
		ce.Reply("Error parsing arguments: %v\n\n**Usage:** "+formatCommand(cmd), err)
	} else {
		nonce := generateNonce()
		ce.User.pendingInteractionsLock.Lock()
		ce.User.pendingInteractions[nonce] = ce
		ce.User.pendingInteractionsLock.Unlock()
		err = ce.User.Session.SendInteractions(ce.Portal.GuildID, ce.Portal.Key.ChannelID, cmd, options, nonce, ce.Portal.RefererOpt(""))
		if err != nil {
			ce.Reply("Error sending interaction: %v", err)
			ce.User.pendingInteractionsLock.Lock()
			delete(ce.User.pendingInteractions, nonce)
			ce.User.pendingInteractionsLock.Unlock()
		} else {
			go func() {
				time.Sleep(10 * time.Second)
				ce.User.pendingInteractionsLock.Lock()
				if _, stillWaiting := ce.User.pendingInteractions[nonce]; stillWaiting {
					delete(ce.User.pendingInteractions, nonce)
					ce.Reply("Timed out waiting for interaction success")
				}
				ce.User.pendingInteractionsLock.Unlock()
			}()
		}
	}
}
