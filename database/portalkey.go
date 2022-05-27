package database

type PortalKey struct {
	ChannelID string
	Receiver  string
}

func NewPortalKey(channelID, receiver string) PortalKey {
	return PortalKey{
		ChannelID: channelID,
		Receiver:  receiver,
	}
}

func (key PortalKey) String() string {
	if key.Receiver == "" {
		return key.ChannelID
	}
	return key.ChannelID + "-" + key.Receiver
}
