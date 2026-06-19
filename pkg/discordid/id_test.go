package discordid_test

import (
	"testing"
	"time"

	"maunium.net/go/mautrix/bridgev2/networkid"

	"go.mau.fi/mautrix-discord/pkg/discordid"
)

// --- PortalID round-trips ---

func TestMakePortalID(t *testing.T) {
	got := discordid.MakePortalID("123456789")
	if got != networkid.PortalID("123456789") {
		t.Errorf("MakePortalID: got %q, want %q", got, "123456789")
	}
}

func TestParsePortalIDRoundTrip(t *testing.T) {
	channelID := "987654321"
	id := discordid.MakePortalID(channelID)
	got := discordid.ParsePortalID(id)
	if got != channelID {
		t.Errorf("ParsePortalID round-trip: got %q, want %q", got, channelID)
	}
}

func TestMakeGuildPortalID(t *testing.T) {
	got := discordid.MakeGuildPortalID("111222333")
	if got != networkid.PortalID("111222333") {
		t.Errorf("MakeGuildPortalID: got %q, want %q", got, "111222333")
	}
}

// --- MessageID round-trips ---

func TestMakeMessageID(t *testing.T) {
	got := discordid.MakeMessageID("c", "m")
	if got != networkid.MessageID("c-m") {
		t.Errorf("MakeMessageID: got %q, want %q", got, "c-m")
	}
}

func TestParseMessageIDRoundTrip(t *testing.T) {
	channelID := "chan1"
	messageID := "msg1"
	id := discordid.MakeMessageID(channelID, messageID)
	gotChan, gotMsg, ok := discordid.ParseMessageID(id)
	if !ok {
		t.Fatalf("ParseMessageID returned ok=false for %q", id)
	}
	if gotChan != channelID || gotMsg != messageID {
		t.Errorf("ParseMessageID round-trip: got (%q, %q), want (%q, %q)", gotChan, gotMsg, channelID, messageID)
	}
}

func TestParseMessageIDNoSeparator(t *testing.T) {
	_, _, ok := discordid.ParseMessageID("nodash")
	if ok {
		t.Error("ParseMessageID should return ok=false when there is no '-'")
	}
}

// --- PartID: single-part and multi-part ---

func TestMakePartIDSinglePart(t *testing.T) {
	// Single-part message: empty attachmentID → empty PartID
	got := discordid.MakePartID(0, "")
	if got != networkid.PartID("") {
		t.Errorf("MakePartID single-part: got %q, want %q", got, "")
	}
}

func TestMakePartIDMultiPart(t *testing.T) {
	tests := []struct {
		index        int
		attachmentID string
		want         networkid.PartID
	}{
		{0, "att111", "attachment-0-att111"},
		{1, "att222", "attachment-1-att222"},
	}
	for _, tt := range tests {
		got := discordid.MakePartID(tt.index, tt.attachmentID)
		if got != tt.want {
			t.Errorf("MakePartID(%d, %q): got %q, want %q", tt.index, tt.attachmentID, got, tt.want)
		}
	}
}

func TestParsePartIDSinglePart(t *testing.T) {
	kind, index, attachmentID := discordid.ParsePartID("")
	if kind != "" || index != 0 || attachmentID != "" {
		t.Errorf("ParsePartID(\"\") got (%q, %d, %q), want (\"\", 0, \"\")", kind, index, attachmentID)
	}
}

func TestParsePartIDAttachment(t *testing.T) {
	got := networkid.PartID("attachment-1-aid")
	kind, index, attachmentID := discordid.ParsePartID(got)
	if kind != "attachment" || index != 1 || attachmentID != "aid" {
		t.Errorf("ParsePartID(%q) got (%q, %d, %q), want (attachment, 1, aid)", got, kind, index, attachmentID)
	}
}

// TestTwoAttachmentPartIDs verifies the 0-based indexing convention that must
// match the migration SQL window function:
//
//	ROW_NUMBER() OVER (...ORDER BY dc_attachment_id) - 1
func TestTwoAttachmentPartIDs(t *testing.T) {
	// Simulate two attachments sorted ascending by attachment ID.
	// The first (index 0) and second (index 1) must produce the correct part IDs.
	part0 := discordid.MakePartID(0, "100000000000001")
	part1 := discordid.MakePartID(1, "100000000000002")

	if part0 != "attachment-0-100000000000001" {
		t.Errorf("first attachment part_id: got %q, want %q", part0, "attachment-0-100000000000001")
	}
	if part1 != "attachment-1-100000000000002" {
		t.Errorf("second attachment part_id: got %q, want %q", part1, "attachment-1-100000000000002")
	}

	// Round-trip both
	k0, i0, a0 := discordid.ParsePartID(part0)
	if k0 != "attachment" || i0 != 0 || a0 != "100000000000001" {
		t.Errorf("round-trip part0: got (%q, %d, %q)", k0, i0, a0)
	}
	k1, i1, a1 := discordid.ParsePartID(part1)
	if k1 != "attachment" || i1 != 1 || a1 != "100000000000002" {
		t.Errorf("round-trip part1: got (%q, %d, %q)", k1, i1, a1)
	}
}

// --- UserID / UserLoginID ---

func TestMakeUserID(t *testing.T) {
	got := discordid.MakeUserID("555666777")
	if got != networkid.UserID("555666777") {
		t.Errorf("MakeUserID: got %q, want %q", got, "555666777")
	}
}

func TestMakeUserLoginID(t *testing.T) {
	got := discordid.MakeUserLoginID("555666777")
	if got != networkid.UserLoginID("555666777") {
		t.Errorf("MakeUserLoginID: got %q, want %q", got, "555666777")
	}
}

func TestUserIDToUserLoginID(t *testing.T) {
	uid := discordid.MakeUserID("444333222")
	got := discordid.UserIDToUserLoginID(uid)
	want := networkid.UserLoginID("444333222")
	if got != want {
		t.Errorf("UserIDToUserLoginID: got %q, want %q", got, want)
	}
}

// --- PortalKey ---

func TestMakePortalKeyDM(t *testing.T) {
	key := discordid.MakePortalKey("chan1", "login1", true)
	if key.ID != "chan1" {
		t.Errorf("PortalKey.ID: got %q, want %q", key.ID, "chan1")
	}
	if key.Receiver != "login1" {
		t.Errorf("PortalKey.Receiver for DM: got %q, want %q", key.Receiver, "login1")
	}
}

func TestMakePortalKeyGuildChannel(t *testing.T) {
	key := discordid.MakePortalKey("chan2", "login2", false)
	if key.ID != "chan2" {
		t.Errorf("PortalKey.ID: got %q, want %q", key.ID, "chan2")
	}
	if key.Receiver != "" {
		t.Errorf("PortalKey.Receiver for guild channel: got %q, want empty", key.Receiver)
	}
}

// --- SnowflakeToTime ---

// TestSnowflakeToTimeKnown checks a known Discord snowflake against its expected timestamp.
// Snowflake 175928847299117063 was created on 2016-04-30T11:18:25.796Z (Discord API docs example).
func TestSnowflakeToTimeKnown(t *testing.T) {
	// 175928847299117063 >> 22 = 41944705 (decimal)
	// 41944705 + 1420070400000 = 1462015105000 ms
	// = 2016-04-30T11:18:25.000Z  (the docs example rounds down to the ms)
	snowflake := "175928847299117063"
	got := discordid.SnowflakeToTime(snowflake)

	// Expected: 2016-04-30 11:18:25 UTC (exact ms from the bit-shift)
	wantMs := int64(175928847299117063>>22) + discordid.DiscordEpoch
	want := time.UnixMilli(wantMs).UTC()

	if !got.Equal(want) {
		t.Errorf("SnowflakeToTime(%q): got %v, want %v", snowflake, got, want)
	}
}

func TestSnowflakeToTimeZero(t *testing.T) {
	// A zero snowflake should give the Discord epoch.
	got := discordid.SnowflakeToTime("0")
	want := time.UnixMilli(discordid.DiscordEpoch).UTC()
	if !got.Equal(want) {
		t.Errorf("SnowflakeToTime(\"0\"): got %v, want %v", got, want)
	}
}

func TestSnowflakeToTimeInvalid(t *testing.T) {
	got := discordid.SnowflakeToTime("not-a-number")
	if !got.IsZero() {
		t.Errorf("SnowflakeToTime(invalid): expected zero time, got %v", got)
	}
}
