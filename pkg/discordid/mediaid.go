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

package discordid

import (
	"encoding/binary"
	"fmt"
	"strconv"

	"github.com/bwmarrin/discordgo"
	"maunium.net/go/mautrix/bridgev2/networkid"
)

type DirectMediaType byte

const (
	DirectMediaTypeV1     DirectMediaType = 1
	DirectMediaTypeAvatar DirectMediaType = 2

	encodedSnowflakeSize = 8
	encodedMediaIDV1Size = 1 + 4*encodedSnowflakeSize
)

func (dmt DirectMediaType) isSupported() bool {
	switch dmt {
	case DirectMediaTypeV1:
		return true
	}
	return false
}

type MediaInfoV1 struct {
	UserLoginID  networkid.UserLoginID
	ChannelID    string
	MessageID    string
	AttachmentID string
}

type MediaInfo struct {
	Type DirectMediaType
	MediaInfoV1
}

func NewMediaInfoV1(userLoginID networkid.UserLoginID, channelID, messageID, attachmentID string) MediaInfo {
	return MediaInfo{
		Type: DirectMediaTypeV1,
		MediaInfoV1: MediaInfoV1{
			UserLoginID:  userLoginID,
			ChannelID:    channelID,
			MessageID:    messageID,
			AttachmentID: attachmentID,
		},
	}
}

func (mi *MediaInfo) Encode() ([]byte, error) {
	buf := make([]byte, 1, encodedMediaIDV1Size)
	buf[0] = byte(mi.Type)

	appendSnowflake := func(what, snowflakeStr string) error {
		snowflake, err := strconv.ParseUint(snowflakeStr, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid %s: %w", what, err)
		}

		buf = binary.BigEndian.AppendUint64(buf, snowflake)
		return nil
	}

	if err := appendSnowflake("user login id", ParseUserLoginID(mi.UserLoginID)); err != nil {
		return nil, err
	}
	if err := appendSnowflake("channel id", mi.ChannelID); err != nil {
		return nil, err
	}
	if err := appendSnowflake("message id", mi.MessageID); err != nil {
		return nil, err
	}
	if err := appendSnowflake("attachment id", mi.AttachmentID); err != nil {
		return nil, err
	}

	return buf, nil
}

func ParseMediaID(mediaID networkid.MediaID) (*MediaInfo, error) {
	var info MediaInfo

	ptr := 0
	read := func(size int, what string) ([]byte, error) {
		if len(mediaID) < ptr+size {
			return nil, fmt.Errorf("media ID too short (%d bytes) to read %d byte %s starting at byte %d", len(mediaID), size, what, ptr)
		}
		b := mediaID[ptr : ptr+size]
		ptr += size
		return b, nil
	}
	readOne := func(what string) (byte, error) {
		b, err := read(1, what)
		if err != nil {
			return 0, err
		}
		return b[0], nil
	}
	readSnowflake := func(what string) (string, error) {
		snowflakeBytes, err := read(encodedSnowflakeSize, what)
		if err != nil {
			return "", err
		}

		snowflake := binary.BigEndian.Uint64(snowflakeBytes)
		return strconv.FormatUint(snowflake, 10), nil
	}

	mediaType, err := readOne("media type")
	if err != nil {
		return nil, err
	}
	info.Type = DirectMediaType(mediaType)

	if !info.Type.isSupported() {
		return nil, fmt.Errorf("unrecognized media type %d", info.Type)
	}

	userLoginID, err := readSnowflake("user login id")
	info.UserLoginID = networkid.UserLoginID(userLoginID)
	if err != nil {
		return nil, err
	}

	channelID, err := readSnowflake("channel id")
	info.ChannelID = channelID
	if err != nil {
		return nil, err
	}

	messageID, err := readSnowflake("message id")
	info.MessageID = messageID
	if err != nil {
		return nil, err
	}

	attachmentID, err := readSnowflake("attachment id")
	info.AttachmentID = attachmentID
	if err != nil {
		return nil, err
	}

	if ptr != len(mediaID) {
		return nil, fmt.Errorf("media ID has %d trailing bytes", len(mediaID)-ptr)
	}

	return &info, nil
}

type AvatarInfo struct {
	GuildID string
	UserID  string
	Avatar  string
}

func (ai *AvatarInfo) URL() string {
	user := &discordgo.User{ID: ai.UserID, Avatar: ai.Avatar}
	if ai.GuildID != "" {
		member := &discordgo.Member{GuildID: ai.GuildID, User: user, Avatar: ai.Avatar}
		return member.AvatarURL("256")
	}
	return user.AvatarURL("256")
}

func (ai *AvatarInfo) Encode() (networkid.MediaID, error) {
	if len(ai.Avatar) == 0 || len(ai.Avatar) > 255 {
		return nil, fmt.Errorf("invalid avatar hash length %d", len(ai.Avatar))
	}
	var guildID uint64
	if ai.GuildID != "" {
		var err error
		guildID, err = strconv.ParseUint(ai.GuildID, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid guild id: %w", err)
		}
	}
	userID, err := strconv.ParseUint(ai.UserID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid user id: %w", err)
	}
	buf := make([]byte, 1, 1+2*encodedSnowflakeSize+1+len(ai.Avatar))
	buf[0] = byte(DirectMediaTypeAvatar)
	buf = binary.BigEndian.AppendUint64(buf, guildID)
	buf = binary.BigEndian.AppendUint64(buf, userID)
	buf = append(buf, byte(len(ai.Avatar)))
	buf = append(buf, ai.Avatar...)
	return buf, nil
}

func ParseAvatarMediaID(mediaID networkid.MediaID) (*AvatarInfo, error) {
	const headerSize = 1 + 2*encodedSnowflakeSize + 1
	if len(mediaID) < headerSize || DirectMediaType(mediaID[0]) != DirectMediaTypeAvatar {
		return nil, fmt.Errorf("not an avatar media ID")
	}
	avatarLen := int(mediaID[headerSize-1])
	if len(mediaID) != headerSize+avatarLen {
		return nil, fmt.Errorf("avatar media ID has wrong length %d", len(mediaID))
	}
	info := &AvatarInfo{
		UserID: strconv.FormatUint(binary.BigEndian.Uint64(mediaID[1+encodedSnowflakeSize:]), 10),
		Avatar: string(mediaID[headerSize:]),
	}
	if guildID := binary.BigEndian.Uint64(mediaID[1:]); guildID != 0 {
		info.GuildID = strconv.FormatUint(guildID, 10)
	}
	return info, nil
}
