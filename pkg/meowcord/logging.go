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

// This file contains code related to discordgo package logging

package meowcord

import (
	"fmt"
	"log"
	"runtime"
	"strings"
)

const (

	// LogError level is used for critical errors that could lead to data loss
	// or panic that would not be returned to a calling function.
	LogError int = iota

	// LogWarning level is used for very abnormal events and errors that are
	// also returned to a calling function.
	LogWarning

	// LogInformational level is used for normal non-error activity
	LogInformational

	// LogDebug level is for very detailed non-error activity.  This is
	// very spammy and will impact performance.
	LogDebug
)

// Logger can be used to replace the standard logging for discordgo
var Logger func(msgL, caller int, format string, a ...interface{})

// msglog provides package wide logging consistency for discordgo
// the format, a...  portion this command follows that of fmt.Printf
//
//	msgL   : LogLevel of the message
//	caller : 1 + the number of callers away from the message source
//	format : Printf style message format
//	a ...  : comma separated list of values to pass
func msglog(msgL, caller int, format string, a ...interface{}) {

	if Logger != nil {
		Logger(msgL, caller, format, a...)
	} else {

		pc, file, line, _ := runtime.Caller(caller)

		files := strings.Split(file, "/")
		file = files[len(files)-1]

		name := runtime.FuncForPC(pc).Name()
		fns := strings.Split(name, ".")
		name = fns[len(fns)-1]

		msg := fmt.Sprintf(format, a...)

		log.Printf("[DG%d] %s:%d:%s() %s\n", msgL, file, line, name, msg)
	}
}

// helper function that wraps msglog for the Session struct
// This adds a check to insure the message is only logged
// if the session log level is equal or higher than the
// message log level
func (s *Session) log(msgL int, format string, a ...interface{}) {

	if msgL > s.LogLevel {
		return
	}

	if s.Logger != nil {
		s.Logger(msgL, 1, format, a...)
		return
	}

	msglog(msgL, 2, format, a...)
}

// helper function that wraps msglog for the VoiceConnection struct
// This adds a check to insure the message is only logged
// if the voice connection log level is equal or higher than the
// message log level
func (v *VoiceConnection) log(msgL int, format string, a ...interface{}) {

	if msgL > v.LogLevel {
		return
	}

	msglog(msgL, 2, format, a...)
}

// printJSON is a helper function to display JSON data in an easy to read format.
/* NOT USED ATM
func printJSON(body []byte) {
	var prettyJSON bytes.Buffer
	error := json.Indent(&prettyJSON, body, "", "\t")
	if error != nil {
		log.Print("JSON parse error: ", error)
	}
	log.Println(string(prettyJSON.Bytes()))
}
*/
