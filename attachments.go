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

func (br *DiscordBridge) uploadMatrixAttachment(intent *appservice.IntentAPI, data []byte, url string, encrypt bool, attachmentID, mime string) (*database.File, error) {
	dbFile := br.DB.File.New()
	dbFile.Timestamp = time.Now()
	dbFile.URL = url
	dbFile.ID = attachmentID
	dbFile.Size = len(data)
	dbFile.MimeType = mimetype.Detect(data).String()
	if mime == "" {
		mime = dbFile.MimeType
	}
	if strings.HasPrefix(mime, "image/") {
		cfg, _, _ := image.DecodeConfig(bytes.NewReader(data))
		dbFile.Width = cfg.Width
		dbFile.Height = cfg.Height
	}

	uploadMime := mime
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
	dbFile.Insert(nil)
	return dbFile, nil
}

func (br *DiscordBridge) copyAttachmentToMatrix(intent *appservice.IntentAPI, url string, encrypt bool, attachmentID, mime string) (*database.File, error) {
	dbFile := br.DB.File.Get(url, encrypt)
	if dbFile == nil {
		data, err := downloadDiscordAttachment(url)
		if err != nil {
			return nil, err
		}

		dbFile, err = br.uploadMatrixAttachment(intent, data, url, encrypt, attachmentID, mime)
		if err != nil {
			return nil, err
		}
	}
	return dbFile, nil
}
