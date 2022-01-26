package bridge

import (
	"maunium.net/go/mautrix/id"
)

func (b *Bridge) updateBotProfile() {
	cfg := b.Config.Appservice.Bot

	// Set the bot's avatar.
	if cfg.Avatar != "" {
		var err error
		var mxc id.ContentURI

		if cfg.Avatar == "remove" {
			err = b.bot.SetAvatarURL(mxc)
		} else {
			mxc, err = id.ParseContentURI(cfg.Avatar)
			if err == nil {
				err = b.bot.SetAvatarURL(mxc)
			}
		}

		b.log.Warnln("failed to update the bot's avatar: %v", err)
	}

	// Update the bot's display name.
	if cfg.Displayname != "" {
		var err error

		if cfg.Displayname == "remove" {
			err = b.bot.SetDisplayName("")
		} else {
			err = b.bot.SetDisplayName(cfg.Displayname)
		}

		if err != nil {
			b.log.Warnln("failed to update the bot's display name: %v", err)
		}
	}
}
