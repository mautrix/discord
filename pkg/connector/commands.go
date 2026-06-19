// Network-specific commands: exec, guilds, rejoin-space
// Implemented in Group 6 (Task 6.3).
//
// Commands are registered via GetNetworkCommandHandlers(), which main.go wires
// into br.Bridge.Commands via PostInit. This keeps the connector self-contained
// while avoiding a new framework interface (OQ-11).
//
// FR-52: rejoin-space — invite user back to main / DM / guild space.
// FR-53: guilds — status, bridge, unbridge, bridging-mode subcommands.
// FR-54: exec / commands — slash-command interaction with nonce dedup + ~10s timeout.
package connector

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"

	bvcommands "maunium.net/go/mautrix/bridgev2/commands"
	"maunium.net/go/mautrix/id"
)

// HelpSectionDiscordBots groups the Discord bot-interaction commands in help output.
var HelpSectionDiscordBots = bvcommands.HelpSection{Name: "Discord bot interaction", Order: 30}

// GetNetworkCommandHandlers returns the Discord-specific commands that should
// be added to the bridge command processor. Call this from PostInit:
//
//	br.Bridge.Commands.(*commands.Processor).AddHandlers(c.GetNetworkCommandHandlers()...)
func (dc *DiscordConnector) GetNetworkCommandHandlers() []bvcommands.CommandHandler {
	return []bvcommands.CommandHandler{
		cmdExec,
		cmdDiscordCommands,
		cmdGuilds,
		cmdRejoinSpace,
	}
}

// ---------------------------------------------------------------------------
// exec / commands (FR-54)
// ---------------------------------------------------------------------------

var cmdExec = &bvcommands.FullHandler{
	Func:    fnExec,
	Name:    "exec",
	Aliases: []string{"command", "cmd", "c", "e"},
	Help: bvcommands.HelpMeta{
		Section:     HelpSectionDiscordBots,
		Description: "Run a Discord slash command in this channel.",
		Args:        "<_command_> [_arg=value ..._]",
	},
	RequiresLogin:  true,
	RequiresPortal: true,
}

var cmdDiscordCommands = &bvcommands.FullHandler{
	Func:    fnDiscordCommands,
	Name:    "commands",
	Aliases: []string{"cmds", "cs"},
	Help: bvcommands.HelpMeta{
		Section:     HelpSectionDiscordBots,
		Description: "View Discord bot slash-command parameters.",
		Args:        "search <_query_> OR help <_command_>",
	},
	RequiresLogin:  true,
	RequiresPortal: true,
}

// portalChannelID returns the Discord channel snowflake for the current portal.
func portalChannelID(ce *bvcommands.Event) string {
	if ce.Portal == nil {
		return ""
	}
	return string(ce.Portal.ID)
}

// portalGuildID returns the Discord guild snowflake from the portal's PortalMeta,
// or "" for DMs.
func portalGuildID(ce *bvcommands.Event) string {
	if ce.Portal == nil {
		return ""
	}
	meta, ok := ce.Portal.Metadata.(*PortalMeta)
	if !ok || meta == nil {
		return ""
	}
	return meta.GuildID
}

// discordClientFromCE returns the DiscordClient for the default login of the
// command sender, or nil if the user is not logged in.
func discordClientFromCE(ce *bvcommands.Event) *DiscordClient {
	login := ce.User.GetDefaultLogin()
	if login == nil {
		return nil
	}
	dc, ok := login.Client.(*DiscordClient)
	if !ok {
		return nil
	}
	return dc
}

// getOrSearchCommand looks up name in the per-portal command cache, fetching
// from Discord if not found.
func (dc *DiscordClient) getOrSearchCommand(channelID, name string) (*discordgo.ApplicationCommand, error) {
	dc.commandCacheLock.Lock()
	defer dc.commandCacheLock.Unlock()

	if dc.commandCache == nil {
		dc.commandCache = make(map[string]map[string]*discordgo.ApplicationCommand)
	}
	byChannel, ok := dc.commandCache[channelID]
	if ok {
		if cmd, found := byChannel[name]; found {
			return cmd, nil
		}
	}

	results, err := dc.Session.ApplicationCommandsSearch(channelID, name)
	if err != nil {
		return nil, err
	}
	if byChannel == nil {
		byChannel = make(map[string]*discordgo.ApplicationCommand)
		dc.commandCache[channelID] = byChannel
	}
	for _, result := range results {
		byChannel[result.Name] = result
	}
	if cmd, found := byChannel[name]; found {
		return cmd, nil
	}
	return nil, nil
}

// cacheSearchResults stores search results in the per-portal command cache.
func (dc *DiscordClient) cacheSearchResults(channelID string, results []*discordgo.ApplicationCommand) {
	dc.commandCacheLock.Lock()
	defer dc.commandCacheLock.Unlock()
	if dc.commandCache == nil {
		dc.commandCache = make(map[string]map[string]*discordgo.ApplicationCommand)
	}
	byChannel := dc.commandCache[channelID]
	if byChannel == nil {
		byChannel = make(map[string]*discordgo.ApplicationCommand)
		dc.commandCache[channelID] = byChannel
	}
	for _, result := range results {
		byChannel[result.Name] = result
	}
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

func indentLines(text, prefix string) string {
	split := strings.Split(text, "\n")
	for i, part := range split {
		split[i] = prefix + part
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
			subopts[i] = indentLines(formatOption(subopt), "  ")
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

// splitArgs splits a raw argument string respecting double-quoted tokens, similar
// to a minimal POSIX shell word split. Used by fnExec to support arg values with
// spaces (e.g. exec roll sides="two dice").
func splitArgs(raw string) ([]string, error) {
	var args []string
	var current strings.Builder
	inQuote := false
	for i := 0; i < len(raw); i++ {
		ch := raw[i]
		switch {
		case ch == '"' && !inQuote:
			inQuote = true
		case ch == '"' && inQuote:
			inQuote = false
		case ch == ' ' && !inQuote:
			if current.Len() > 0 {
				args = append(args, current.String())
				current.Reset()
			}
		default:
			current.WriteByte(ch)
		}
	}
	if inQuote {
		return nil, fmt.Errorf("unclosed quote in arguments")
	}
	if current.Len() > 0 {
		args = append(args, current.String())
	}
	return args, nil
}

func fnDiscordCommands(ce *bvcommands.Event) {
	if len(ce.Args) < 2 {
		ce.Reply("**Usage**: `$cmdprefix commands search <_query_>` OR `$cmdprefix commands help <_command_>`")
		return
	}
	dc := discordClientFromCE(ce)
	if dc == nil || dc.Session == nil {
		ce.Reply("You are not connected to Discord.")
		return
	}
	channelID := portalChannelID(ce)
	subcmd := strings.ToLower(ce.Args[0])
	switch subcmd {
	case "search":
		results, err := dc.Session.ApplicationCommandsSearch(channelID, ce.Args[1])
		if err != nil {
			ce.Reply("Error searching for commands: %v", err)
			return
		}
		dc.cacheSearchResults(channelID, results)
		formatted := make([]string, len(results))
		for i, result := range results {
			formatted[i] = indentLines(formatCommand(result), "  ")
			formatted[i] = "*" + formatted[i][1:]
		}
		ce.Reply("Found results:\n" + strings.Join(formatted, "\n"))
	case "help":
		command := strings.ToLower(ce.Args[1])
		cmd, err := dc.getOrSearchCommand(channelID, command)
		if err != nil {
			ce.Reply("Error searching for commands: %v", err)
		} else if cmd == nil {
			ce.Reply("Command %q not found", command)
		} else {
			ce.Reply(formatCommand(cmd))
		}
	default:
		ce.Reply("**Usage**: `$cmdprefix commands search <_query_>` OR `$cmdprefix commands help <_command_>`")
	}
}

func fnExec(ce *bvcommands.Event) {
	if len(ce.Args) == 0 {
		ce.Reply("**Usage**: `$cmdprefix exec <command> [arg=value ...]`")
		return
	}
	dc := discordClientFromCE(ce)
	if dc == nil || dc.Session == nil {
		ce.Reply("You are not connected to Discord.")
		return
	}
	args, err := splitArgs(ce.RawArgs)
	if err != nil {
		ce.Reply("Error parsing arguments: %v", err)
		return
	}
	channelID := portalChannelID(ce)
	guildID := portalGuildID(ce)
	command := strings.ToLower(args[0])
	cmd, err := dc.getOrSearchCommand(channelID, command)
	if err != nil {
		ce.Reply("Error searching for commands: %v", err)
		return
	} else if cmd == nil {
		ce.Reply("Command %q not found", command)
		return
	}
	options, err := executeCommand(cmd, args[1:])
	if err != nil {
		ce.Reply("Error parsing arguments: %v\n\n**Usage:** "+formatCommand(cmd), err)
		return
	}

	nonce := generateNonce()

	dc.pendingInteractionsLock.Lock()
	if dc.pendingInteractions == nil {
		dc.pendingInteractions = make(map[string]interactionPending)
	}
	dc.pendingInteractions[nonce] = interactionPending{
		react: func(key string) { ce.React(key) },
	}
	dc.pendingInteractionsLock.Unlock()

	err = dc.Session.SendInteractions(guildID, channelID, cmd, options, nonce)
	if err != nil {
		ce.Reply("Error sending interaction: %v", err)
		dc.pendingInteractionsLock.Lock()
		delete(dc.pendingInteractions, nonce)
		dc.pendingInteractionsLock.Unlock()
		return
	}

	// Time out after ~10 seconds if INTERACTION_SUCCESS never arrives (FR-54).
	go func() {
		time.Sleep(10 * time.Second)
		dc.pendingInteractionsLock.Lock()
		if _, stillWaiting := dc.pendingInteractions[nonce]; stillWaiting {
			delete(dc.pendingInteractions, nonce)
			dc.pendingInteractionsLock.Unlock()
			ce.Reply("Timed out waiting for interaction success")
		} else {
			dc.pendingInteractionsLock.Unlock()
		}
	}()
}

// ---------------------------------------------------------------------------
// guilds (FR-53)
// ---------------------------------------------------------------------------

var cmdGuilds = &bvcommands.FullHandler{
	Func:    fnGuilds,
	Name:    "guilds",
	Aliases: []string{"servers", "guild", "server"},
	Help: bvcommands.HelpMeta{
		Section:     bvcommands.HelpSectionChats,
		Description: "Guild bridging management.",
		Args:        "<status/bridge/unbridge/bridging-mode> [_guild ID_] [...]",
	},
	RequiresLogin: true,
}

const smallGuildsHelp = "**Usage**: `$cmdprefix guilds <help/status/bridge/unbridge/bridging-mode> [guild ID] [...]`"

const fullGuildsHelp = smallGuildsHelp + `

* **help** - View this help message.
* **status** - View the list of guilds and their bridging status.
* **bridge <_guild ID_> [--entire]** - Enable bridging for a guild. The --entire flag auto-creates portals for all channels.
* **bridging-mode <_guild ID_> <_mode_>** - Set the mode for bridging messages and new channels in a guild.
* **unbridge <_guild ID_>** - Unbridge a guild and delete all channel portal rooms.`

func fnGuilds(ce *bvcommands.Event) {
	if len(ce.Args) == 0 {
		ce.Reply(fullGuildsHelp)
		return
	}
	subcommand := strings.ToLower(ce.Args[0])
	ce.Args = ce.Args[1:]
	switch subcommand {
	case "status", "list":
		fnListGuilds(ce)
	case "bridge":
		fnBridgeGuild(ce)
	case "unbridge", "delete":
		fnUnbridgeGuild(ce)
	case "bridging-mode", "mode":
		fnGuildBridgingMode(ce)
	case "help":
		ce.Reply(fullGuildsHelp)
	default:
		ce.Reply("Unknown subcommand `%s`\n\n"+smallGuildsHelp, subcommand)
	}
}

func fnListGuilds(ce *bvcommands.Event) {
	dc := discordClientFromCE(ce)
	if dc == nil || dc.Session == nil {
		ce.Reply("You are not connected to Discord.")
		return
	}
	guilds := dc.Session.State.Guilds
	if len(guilds) == 0 {
		ce.Reply("No guilds found")
		return
	}
	var items []string
	for _, guild := range guilds {
		guildPortalKey := makeGuildPortalKey(guild.ID)
		portal, err := ce.Bridge.GetPortalByKey(ce.Ctx, guildPortalKey)
		var modeStr string
		if err != nil || portal == nil {
			modeStr = string(BridgingModeNothing)
		} else if meta, ok := portal.Metadata.(*PortalMeta); ok && meta != nil {
			modeStr = string(meta.GuildBridgingMode)
			if modeStr == "" {
				modeStr = string(BridgingModeNothing)
			}
		} else {
			modeStr = string(BridgingModeNothing)
		}
		items = append(items, fmt.Sprintf("<li>%s (<code>%s</code>) - %s</li>",
			guild.Name, guild.ID, modeStr))
	}
	ce.ReplyAdvanced(fmt.Sprintf("<p>List of guilds:</p><ul>%s</ul>", strings.Join(items, "")), false, true)
}

func fnBridgeGuild(ce *bvcommands.Event) {
	if len(ce.Args) == 0 || len(ce.Args) > 2 {
		ce.Reply("**Usage**: `$cmdprefix guilds bridge <guild ID> [--entire]`")
		return
	}
	guildID := ce.Args[0]
	entire := len(ce.Args) == 2 && strings.ToLower(ce.Args[1]) == "--entire"
	guildPortalKey := makeGuildPortalKey(guildID)
	portal, err := ce.Bridge.GetPortalByKey(ce.Ctx, guildPortalKey)
	if err != nil {
		ce.Reply("Error looking up guild: %v", err)
		return
	}
	if portal == nil {
		ce.Reply("Guild %s not found — join the guild in Discord first", guildID)
		return
	}
	meta, ok := portal.Metadata.(*PortalMeta)
	if !ok || meta == nil {
		ce.Reply("Internal error: portal metadata is missing")
		return
	}
	if entire {
		meta.GuildBridgingMode = BridgingModeEverything
	} else {
		meta.GuildBridgingMode = BridgingModeCreateOnMessage
	}
	if err = portal.Save(ce.Ctx); err != nil {
		ce.Reply("Error saving bridging mode: %v", err)
		return
	}
	ce.Reply("Successfully bridged guild %s (mode: %s)", guildID, meta.GuildBridgingMode)
}

func fnUnbridgeGuild(ce *bvcommands.Event) {
	if len(ce.Args) != 1 {
		ce.Reply("**Usage**: `$cmdprefix guilds unbridge <guild ID>`")
		return
	}
	guildID := ce.Args[0]
	guildPortalKey := makeGuildPortalKey(guildID)
	portal, err := ce.Bridge.GetPortalByKey(ce.Ctx, guildPortalKey)
	if err != nil {
		ce.Reply("Error looking up guild: %v", err)
		return
	}
	if portal == nil {
		ce.Reply("Guild %s not found", guildID)
		return
	}
	meta, ok := portal.Metadata.(*PortalMeta)
	if !ok || meta == nil {
		ce.Reply("Internal error: portal metadata is missing")
		return
	}
	meta.GuildBridgingMode = BridgingModeNothing
	if err = portal.Save(ce.Ctx); err != nil {
		ce.Reply("Error saving bridging mode: %v", err)
		return
	}
	ce.Reply("Successfully unbridged guild %s", guildID)
}

const availableModes = "Available modes:\n" +
	"* `nothing` to never bridge any messages (default when unbridged)\n" +
	"* `if-portal-exists` to bridge messages in existing portals only\n" +
	"* `create-on-message` to bridge all messages and create portals if necessary (default after bridging)\n" +
	"* `everything` to bridge all messages and create portals proactively (default with --entire)\n"

func fnGuildBridgingMode(ce *bvcommands.Event) {
	if len(ce.Args) == 0 || len(ce.Args) > 2 {
		ce.Reply("**Usage**: `$cmdprefix guilds bridging-mode <guild ID> [mode]`\n\n" + availableModes)
		return
	}
	guildID := ce.Args[0]
	guildPortalKey := makeGuildPortalKey(guildID)
	portal, err := ce.Bridge.GetPortalByKey(ce.Ctx, guildPortalKey)
	if err != nil {
		ce.Reply("Error looking up guild: %v", err)
		return
	}
	if portal == nil {
		ce.Reply("Guild %s not found", guildID)
		return
	}
	meta, ok := portal.Metadata.(*PortalMeta)
	if !ok || meta == nil {
		ce.Reply("Internal error: portal metadata is missing")
		return
	}
	if len(ce.Args) == 1 {
		mode := meta.GuildBridgingMode
		if mode == "" {
			mode = BridgingModeNothing
		}
		ce.Reply("Guild %s is currently set to `%s`\n\n%s", guildID, mode, availableModes)
		return
	}
	mode := BridgingMode(strings.ToLower(ce.Args[1]))
	switch mode {
	case BridgingModeNothing, BridgingModeIfPortalExists, BridgingModeCreateOnMessage, BridgingModeEverything:
		// valid
	default:
		ce.Reply("Invalid bridging mode `%s`\n\n%s", ce.Args[1], availableModes)
		return
	}
	meta.GuildBridgingMode = mode
	if err = portal.Save(ce.Ctx); err != nil {
		ce.Reply("Error saving bridging mode: %v", err)
		return
	}
	ce.Reply("Set guild %s bridging mode to `%s`", guildID, mode)
}

// ---------------------------------------------------------------------------
// rejoin-space (FR-52)
// ---------------------------------------------------------------------------

var cmdRejoinSpace = &bvcommands.FullHandler{
	Func:    fnRejoinSpace,
	Name:    "rejoin-space",
	Aliases: []string{"rejoin_space"},
	Help: bvcommands.HelpMeta{
		Section:     bvcommands.HelpSectionChats,
		Description: "Get a re-invite to a Discord space you left.",
		Args:        "<main/dms/_guild ID_>",
	},
	RequiresLogin: true,
}

func fnRejoinSpace(ce *bvcommands.Event) {
	if len(ce.Args) == 0 {
		ce.Reply("**Usage**: `$cmdprefix rejoin-space <main/dms/guild ID>`")
		return
	}
	login := ce.User.GetDefaultLogin()
	if login == nil {
		ce.Reply("You are not logged in.")
		return
	}
	ctx := ce.Ctx
	switch strings.ToLower(ce.Args[0]) {
	case "main":
		spaceRoom, err := login.GetSpaceRoom(ctx)
		if err != nil {
			ce.Reply("Error getting space room: %v", err)
			return
		}
		if spaceRoom == "" {
			ce.Reply("Personal filtering spaces are not enabled.")
			return
		}
		if err = ce.Bridge.Bot.EnsureInvited(ctx, spaceRoom, ce.User.MXID); err != nil {
			ce.Reply("Error inviting you to your main space: %v", err)
			return
		}
		ce.Reply("Invited you to your main space ([link](%s))",
			id.RoomID(spaceRoom).URI(ce.Bridge.Matrix.ServerName()).MatrixToURL())
	case "dms":
		// DM space: find the DM-space portal for this login. In the legacy
		// bridge the DM space was a separate room; in bridgev2 it is managed
		// by the bridge's personal filtering space. Re-inviting to the
		// personal space covers both cases.
		spaceRoom, err := login.GetSpaceRoom(ctx)
		if err != nil {
			ce.Reply("Error getting DM space room: %v", err)
			return
		}
		if spaceRoom == "" {
			ce.Reply("Personal filtering spaces are not enabled.")
			return
		}
		if err = ce.Bridge.Bot.EnsureInvited(ctx, spaceRoom, ce.User.MXID); err != nil {
			ce.Reply("Error inviting you to your DM space: %v", err)
			return
		}
		ce.Reply("Invited you to your DM space ([link](%s))",
			id.RoomID(spaceRoom).URI(ce.Bridge.Matrix.ServerName()).MatrixToURL())
	default:
		guildID := ce.Args[0]
		if _, err := strconv.ParseUint(guildID, 10, 64); err != nil {
			ce.Reply("**Usage**: `$cmdprefix rejoin-space <main/dms/guild ID>`")
			return
		}
		guildPortalKey := makeGuildPortalKey(guildID)
		portal, err := ce.Bridge.GetPortalByKey(ctx, guildPortalKey)
		if err != nil {
			ce.Reply("Error looking up guild space: %v", err)
			return
		}
		if portal == nil || portal.MXID == "" {
			ce.Reply("Guild space for %s has not been created yet.", guildID)
			return
		}
		if err = ce.Bridge.Bot.EnsureInvited(ctx, portal.MXID, ce.User.MXID); err != nil {
			ce.Reply("Error inviting you to the guild space: %v", err)
			return
		}
		ce.Reply("Invited you to the guild space ([link](%s))",
			portal.MXID.URI(ce.Bridge.Matrix.ServerName()).MatrixToURL())
	}
}
