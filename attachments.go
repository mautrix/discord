package main

import (
	"bytes"
	"fmt"
	"image"
	"io"
	"net/http"
	"strings"

	"maunium.net/go/mautrix/crypto/attachment"

	"github.com/bwmarrin/discordgo"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

func (portal *Portal) downloadDiscordAttachment(url string) ([]byte, error) {
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
	if resp.StatusCode > 300 {
		data, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, data)
	}
	return io.ReadAll(resp.Body)
}

func (portal *Portal) downloadMatrixAttachment(content *event.MessageEventContent) ([]byte, error) {
	var file *event.EncryptedFileInfo
	rawMXC := content.URL

	if content.File != nil {
		file = content.File
		rawMXC = file.URL
	}

	mxc, err := rawMXC.Parse()
	if err != nil {
		return nil, err
	}

	data, err := portal.MainIntent().DownloadBytes(mxc)
	if err != nil {
		return nil, err
	}

	if file != nil {
		err = file.DecryptInPlace(data)
		if err != nil {
			return nil, err
		}
	}

	return data, nil
}

func (portal *Portal) uploadMatrixAttachment(intent *appservice.IntentAPI, data []byte, content *event.MessageEventContent) error {
	content.Info.Size = len(data)
	if content.Info.Width == 0 && content.Info.Height == 0 && strings.HasPrefix(content.Info.MimeType, "image/") {
		cfg, _, _ := image.DecodeConfig(bytes.NewReader(data))
		content.Info.Width = cfg.Width
		content.Info.Height = cfg.Height
	}

	uploadMime := content.Info.MimeType
	var file *attachment.EncryptedFile
	if portal.Encrypted {
		file = attachment.NewEncryptedFile()
		file.EncryptInPlace(data)
		uploadMime = "application/octet-stream"
	}
	req := mautrix.ReqUploadMedia{
		ContentBytes: data,
		ContentType:  uploadMime,
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

	if file != nil {
		content.File = &event.EncryptedFileInfo{
			EncryptedFile: *file,
			URL:           mxc.CUString(),
		}
	} else {
		content.URL = mxc.CUString()
	}

	return nil
}
