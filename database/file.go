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

package database

import (
	"context"
	"database/sql"
	"time"

	"go.mau.fi/util/dbutil"
	"maunium.net/go/mautrix/crypto/attachment"
	"maunium.net/go/mautrix/id"
)

type FileQuery struct {
	*dbutil.QueryHelper[*File]
}

// language=postgresql
const (
	getFileByURLQuery = `
		SELECT url, encrypted, mxc, id, emoji_name, size, width, height, mime_type, decryption_info, timestamp
		FROM discord_file WHERE url=$1 AND encrypted=$2
	`
	getFileByEmojiMXCQuery = `
		SELECT url, encrypted, mxc, id, emoji_name, size, width, height, mime_type, decryption_info, timestamp
		FROM discord_file WHERE mxc=$1 AND emoji_name<>'' LIMIT 1
	`
	insertFileQuery = `
		INSERT INTO discord_file (url, encrypted, mxc, id, emoji_name, size, width, height, mime_type, decryption_info, timestamp)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	`
	deleteFileQuery = "DELETE FROM discord_file WHERE url=$1 AND encrypted=$2"
)

func newFile(qh *dbutil.QueryHelper[*File]) *File {
	return &File{qh: qh}
}

func (fq *FileQuery) Get(ctx context.Context, url string, encrypted bool) (*File, error) {
	return fq.QueryOne(ctx, getFileByURLQuery, url, encrypted)
}

func (fq *FileQuery) GetEmojiByMXC(ctx context.Context, mxc id.ContentURI) (*File, error) {
	return fq.QueryOne(ctx, getFileByEmojiMXCQuery, mxc.String())
}

type File struct {
	qh *dbutil.QueryHelper[*File]

	URL       string
	Encrypted bool
	MXC       id.ContentURI

	ID        string
	EmojiName string

	Size     int
	Width    int
	Height   int
	MimeType string

	DecryptionInfo *attachment.EncryptedFile
	Timestamp      time.Time
}

func (f *File) Scan(row dbutil.Scannable) (*File, error) {
	var fileID, emojiName sql.NullString
	var width, height sql.NullInt32
	var timestamp int64
	err := row.Scan(
		&f.URL, &f.Encrypted, &f.MXC, &fileID, &emojiName, &f.Size,
		&width, &height, &f.MimeType,
		dbutil.JSON{Data: &f.DecryptionInfo}, &timestamp,
	)
	if err != nil {
		return nil, err
	}
	f.ID = fileID.String
	f.EmojiName = emojiName.String
	f.Timestamp = time.UnixMilli(timestamp).UTC()
	f.Width = int(width.Int32)
	f.Height = int(height.Int32)
	return f, nil
}

func (f *File) sqlVariables() []any {
	return []any{
		f.URL, f.Encrypted, f.MXC.String(), dbutil.StrPtr(f.ID), dbutil.StrPtr(f.EmojiName), f.Size,
		dbutil.NumPtr(f.Width), dbutil.NumPtr(f.Height), f.MimeType,
		dbutil.JSONPtr(f.DecryptionInfo), f.Timestamp.UnixMilli(),
	}
}

func (f *File) Insert(ctx context.Context) error {
	return f.qh.Exec(ctx, insertFileQuery, f.sqlVariables()...)
}

func (f *File) Delete(ctx context.Context) error {
	return f.qh.Exec(ctx, deleteFileQuery, f.URL, f.Encrypted)
}
