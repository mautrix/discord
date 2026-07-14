package connector

import (
	"encoding/binary"
	"encoding/hex"
	"testing"
	"time"
)

func TestParseAttachmentExpiryParam(t *testing.T) {
	want := time.Now().Add(24 * time.Hour).Truncate(time.Second)
	var encoded [4]byte
	binary.BigEndian.PutUint32(encoded[:], uint32(want.Unix()))

	got := parseAttachmentExpiryParam(hex.EncodeToString(encoded[:]))
	if !got.Equal(want) {
		t.Fatalf("unexpected parsed expiry: got %q, want %q", got, want)
	}
}
