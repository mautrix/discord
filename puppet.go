package main

import (
	"fmt"
	"regexp"
	"sync"

	log "maunium.net/go/maulogger/v2"

	"github.com/bwmarrin/discordgo"

	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/bridge"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-discord/database"
)

type Puppet struct {
	*database.Puppet

	bridge *DiscordBridge
	log    log.Logger

	MXID id.UserID

	customIntent *appservice.IntentAPI
	customUser   *User

	syncLock sync.Mutex
}

var _ bridge.Ghost = (*Puppet)(nil)

func (puppet *Puppet) GetMXID() id.UserID {
	return puppet.MXID
}

var userIDRegex *regexp.Regexp

func (br *DiscordBridge) NewPuppet(dbPuppet *database.Puppet) *Puppet {
	return &Puppet{
		Puppet: dbPuppet,
		bridge: br,
		log:    br.Log.Sub(fmt.Sprintf("Puppet/%s", dbPuppet.ID)),

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
		puppet.log.Warnln("Failed to update displayname:", err)
	} else {
		go puppet.updatePortalMeta(func(portal *Portal) {
			if portal.UpdateName(puppet.Name) {
				portal.Update()
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
	puppet.Avatar = info.Avatar
	puppet.AvatarSet = false

	if puppet.Avatar == "" {
		// TODO should we just use discord's default avatars?
		puppet.AvatarURL = id.ContentURI{}
	} else {
		url, err := uploadAvatar(puppet.DefaultIntent(), info.AvatarURL(""))
		if err != nil {
			puppet.log.Warnln("Failed to reupload user avatar:", err)
			return true
		}
		puppet.AvatarURL = url
	}

	err := puppet.DefaultIntent().SetAvatarURL(puppet.AvatarURL)
	if err != nil {
		puppet.log.Warnln("Failed to update avatar:", err)
	} else {
		go puppet.updatePortalMeta(func(portal *Portal) {
			if portal.UpdateAvatarFromPuppet(puppet) {
				portal.Update()
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
		if puppet.Name != "" {
			return
		}
		var err error
		puppet.log.Debugfln("Fetching info through %s to update", source.DiscordID)
		info, err = source.Session.User(puppet.ID)
		if err != nil {
			puppet.log.Errorfln("Failed to fetch info through %s: %v", source.DiscordID, err)
			return
		}
	}

	err := puppet.DefaultIntent().EnsureRegistered()
	if err != nil {
		puppet.log.Errorln("Failed to ensure registered:", err)
	}

	changed := false
	changed = puppet.UpdateName(info) || changed
	changed = puppet.UpdateAvatar(info) || changed
	if changed {
		puppet.Update()
	}
}
