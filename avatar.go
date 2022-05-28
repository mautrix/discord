package main

import (
	"fmt"
	"io"
	"net/http"

	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/id"

	"github.com/bwmarrin/discordgo"
)

func uploadAvatar(intent *appservice.IntentAPI, url string) (id.ContentURI, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return id.ContentURI{}, fmt.Errorf("failed to prepare request: %w", err)
	}
	for key, value := range discordgo.DroidImageHeaders {
		req.Header.Set(key, value)
	}
	getResp, err := http.DefaultClient.Do(req)
	if err != nil {
		return id.ContentURI{}, fmt.Errorf("failed to download avatar: %w", err)
	}

	data, err := io.ReadAll(getResp.Body)
	_ = getResp.Body.Close()
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
