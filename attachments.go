package main

import (
	"bytes"
	"fmt"
	"image"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/gabriel-vasile/mimetype"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/crypto/attachment"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-discord/database"
)

func downloadDiscordAttachment(url string) ([]byte, error) {
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

func uploadDiscordAttachment(url string, data []byte) error {
	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(data))
	if err != nil {
		return err
	}
	for key, value := range discordgo.DroidFetchHeaders {
		req.Header.Set(key, value)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode > 300 {
		respData, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, respData)
	}
	return nil
}

func downloadMatrixAttachment(intent *appservice.IntentAPI, content *event.MessageEventContent) ([]byte, error) {
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

	data, err := intent.DownloadBytes(mxc)
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

func (br *DiscordBridge) uploadMatrixAttachment(intent *appservice.IntentAPI, data []byte, url string, encrypt bool, meta AttachmentMeta) (*database.File, error) {
	dbFile := br.DB.File.New()
	dbFile.Timestamp = time.Now()
	dbFile.URL = url
	dbFile.ID = meta.AttachmentID
	dbFile.EmojiName = meta.EmojiName
	dbFile.Size = len(data)
	dbFile.MimeType = mimetype.Detect(data).String()
	if meta.MimeType == "" {
		meta.MimeType = dbFile.MimeType
	}
	if strings.HasPrefix(meta.MimeType, "image/") {
		cfg, _, _ := image.DecodeConfig(bytes.NewReader(data))
		dbFile.Width = cfg.Width
		dbFile.Height = cfg.Height
	}

	uploadMime := meta.MimeType
	if encrypt {
		dbFile.Encrypted = true
		dbFile.DecryptionInfo = attachment.NewEncryptedFile()
		dbFile.DecryptionInfo.EncryptInPlace(data)
		uploadMime = "application/octet-stream"
	}
	req := mautrix.ReqUploadMedia{
		ContentBytes: data,
		ContentType:  uploadMime,
	}
	if br.Config.Homeserver.AsyncMedia {
		resp, err := intent.UnstableCreateMXC()
		if err != nil {
			return nil, err
		}
		dbFile.MXC = resp.ContentURI
		req.UnstableMXC = resp.ContentURI
		req.UploadURL = resp.UploadURL
		go func() {
			_, err = intent.UploadMedia(req)
			if err != nil {
				br.Log.Errorfln("Failed to upload %s: %v", req.UnstableMXC, err)
				dbFile.Delete()
			}
		}()
	} else {
		uploaded, err := intent.UploadMedia(req)
		if err != nil {
			return nil, err
		}
		dbFile.MXC = uploaded.ContentURI
	}
	return dbFile, nil
}

type AttachmentMeta struct {
	AttachmentID string
	MimeType     string
	EmojiName    string
}

func (br *DiscordBridge) copyAttachmentToMatrix(intent *appservice.IntentAPI, url string, encrypt bool, meta *AttachmentMeta) (*database.File, error) {
	dbFile := br.DB.File.Get(url, encrypt)
	if dbFile == nil {
		data, err := downloadDiscordAttachment(url)
		if err != nil {
			return nil, err
		}

		if meta == nil {
			meta = &AttachmentMeta{}
		}
		dbFile, err = br.uploadMatrixAttachment(intent, data, url, encrypt, *meta)
		if err != nil {
			return nil, err
		}
		// TODO add option to cache encrypted files too?
		if !dbFile.Encrypted {
			dbFile.Insert(nil)
		}
	}
	return dbFile, nil
}

func (portal *Portal) getEmojiMXCByDiscordID(emojiID, name string, animated bool) id.ContentURI {
	var url, mimeType string
	if animated {
		url = discordgo.EndpointEmojiAnimated(emojiID)
		mimeType = "image/gif"
	} else {
		url = discordgo.EndpointEmoji(emojiID)
		mimeType = "image/png"
	}
	dbFile, err := portal.bridge.copyAttachmentToMatrix(portal.MainIntent(), url, false, &AttachmentMeta{
		AttachmentID: emojiID,
		MimeType:     mimeType,
		EmojiName:    name,
	})
	if err != nil {
		portal.log.Warnfln("Failed to download emoji %s from discord: %v", emojiID, err)
		return id.ContentURI{}
	}
	return dbFile.MXC
}
