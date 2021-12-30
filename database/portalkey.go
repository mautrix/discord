package database

type PortalKey struct {
	ID       string
	Receiver string
}

func (key PortalKey) String() string {
	if key.Receiver == key.ID {
		return key.ID
	}
	return key.ID + "-" + key.Receiver
}
