package database

type PortalKey struct {
	ID        string
	ChannelID string
}

func NewPortalKey(id, channelID string) PortalKey {
	return PortalKey{
		ID:        id,
		ChannelID: channelID,
	}
}

func (key PortalKey) String() string {
	if key.ChannelID == key.ID {
		return key.ID
	}
	return key.ID + "-" + key.ChannelID
}
