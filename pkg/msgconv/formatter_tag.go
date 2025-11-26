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

package msgconv

// TODO(skip): Port the rest of this.

type discordTimestampStyle rune

func (dts discordTimestampStyle) Format() string {
	switch dts {
	case 't':
		return "15:04 MST"
	case 'T':
		return "15:04:05 MST"
	case 'd':
		return "2006-01-02 MST"
	case 'D':
		return "2 January 2006 MST"
	case 'F':
		return "Monday, 2 January 2006 15:04 MST"
	case 'f':
		fallthrough
	default:
		return "2 January 2006 15:04 MST"
	}
}
