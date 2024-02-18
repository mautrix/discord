// mautrix-discord - A Matrix-Discord puppeting bridge.
// Copyright (C) 2024 Tulir Asokan
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

package main

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const MediaIDPrefix = "\U0001F408DISCORD"
const MediaIDVersion = 1

type MediaIDClass uint8

const (
	MediaIDClassAttachment        MediaIDClass = 1
	MediaIDClassEmoji             MediaIDClass = 2
	MediaIDClassSticker           MediaIDClass = 3
	MediaIDClassUserAvatar        MediaIDClass = 4
	MediaIDClassGuildMemberAvatar MediaIDClass = 5
)

type MediaIDData interface {
	Write(to io.Writer)
	Read(from io.Reader) error
	Size() int
	Wrap() *MediaID
}

type MediaID struct {
	Version   uint8
	TypeClass MediaIDClass
	Data      MediaIDData
}

func ParseMediaID(id string) (*MediaID, error) {
	data, err := base64.RawURLEncoding.DecodeString(id)
	if err != nil {
		return nil, fmt.Errorf("failed to decode base64: %w", err)
	}
	mid := &MediaID{}
	err = mid.Read(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("failed to parse media ID: %w", err)
	}
	return mid, nil
}

func (mid *MediaID) String() string {
	buf := bytes.NewBuffer(make([]byte, 0, mid.Data.Size()))
	mid.Write(buf)
	return base64.RawURLEncoding.EncodeToString(buf.Bytes())
}

func (mid *MediaID) Write(to io.Writer) {
	_, _ = to.Write([]byte(MediaIDPrefix))
	_ = binary.Write(to, binary.BigEndian, mid.Version)
	_ = binary.Write(to, binary.BigEndian, mid.TypeClass)
	mid.Data.Write(to)
}

var (
	ErrInvalidMediaID     = errors.New("invalid media ID")
	ErrUnsupportedMediaID = errors.New("unsupported media ID")
)

func (mid *MediaID) Read(from io.Reader) error {
	prefix := make([]byte, len(MediaIDPrefix))
	_, err := io.ReadFull(from, prefix)
	if err != nil || !bytes.Equal(prefix, []byte(MediaIDPrefix)) {
		return fmt.Errorf("%w: prefix not found", ErrInvalidMediaID)
	}
	versionAndClass := make([]byte, 2)
	_, err = io.ReadFull(from, versionAndClass)
	if err != nil {
		return fmt.Errorf("%w: version and class not found", ErrInvalidMediaID)
	} else if versionAndClass[0] != MediaIDVersion {
		return fmt.Errorf("%w: unknown version %d", ErrUnsupportedMediaID, versionAndClass[0])
	}
	switch MediaIDClass(versionAndClass[1]) {
	case MediaIDClassAttachment:
		mid.Data = &AttachmentMediaData{}
	case MediaIDClassEmoji:
		mid.Data = &EmojiMediaData{}
	case MediaIDClassSticker:
		mid.Data = &StickerMediaData{}
	case MediaIDClassUserAvatar:
		mid.Data = &UserAvatarMediaData{}
	case MediaIDClassGuildMemberAvatar:
		mid.Data = &GuildMemberAvatarMediaData{}
	default:
		return fmt.Errorf("%w: unrecognized type class %d", ErrUnsupportedMediaID, versionAndClass[1])
	}
	err = mid.Data.Read(from)
	if err != nil {
		return fmt.Errorf("failed to parse media ID data: %w", err)
	}
	return nil
}

type AttachmentMediaDataInner struct {
	ChannelID    uint64
	MessageID    uint64
	AttachmentID uint64
}

func (amdi AttachmentMediaDataInner) CacheKey() AttachmentCacheKey {
	return AttachmentCacheKey{
		ChannelID:    amdi.ChannelID,
		AttachmentID: amdi.AttachmentID,
	}
}

type AttachmentMediaData struct {
	AttachmentMediaDataInner
	FileName string
}

func (amd *AttachmentMediaData) Write(to io.Writer) {
	_ = binary.Write(to, binary.BigEndian, &amd.AttachmentMediaDataInner)
	_, _ = to.Write([]byte(amd.FileName))
}

func (amd *AttachmentMediaData) Read(from io.Reader) (err error) {
	err = binary.Read(from, binary.BigEndian, &amd.AttachmentMediaDataInner)
	if err != nil {
		return
	}
	name, err := io.ReadAll(from)
	if err != nil {
		return
	}
	amd.FileName = string(name)
	return
}

func (amd *AttachmentMediaData) Size() int {
	return binary.Size(amd.AttachmentMediaDataInner) + len(amd.FileName)
}

func (amd *AttachmentMediaData) Wrap() *MediaID {
	return &MediaID{
		Version:   MediaIDVersion,
		TypeClass: MediaIDClassAttachment,
		Data:      amd,
	}
}

type StickerMediaData struct {
	StickerID uint64
	Format    uint8
}

func (smd *StickerMediaData) Write(to io.Writer) {
	_ = binary.Write(to, binary.BigEndian, smd)
}

func (smd *StickerMediaData) Read(from io.Reader) error {
	return binary.Read(from, binary.BigEndian, smd)
}

func (smd *StickerMediaData) Size() int {
	return binary.Size(smd)
}

func (smd *StickerMediaData) Wrap() *MediaID {
	return &MediaID{
		Version:   MediaIDVersion,
		TypeClass: MediaIDClassSticker,
		Data:      smd,
	}
}

type EmojiMediaDataInner struct {
	EmojiID  uint64
	Animated bool
}

type EmojiMediaData struct {
	EmojiMediaDataInner
	Name string
}

func (emd *EmojiMediaData) Write(to io.Writer) {
	_ = binary.Write(to, binary.BigEndian, &emd.EmojiMediaDataInner)
	_, _ = to.Write([]byte(emd.Name))
}

func (emd *EmojiMediaData) Read(from io.Reader) (err error) {
	err = binary.Read(from, binary.BigEndian, &emd.EmojiMediaDataInner)
	if err != nil {
		return
	}
	name, err := io.ReadAll(from)
	if err != nil {
		return
	}
	emd.Name = string(name)
	return
}

func (emd *EmojiMediaData) Size() int {
	return binary.Size(emd.EmojiMediaDataInner) + len(emd.Name)
}

func (emd *EmojiMediaData) Wrap() *MediaID {
	return &MediaID{
		Version:   MediaIDVersion,
		TypeClass: MediaIDClassEmoji,
		Data:      emd,
	}
}

type UserAvatarMediaData struct {
	UserID   uint64
	AvatarID uint64
	Animated bool
}

func (uamd *UserAvatarMediaData) Write(to io.Writer) {
	_ = binary.Write(to, binary.BigEndian, uamd)
}

func (uamd *UserAvatarMediaData) Read(from io.Reader) error {
	return binary.Read(from, binary.BigEndian, uamd)
}

func (uamd *UserAvatarMediaData) Size() int {
	return binary.Size(uamd)
}

func (uamd *UserAvatarMediaData) Wrap() *MediaID {
	return &MediaID{
		Version:   MediaIDVersion,
		TypeClass: MediaIDClassUserAvatar,
		Data:      uamd,
	}
}

type GuildMemberAvatarMediaData struct {
	GuildID  uint64
	UserID   uint64
	AvatarID uint64
	Animated bool
}

func (guamd *GuildMemberAvatarMediaData) Write(to io.Writer) {
	_ = binary.Write(to, binary.BigEndian, guamd)
}

func (guamd *GuildMemberAvatarMediaData) Read(from io.Reader) error {
	return binary.Read(from, binary.BigEndian, guamd)
}

func (guamd *GuildMemberAvatarMediaData) Size() int {
	return binary.Size(guamd)
}

func (guamd *GuildMemberAvatarMediaData) Wrap() *MediaID {
	return &MediaID{
		Version:   MediaIDVersion,
		TypeClass: MediaIDClassGuildMemberAvatar,
		Data:      guamd,
	}
}
