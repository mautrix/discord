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

package meowcord_test

import (
	"log"
	"os"

	"go.mau.fi/mautrix-discord/pkg/meowcord"
)

func ExampleApplication() {

	// Authentication Token pulled from environment variable DGU_TOKEN
	Token := os.Getenv("DGU_TOKEN")
	if Token == "" {
		return
	}

	// Create a new Discordgo session
	dg, err := meowcord.New(Token)
	if err != nil {
		log.Println(err)
		return
	}

	// Create an new Application
	ap := &meowcord.Application{}
	ap.Name = "TestApp"
	ap.Description = "TestDesc"
	ap, err = dg.ApplicationCreate(ap)
	log.Printf("ApplicationCreate: err: %+v, app: %+v\n", err, ap)

	// Get a specific Application by it's ID
	ap, err = dg.Application(ap.ID)
	log.Printf("Application: err: %+v, app: %+v\n", err, ap)

	// Update an existing Application with new values
	ap.Description = "Whooooa"
	ap, err = dg.ApplicationUpdate(ap.ID, ap)
	log.Printf("ApplicationUpdate: err: %+v, app: %+v\n", err, ap)

	// create a new bot account for this application
	bot, err := dg.ApplicationBotCreate(ap.ID)
	log.Printf("BotCreate: err: %+v, bot: %+v\n", err, bot)

	// Get a list of all applications for the authenticated user
	apps, err := dg.Applications()
	log.Printf("Applications: err: %+v, apps : %+v\n", err, apps)
	for k, v := range apps {
		log.Printf("Applications: %d : %+v\n", k, v)
	}

	// Delete the application we created.
	err = dg.ApplicationDelete(ap.ID)
	log.Printf("Delete: err: %+v\n", err)
}
