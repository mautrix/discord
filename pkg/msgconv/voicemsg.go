// mautrix-discord - A Matrix-Discord puppeting bridge.
// Copyright (C) 2026 Tulir Asokan
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package msgconv

import (
	"mime"
	"strings"

	"maunium.net/go/mautrix/event"
)

// voiceAttachmentExtension returns a filename extension (including the leading
// dot, à la filepath.Ext) appropriate for the given audio content type.
func voiceAttachmentExtension(contentType string) string {
	// (ParseMediaType strips any codec parameters
	// (e.g. "audio/ogg; codecs=opus") and lowercases the media type.)
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		mediaType = strings.ToLower(strings.TrimSpace(contentType))
	}

	switch mediaType {
	case "audio/mp4", "audio/x-m4a", "audio/m4a", "audio/aac":
		return ".m4a"
	case "audio/mpeg", "audio/mp3":
		return ".mp3"
	case "audio/wav", "audio/x-wav", "audio/wave":
		return ".wav"
	case "audio/webm":
		return ".webm"
	case "audio/flac", "audio/x-flac":
		return ".flac"
	default:
		// Presume Ogg/Opus, since Discord voice messages are canonically of
		// that codec and container.
		return ".ogg"
	}
}

type discordVoiceMetadata struct {
	ContentType     string
	DurationSeconds float64
	Waveform        []byte
}

// NOTE these waveform calculations are pure conjecture; i.e. they aren't
// modeled after what first-party clients actually do

func waveformBuckets(durationMs int) int {
	targetLength := (durationMs + 99) / 100       // like math.Ceil(durationMs / 100)
	targetLength = max(min(targetLength, 256), 1) // clamp to [1,256]
	return targetLength
}

func downsampleWaveform(waveform []int, buckets int) []int {
	if len(waveform) <= buckets {
		return waveform
	}

	samples := make([]int, 0, buckets)
	for i := range buckets {
		start := i * len(waveform) / buckets
		end := (i + 1) * len(waveform) / buckets
		if end <= start {
			end = start + 1
		}

		maxVal := waveform[start]
		for _, sample := range waveform[start+1 : end] {
			if sample > maxVal {
				maxVal = sample
			}
		}
		samples = append(samples, maxVal)
	}

	return samples
}

func matrixWaveformToDiscord(samples []int, durationMs int) []byte {
	if len(samples) == 0 {
		return nil
	}

	samples = downsampleWaveform(samples, waveformBuckets(durationMs))

	maxVal := 0
	for _, sample := range samples {
		if sample > maxVal {
			maxVal = sample
		}
	}

	clampedSamples := make([]byte, len(samples))
	for i, sample := range samples {
		if maxVal > 256 {
			sample /= 4
		}
		if sample < 0 {
			sample = 0
		}
		if sample > 255 {
			sample = 255
		}
		clampedSamples[i] = byte(sample)
	}

	return clampedSamples
}

func getDiscordVoiceMetadata(content *event.MessageEventContent) *discordVoiceMetadata {
	if content.MSC3245Voice == nil || content.MSC1767Audio == nil {
		return nil
	}

	mimeType := strings.TrimSpace(content.Info.MimeType)
	if !strings.HasPrefix(strings.ToLower(mimeType), "audio/") {
		return nil
	}

	durationMs := content.Info.Duration
	if durationMs == 0 {
		durationMs = content.MSC1767Audio.Duration
	}
	if durationMs <= 0 {
		return nil
	}

	waveform := matrixWaveformToDiscord(content.MSC1767Audio.Waveform, durationMs)
	if len(waveform) == 0 {
		return nil
	}

	return &discordVoiceMetadata{
		ContentType:     mimeType,
		DurationSeconds: float64(durationMs) / 1000,
		Waveform:        waveform,
	}
}
