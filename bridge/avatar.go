package bridge

import (
	"fmt"
	"io"
	"net/http"

	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/id"
)

func uploadAvatar(intent *appservice.IntentAPI, url string) (id.ContentURI, error) {
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
