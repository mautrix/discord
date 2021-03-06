package main

import (
	"io/ioutil"
	"net/http"

	"github.com/bwmarrin/discordgo"

	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/id"
)

func (portal *Portal) getEmojiMXCByDiscordID(emojiID, name string, animated bool) id.ContentURI {
	dbEmoji := portal.bridge.DB.Emoji.GetByDiscordID(emojiID)

	if dbEmoji == nil {
		data, mimeType, err := portal.downloadDiscordEmoji(emojiID, animated)
		if err != nil {
			portal.log.Warnfln("Failed to download emoji %s from discord: %v", emojiID, err)
			return id.ContentURI{}
		}

		uri, err := portal.uploadMatrixEmoji(portal.MainIntent(), data, mimeType)
		if err != nil {
			portal.log.Warnfln("Failed to upload discord emoji %s to homeserver: %v", emojiID, err)
			return id.ContentURI{}
		}

		dbEmoji = portal.bridge.DB.Emoji.New()
		dbEmoji.DiscordID = emojiID
		dbEmoji.DiscordName = name
		dbEmoji.MatrixURL = uri
		dbEmoji.Insert()
	}

	return dbEmoji.MatrixURL
}

func (portal *Portal) downloadDiscordEmoji(id string, animated bool) ([]byte, string, error) {
	var url string
	var mimeType string

	if animated {
		// This url requests a gif, so that's what we set the mimetype to.
		url = discordgo.EndpointEmojiAnimated(id)
		mimeType = "image/gif"
	} else {
		// This url requests a png, so that's what we set the mimetype to.
		url = discordgo.EndpointEmoji(id)
		mimeType = "image/png"
	}

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, mimeType, err
	}

	req.Header.Set("User-Agent", discordgo.DroidBrowserUserAgent)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, mimeType, err
	}

	defer resp.Body.Close()

	data, err := ioutil.ReadAll(resp.Body)

	return data, mimeType, err
}

func (portal *Portal) uploadMatrixEmoji(intent *appservice.IntentAPI, data []byte, mimeType string) (id.ContentURI, error) {
	uploaded, err := intent.UploadBytes(data, mimeType)
	if err != nil {
		return id.ContentURI{}, err
	}

	return uploaded.ContentURI, nil
}
