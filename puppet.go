package main

import (
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/bwmarrin/discordgo"
	"github.com/rs/zerolog"

	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/bridge"
	"maunium.net/go/mautrix/bridge/bridgeconfig"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-discord/database"
)

type Puppet struct {
	*database.Puppet

	bridge *DiscordBridge
	log    zerolog.Logger

	MXID id.UserID

	customIntent *appservice.IntentAPI
	customUser   *User

	syncLock sync.Mutex
}

var _ bridge.Ghost = (*Puppet)(nil)
var _ bridge.GhostWithProfile = (*Puppet)(nil)

func (puppet *Puppet) GetMXID() id.UserID {
	return puppet.MXID
}

var userIDRegex *regexp.Regexp

func (br *DiscordBridge) NewPuppet(dbPuppet *database.Puppet) *Puppet {
	return &Puppet{
		Puppet: dbPuppet,
		bridge: br,
		log:    br.ZLog.With().Str("discord_user_id", dbPuppet.ID).Logger(),

		MXID: br.FormatPuppetMXID(dbPuppet.ID),
	}
}

func (br *DiscordBridge) ParsePuppetMXID(mxid id.UserID) (string, bool) {
	if userIDRegex == nil {
		pattern := fmt.Sprintf(
			"^@%s:%s$",
			br.Config.Bridge.FormatUsername("([0-9]+)"),
			br.Config.Homeserver.Domain,
		)

		userIDRegex = regexp.MustCompile(pattern)
	}

	match := userIDRegex.FindStringSubmatch(string(mxid))
	if len(match) == 2 {
		return match[1], true
	}

	return "", false
}

func (br *DiscordBridge) GetPuppetByMXID(mxid id.UserID) *Puppet {
	discordID, ok := br.ParsePuppetMXID(mxid)
	if !ok {
		return nil
	}

	return br.GetPuppetByID(discordID)
}

func (br *DiscordBridge) GetPuppetByID(id string) *Puppet {
	br.puppetsLock.Lock()
	defer br.puppetsLock.Unlock()

	puppet, ok := br.puppets[id]
	if !ok {
		dbPuppet := br.DB.Puppet.Get(id)
		if dbPuppet == nil {
			dbPuppet = br.DB.Puppet.New()
			dbPuppet.ID = id
			dbPuppet.Insert()
		}

		puppet = br.NewPuppet(dbPuppet)
		br.puppets[puppet.ID] = puppet
	}

	return puppet
}

func (br *DiscordBridge) GetPuppetByCustomMXID(mxid id.UserID) *Puppet {
	br.puppetsLock.Lock()
	defer br.puppetsLock.Unlock()

	puppet, ok := br.puppetsByCustomMXID[mxid]
	if !ok {
		dbPuppet := br.DB.Puppet.GetByCustomMXID(mxid)
		if dbPuppet == nil {
			return nil
		}

		puppet = br.NewPuppet(dbPuppet)
		br.puppets[puppet.ID] = puppet
		br.puppetsByCustomMXID[puppet.CustomMXID] = puppet
	}

	return puppet
}

func (br *DiscordBridge) GetAllPuppetsWithCustomMXID() []*Puppet {
	return br.dbPuppetsToPuppets(br.DB.Puppet.GetAllWithCustomMXID())
}

func (br *DiscordBridge) GetAllPuppets() []*Puppet {
	return br.dbPuppetsToPuppets(br.DB.Puppet.GetAll())
}

func (br *DiscordBridge) dbPuppetsToPuppets(dbPuppets []*database.Puppet) []*Puppet {
	br.puppetsLock.Lock()
	defer br.puppetsLock.Unlock()

	output := make([]*Puppet, len(dbPuppets))
	for index, dbPuppet := range dbPuppets {
		if dbPuppet == nil {
			continue
		}

		puppet, ok := br.puppets[dbPuppet.ID]
		if !ok {
			puppet = br.NewPuppet(dbPuppet)
			br.puppets[dbPuppet.ID] = puppet

			if dbPuppet.CustomMXID != "" {
				br.puppetsByCustomMXID[dbPuppet.CustomMXID] = puppet
			}
		}

		output[index] = puppet
	}

	return output
}

func (br *DiscordBridge) FormatPuppetMXID(did string) id.UserID {
	return id.NewUserID(
		br.Config.Bridge.FormatUsername(did),
		br.Config.Homeserver.Domain,
	)
}

func (puppet *Puppet) GetDisplayname() string {
	return puppet.Name
}

func (puppet *Puppet) GetAvatarURL() id.ContentURI {
	return puppet.AvatarURL
}

func (puppet *Puppet) DefaultIntent() *appservice.IntentAPI {
	return puppet.bridge.AS.Intent(puppet.MXID)
}

func (puppet *Puppet) IntentFor(portal *Portal) *appservice.IntentAPI {
	if puppet.customIntent == nil || (portal.Key.Receiver != "" && portal.Key.Receiver != puppet.ID) {
		return puppet.DefaultIntent()
	}

	return puppet.customIntent
}

func (puppet *Puppet) CustomIntent() *appservice.IntentAPI {
	if puppet == nil {
		return nil
	}
	return puppet.customIntent
}

func (puppet *Puppet) updatePortalMeta(meta func(portal *Portal)) {
	for _, portal := range puppet.bridge.GetDMPortalsWith(puppet.ID) {
		// Get room create lock to prevent races between receiving contact info and room creation.
		portal.roomCreateLock.Lock()
		meta(portal)
		portal.roomCreateLock.Unlock()
	}
}

func (puppet *Puppet) UpdateName(info *discordgo.User) bool {
	newName := puppet.bridge.Config.Bridge.FormatDisplayname(info)
	if puppet.Name == newName && puppet.NameSet {
		return false
	}
	puppet.Name = newName
	puppet.NameSet = false
	err := puppet.DefaultIntent().SetDisplayName(newName)
	if err != nil {
		puppet.log.Warn().Err(err).Msg("Failed to update displayname")
	} else {
		go puppet.updatePortalMeta(func(portal *Portal) {
			if portal.UpdateNameDirect(puppet.Name, false) {
				portal.Update()
				portal.UpdateBridgeInfo()
			}
		})
		puppet.NameSet = true
	}
	return true
}

func (puppet *Puppet) UpdateAvatar(info *discordgo.User) bool {
	if puppet.Avatar == info.Avatar && puppet.AvatarSet {
		return false
	}
	avatarChanged := info.Avatar != puppet.Avatar
	puppet.Avatar = info.Avatar
	puppet.AvatarSet = false
	puppet.AvatarURL = id.ContentURI{}

	if puppet.Avatar != "" && (puppet.AvatarURL.IsEmpty() || avatarChanged) {
		downloadURL := discordgo.EndpointUserAvatar(info.ID, info.Avatar)
		ext := "png"
		if strings.HasPrefix(info.Avatar, "a_") {
			downloadURL = discordgo.EndpointUserAvatarAnimated(info.ID, info.Avatar)
			ext = "gif"
		}
		url := puppet.bridge.Config.Bridge.MediaPatterns.Avatar(info.ID, info.Avatar, ext)
		if url.IsEmpty() {
			var err error
			url, err = uploadAvatar(puppet.DefaultIntent(), downloadURL)
			if err != nil {
				puppet.log.Warn().Err(err).Str("avatar_id", puppet.Avatar).Msg("Failed to reupload user avatar")
				return true
			}
		}
		puppet.AvatarURL = url
	}

	err := puppet.DefaultIntent().SetAvatarURL(puppet.AvatarURL)
	if err != nil {
		puppet.log.Warn().Err(err).Msg("Failed to update avatar")
	} else {
		go puppet.updatePortalMeta(func(portal *Portal) {
			if portal.UpdateAvatarFromPuppet(puppet) {
				portal.Update()
				portal.UpdateBridgeInfo()
			}
		})
		puppet.AvatarSet = true
	}
	return true
}

func (puppet *Puppet) UpdateInfo(source *User, info *discordgo.User) {
	puppet.syncLock.Lock()
	defer puppet.syncLock.Unlock()

	if info == nil || len(info.Username) == 0 || len(info.Discriminator) == 0 {
		if puppet.Name != "" || source == nil {
			return
		}
		var err error
		puppet.log.Debug().Str("source_user", source.DiscordID).Msg("Fetching info through user to update puppet")
		info, err = source.Session.User(puppet.ID)
		if err != nil {
			puppet.log.Error().Err(err).Str("source_user", source.DiscordID).Msg("Failed to fetch info through user")
			return
		}
	}

	err := puppet.DefaultIntent().EnsureRegistered()
	if err != nil {
		puppet.log.Error().Err(err).Msg("Failed to ensure registered")
	}

	changed := false
	changed = puppet.UpdateContactInfo(info) || changed
	changed = puppet.UpdateName(info) || changed
	changed = puppet.UpdateAvatar(info) || changed
	if changed {
		puppet.Update()
	}
}

func (puppet *Puppet) UpdateContactInfo(info *discordgo.User) bool {
	changed := false
	if puppet.Username != info.Username {
		puppet.Username = info.Username
		changed = true
	}
	if puppet.Discriminator != info.Discriminator {
		puppet.Discriminator = info.Discriminator
		changed = true
	}
	if puppet.IsBot != info.Bot {
		puppet.IsBot = info.Bot
		changed = true
	}
	if changed {
		puppet.ContactInfoSet = false
		puppet.ResendContactInfo()
		return true
	}
	return false
}

func (puppet *Puppet) ResendContactInfo() {
	if puppet.bridge.Config.Homeserver.Software != bridgeconfig.SoftwareHungry || puppet.ContactInfoSet {
		return
	}
	contactInfo := map[string]any{
		"com.beeper.bridge.identifiers": []string{
			fmt.Sprintf("discord:%s#%s", puppet.Username, puppet.Discriminator),
		},
		"com.beeper.bridge.remote_id":      puppet.ID,
		"com.beeper.bridge.service":        puppet.bridge.BeeperServiceName,
		"com.beeper.bridge.network":        puppet.bridge.BeeperNetworkName,
		"com.beeper.bridge.is_network_bot": puppet.IsBot,
	}
	err := puppet.DefaultIntent().BeeperUpdateProfile(contactInfo)
	if err != nil {
		puppet.log.Warn().Err(err).Msg("Failed to store custom contact info in profile")
	} else {
		puppet.ContactInfoSet = true
	}
}
