package bridge

import (
	"fmt"
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

func (b *Bridge) FormatPuppetMXID(did string) id.UserID {
	return id.NewUserID(
		b.Config.Bridge.FormatUsername(did),
		b.Config.Homeserver.Domain,
	)
}

func (p *Puppet) DefaultIntent() *appservice.IntentAPI {
	return p.bridge.as.Intent(p.MXID)
}

func (p *Puppet) SyncContact(user *User) {
	p.syncLock.Lock()
	defer p.syncLock.Unlock()

	dUser, err := user.Session.User(p.ID)
	if err != nil {
		p.log.Warnfln("failed to sync puppet %s: %v", p.ID, err)

		return
	}

	p.DisplayName = p.bridge.Config.Bridge.FormatDisplayname(dUser)

	p.Update()
}
