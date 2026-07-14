// mautrix-discord - A Matrix-Discord puppeting bridge.
// Copyright 2015-2016 Bruce Marriner <bruce@sqls.net>.  All rights reserved.
// Copyright (C) 2026 The mautrix-discord contributors
//
// This file is derived from discordgo (https://github.com/bwmarrin/discordgo),
// used under the BSD-3-Clause license; see README.md in this directory. This
// file is distributed under the GNU AGPLv3 as follows:
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

package meowcord

import "testing"

func TestUser_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		u    *User
		want string
	}{
		{
			name: "User with a discriminator",
			u: &User{
				Username:      "bob",
				Discriminator: "8192",
			},
			want: "bob#8192",
		},
		{
			name: "User with discriminator set to 0",
			u: &User{
				Username:      "aldiwildan",
				Discriminator: "0",
			},
			want: "aldiwildan",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.u.String(); got != tc.want {
				t.Errorf("User.String() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestUser_DisplayName(t *testing.T) {
	t.Run("no global name set", func(t *testing.T) {
		u := &User{
			GlobalName: "",
			Username:   "username",
		}
		if dn := u.DisplayName(); dn != u.Username {
			t.Errorf("User.DisplayName() = %v, want %v", dn, u.Username)
		}
	})
	t.Run("global name set", func(t *testing.T) {
		u := &User{
			GlobalName: "global",
			Username:   "username",
		}
		if dn := u.DisplayName(); dn != u.GlobalName {
			t.Errorf("User.DisplayName() = %v, want %v", dn, u.GlobalName)
		}
	})
}
