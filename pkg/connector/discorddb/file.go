package discorddb

import (
	"context"
	"encoding/json"

	"go.mau.fi/util/dbutil"
)

// File is a cached Discord media upload. Keyed by (URL, encrypted) so that the
// same CDN attachment can be stored both as plaintext and as an E2EE upload.
// decryption_info holds the EncryptedFile JSON required to decrypt E2EE media (M9).
type File struct {
	URL       string
	Encrypted bool
	MXC       string

	// ID is the attachment/emoji snowflake, used for direct-media routing.
	ID        *string
	EmojiName *string

	Size     int64
	Width    *int
	Height   *int
	MimeType string

	// DecryptionInfo is the raw JSON of mautrix-go's event.EncryptedFileInfo.
	// It must be present whenever Encrypted==true.
	DecryptionInfo *json.RawMessage
	Timestamp      int64
}

func (f *File) Scan(row dbutil.Scannable) (*File, error) {
	err := row.Scan(
		&f.URL, &f.Encrypted, &f.MXC,
		&f.ID, &f.EmojiName,
		&f.Size, &f.Width, &f.Height, &f.MimeType,
		&f.DecryptionInfo, &f.Timestamp,
	)
	if err != nil {
		return nil, err
	}
	return f, nil
}

func (f *File) sqlVariables() []any {
	return []any{
		f.URL, f.Encrypted, f.MXC,
		f.ID, f.EmojiName,
		f.Size, f.Width, f.Height, f.MimeType,
		f.DecryptionInfo, f.Timestamp,
	}
}

// FileQuery provides CRUD operations on the dc_file table.
type FileQuery struct {
	*dbutil.QueryHelper[*File]
}

const (
	getFileBaseQuery = `
		SELECT url, encrypted, mxc, id, emoji_name, size, width, height, mime_type, decryption_info, timestamp
		FROM dc_file
	`
	getFileByURLQuery = getFileBaseQuery + `WHERE url=$1 AND encrypted=$2`
	getFileByMXCQuery = getFileBaseQuery + `WHERE mxc=$1 LIMIT 1`

	upsertFileQuery = `
		INSERT INTO dc_file (url, encrypted, mxc, id, emoji_name, size, width, height, mime_type, decryption_info, timestamp)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (url, encrypted) DO UPDATE
		    SET mxc=$3, id=$4, emoji_name=$5, size=$6, width=$7, height=$8, mime_type=$9,
		        decryption_info=$10, timestamp=$11
	`
	deleteFileQuery = `DELETE FROM dc_file WHERE url=$1 AND encrypted=$2`
)

func (fq *FileQuery) Get(ctx context.Context, url string, encrypted bool) (*File, error) {
	return fq.QueryOne(ctx, getFileByURLQuery, url, encrypted)
}

func (fq *FileQuery) GetByMXC(ctx context.Context, mxc string) (*File, error) {
	return fq.QueryOne(ctx, getFileByMXCQuery, mxc)
}

func (fq *FileQuery) Upsert(ctx context.Context, file *File) error {
	return fq.Exec(ctx, upsertFileQuery, file.sqlVariables()...)
}

func (fq *FileQuery) Delete(ctx context.Context, url string, encrypted bool) error {
	return fq.Exec(ctx, deleteFileQuery, url, encrypted)
}
