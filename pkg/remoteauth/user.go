package remoteauth

import (
	"fmt"
	"strings"
)

type User struct {
	UserID        string
	Discriminator string
	AvatarHash    string
	Username      string

	Token string
}

func (u *User) update(payload string) error {
	parts := strings.Split(payload, ":")
	if len(parts) != 4 {
		return fmt.Errorf("expected 4 parts but got %d", len(parts))
	}

	u.UserID = parts[0]
	u.Discriminator = parts[1]
	u.AvatarHash = parts[2]
	u.Username = parts[3]

	return nil
}
