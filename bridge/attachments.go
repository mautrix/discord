package bridge

import (
	"bytes"
	"image"
	"io/ioutil"
	"net/http"
	"strings"

	"github.com/bwmarrin/discordgo"

	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

func (p *Portal) downloadDiscordAttachment(url string) ([]byte, error) {
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

	req.Header.Set("User-Agent", discordgo.DroidBrowserUserAgent)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}

	defer resp.Body.Close()

	return ioutil.ReadAll(resp.Body)
}

func (p *Portal) downloadMatrixAttachment(eventID id.EventID, content *event.MessageEventContent) ([]byte, error) {
	var file *event.EncryptedFileInfo
	rawMXC := content.URL

	if content.File != nil {
		file = content.File
		rawMXC = file.URL
	}

	mxc, err := rawMXC.Parse()
	if err != nil {
		p.log.Errorln("Malformed content URL in %s: %v", eventID, err)

		return nil, err
	}

	data, err := p.MainIntent().DownloadBytes(mxc)
	if err != nil {
		p.log.Errorfln("Failed to download media in %s: %v", eventID, err)

		return nil, err
	}

	if file != nil {
		data, err = file.Decrypt(data)
		if err != nil {
			p.log.Errorfln("Failed to decrypt media in %s: %v", eventID, err)
			return nil, err
		}
	}

	return data, nil
}

func (p *Portal) uploadMatrixAttachment(intent *appservice.IntentAPI, data []byte, content *event.MessageEventContent) error {
	uploaded, err := intent.UploadBytes(data, content.Info.MimeType)
	if err != nil {
		return err
	}

	content.URL = uploaded.ContentURI.CUString()

	content.Info.Size = len(data)

	if content.Info.Width == 0 && content.Info.Height == 0 && strings.HasPrefix(content.Info.MimeType, "image/") {
		cfg, _, _ := image.DecodeConfig(bytes.NewReader(data))
		content.Info.Width = cfg.Width
		content.Info.Height = cfg.Height
	}

	return nil
}
