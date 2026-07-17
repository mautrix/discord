// mautrix-discord - A Matrix-Discord puppeting bridge.
// Copyright (C) 2026 The mautrix-discord contributors
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

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/google/uuid"
)

// TestLaunchSignatureValidUUID ensures that launch signatures double as valid
// version 4 UUIDs.
func TestLaunchSignatureValidUUID(t *testing.T) {
	sig, err := NewVanillaSignature()
	if err != nil {
		t.Error(err)
	}
	uuid.MustParse(sig.String())
}

func TestLaunchSignatureMarshal(t *testing.T) {
	sig, err := NewVanillaSignature()
	if err != nil {
		t.Error(err)
	}
	j, err := json.Marshal(sig)
	if err != nil {
		t.Error(err)
	}

	if string(j) != fmt.Sprintf("\"%s\"", sig.String()) {
		t.Errorf("launch signature didn't serialize as UUID; got %v", string(j))
	}
}
