package bridge

import (
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sync"

	log "maunium.net/go/maulogger/v2"
	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/id"

	"gitlab.com/beeper/discord/database"
)

type Puppet struct {
	*database.Puppet

	bridge *Bridge
	log    log.Logger

	MXID id.UserID

	customIntent *appservice.IntentAPI
	customUser   *User

	syncLock sync.Mutex
}

var userIDRegex *regexp.Regexp

func (b *Bridge) NewPuppet(dbPuppet *database.Puppet) *Puppet {
	return &Puppet{
		Puppet: dbPuppet,
		bridge: b,
		log:    b.log.Sub(fmt.Sprintf("Puppet/%s", dbPuppet.ID)),

		MXID: b.FormatPuppetMXID(dbPuppet.ID),
	}
}

func (b *Bridge) ParsePuppetMXID(mxid id.UserID) (string, bool) {
	if userIDRegex == nil {
		pattern := fmt.Sprintf(
			"^@%s:%s$",
			b.Config.Bridge.FormatUsername("([0-9]+)"),
			b.Config.Homeserver.Domain,
		)

		userIDRegex = regexp.MustCompile(pattern)
	}

	match := userIDRegex.FindStringSubmatch(string(mxid))
	if len(match) == 2 {
		return match[1], true
	}

	return "", false
}

func (b *Bridge) GetPuppetByMXID(mxid id.UserID) *Puppet {
	id, ok := b.ParsePuppetMXID(mxid)
	if !ok {
		return nil
	}

	return b.GetPuppetByID(id)
}

func (b *Bridge) GetPuppetByID(id string) *Puppet {
	b.puppetsLock.Lock()
	defer b.puppetsLock.Unlock()

	puppet, ok := b.puppets[id]
	if !ok {
		dbPuppet := b.db.Puppet.Get(id)
		if dbPuppet == nil {
			dbPuppet = b.db.Puppet.New()
			dbPuppet.ID = id
			dbPuppet.Insert()
		}

		puppet = b.NewPuppet(dbPuppet)
		b.puppets[puppet.ID] = puppet
	}

	return puppet
}

func (b *Bridge) GetPuppetByCustomMXID(mxid id.UserID) *Puppet {
	b.puppetsLock.Lock()
	defer b.puppetsLock.Unlock()

	puppet, ok := b.puppetsByCustomMXID[mxid]
	if !ok {
		dbPuppet := b.db.Puppet.GetByCustomMXID(mxid)
		if dbPuppet == nil {
			return nil
		}

		puppet = b.NewPuppet(dbPuppet)
		b.puppets[puppet.ID] = puppet
		b.puppetsByCustomMXID[puppet.CustomMXID] = puppet
	}

	return puppet
}

func (b *Bridge) GetAllPuppetsWithCustomMXID() []*Puppet {
	return b.dbPuppetsToPuppets(b.db.Puppet.GetAllWithCustomMXID())
}

func (b *Bridge) GetAllPuppets() []*Puppet {
	return b.dbPuppetsToPuppets(b.db.Puppet.GetAll())
}

func (b *Bridge) dbPuppetsToPuppets(dbPuppets []*database.Puppet) []*Puppet {
	b.puppetsLock.Lock()
	defer b.puppetsLock.Unlock()

	output := make([]*Puppet, len(dbPuppets))
	for index, dbPuppet := range dbPuppets {
		if dbPuppet == nil {
			continue
		}

		puppet, ok := b.puppets[dbPuppet.ID]
		if !ok {
			puppet = b.NewPuppet(dbPuppet)
			b.puppets[dbPuppet.ID] = puppet

			if dbPuppet.CustomMXID != "" {
				b.puppetsByCustomMXID[dbPuppet.CustomMXID] = puppet
			}
		}

		output[index] = puppet
	}

	return output
}

func (b *Bridge) FormatPuppetMXID(did string) id.UserID {
	return id.NewUserID(
		b.Config.Bridge.FormatUsername(did),
		b.Config.Homeserver.Domain,
	)
}

func (p *Puppet) DefaultIntent() *appservice.IntentAPI {
	return p.bridge.as.Intent(p.MXID)
}

func (p *Puppet) IntentFor(portal *Portal) *appservice.IntentAPI {
	if p.customIntent == nil || portal.Key.Receiver == p.ID {
		return p.DefaultIntent()
	}

	return p.customIntent
}

func (p *Puppet) CustomIntent() *appservice.IntentAPI {
	return p.customIntent
}

func (p *Puppet) updatePortalMeta(meta func(portal *Portal)) {
	for _, portal := range p.bridge.GetAllPortalsByID(p.ID) {
		meta(portal)
	}
}

func (p *Puppet) updateName(source *User) bool {
	user, err := source.Session.User(p.ID)
	if err != nil {
		p.log.Warnln("failed to get user from id:", err)
		return false
	}

	newName := p.bridge.Config.Bridge.FormatDisplayname(user)

	if p.DisplayName != newName {
		err := p.DefaultIntent().SetDisplayName(newName)
		if err == nil {
			p.DisplayName = newName
			go p.updatePortalName()
			p.Update()
		} else {
			p.log.Warnln("failed to set display name:", err)
		}

		return true
	}

	return false
}

func (p *Puppet) updatePortalName() {
	p.updatePortalMeta(func(portal *Portal) {
		if portal.MXID != "" {
			_, err := portal.MainIntent().SetRoomName(portal.MXID, p.DisplayName)
			if err != nil {
				portal.log.Warnln("Failed to set name:", err)
			}
		}

		portal.Name = p.DisplayName
		portal.Update()
	})
}

func (p *Puppet) uploadAvatar(intent *appservice.IntentAPI, url string) (id.ContentURI, error) {
	getResp, err := http.DefaultClient.Get(url)
	if err != nil {
		return id.ContentURI{}, fmt.Errorf("failed to download avatar: %w", err)
	}

	data, err := io.ReadAll(getResp.Body)
	getResp.Body.Close()
	if err != nil {
		return id.ContentURI{}, fmt.Errorf("failed to read avatar data: %w", err)
	}

	mime := http.DetectContentType(data)
	resp, err := intent.UploadBytes(data, mime)
	if err != nil {
		return id.ContentURI{}, fmt.Errorf("failed to upload avatar to Matrix: %w", err)
	}

	return resp.ContentURI, nil
}

func (p *Puppet) updateAvatar(source *User) bool {
	user, err := source.Session.User(p.ID)
	if err != nil {
		p.log.Warnln("Failed to get user:", err)

		return false
	}

	if p.Avatar == user.Avatar {
		return false
	}

	if user.Avatar == "" {
		p.log.Warnln("User does not have an avatar")

		return false
	}

	url, err := p.uploadAvatar(p.DefaultIntent(), user.AvatarURL(""))
	if err != nil {
		p.log.Warnln("Failed to upload user avatar:", err)

		return false
	}

	p.AvatarURL = url

	err = p.DefaultIntent().SetAvatarURL(p.AvatarURL)
	if err != nil {
		p.log.Warnln("Failed to set avatar:", err)
	}

	p.log.Debugln("Updated avatar", p.Avatar, "->", user.Avatar)
	p.Avatar = user.Avatar
	go p.updatePortalAvatar()

	return true
}

func (p *Puppet) updatePortalAvatar() {
	p.updatePortalMeta(func(portal *Portal) {
		if portal.MXID != "" {
			_, err := portal.MainIntent().SetRoomAvatar(portal.MXID, p.AvatarURL)
			if err != nil {
				portal.log.Warnln("Failed to set avatar:", err)
			}
		}

		portal.AvatarURL = p.AvatarURL
		portal.Avatar = p.Avatar
		portal.Update()
	})

}

func (p *Puppet) SyncContact(source *User) {
	p.syncLock.Lock()
	defer p.syncLock.Unlock()

	p.log.Debugln("syncing contact", p.DisplayName)

	err := p.DefaultIntent().EnsureRegistered()
	if err != nil {
		p.log.Errorln("Failed to ensure registered:", err)
	}

	update := false

	update = p.updateName(source) || update

	if p.Avatar == "" {
		update = p.updateAvatar(source) || update
		p.log.Debugln("update avatar returned", update)
	}

	if update {
		p.Update()
	}
}
