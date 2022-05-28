package main

import (
	"bytes"
	"image"
	"io"
	"net/http"
	"strings"

	"github.com/bwmarrin/discordgo"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

func (portal *Portal) downloadDiscordAttachment(url string) ([]byte, error) {
	// We might want to make this save to disk in the future. Discord defaults
	// to 8mb for all attachments to a messages for non-nitro users and
	// non-boosted servers.
	//
	// If the user has nitro classic, their limit goes up to 50mb but if a user
	// has regular nitro the limit is increased to 100mb.
	//
	// Servers boosted to level 2 will have the limit bumped to 50mb.

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	for key, value := range discordgo.DroidDownloadHeaders {
		req.Header.Set(key, value)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func (portal *Portal) downloadMatrixAttachment(eventID id.EventID, content *event.MessageEventContent) ([]byte, error) {
	var file *event.EncryptedFileInfo
	rawMXC := content.URL

	if content.File != nil {
		file = content.File
		rawMXC = file.URL
	}

	mxc, err := rawMXC.Parse()
	if err != nil {
		portal.log.Errorln("Malformed content URL in %s: %v", eventID, err)

		return nil, err
	}

	data, err := portal.MainIntent().DownloadBytes(mxc)
	if err != nil {
		portal.log.Errorfln("Failed to download media in %s: %v", eventID, err)

		return nil, err
	}

	if file != nil {
		err = file.DecryptInPlace(data)
		if err != nil {
			portal.log.Errorfln("Failed to decrypt media in %s: %v", eventID, err)
			return nil, err
		}
	}

	return data, nil
}

func (portal *Portal) uploadMatrixAttachment(intent *appservice.IntentAPI, data []byte, content *event.MessageEventContent) error {
	req := mautrix.ReqUploadMedia{
		ContentBytes: data,
		ContentType:  content.Info.MimeType,
	}
	var mxc id.ContentURI
	if portal.bridge.Config.Homeserver.AsyncMedia {
		uploaded, err := intent.UnstableUploadAsync(req)
		if err != nil {
			return err
		}
		mxc = uploaded.ContentURI
	} else {
		uploaded, err := intent.UploadMedia(req)
		if err != nil {
			return err
		}
		mxc = uploaded.ContentURI
	}

	content.URL = mxc.CUString()
	content.Info.Size = len(data)

	if content.Info.Width == 0 && content.Info.Height == 0 && strings.HasPrefix(content.Info.MimeType, "image/") {
		cfg, _, _ := image.DecodeConfig(bytes.NewReader(data))
		content.Info.Width = cfg.Width
		content.Info.Height = cfg.Height
	}

	return nil
}
