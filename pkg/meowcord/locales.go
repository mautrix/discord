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

// Locale represents the accepted languages for Discord.
// https://discord.com/developers/docs/reference#locales
type Locale string

// String returns the human-readable string of the locale
func (l Locale) String() string {
	if name, ok := Locales[l]; ok {
		return name
	}
	return Unknown.String()
}

// All defined locales in Discord
const (
	EnglishUS    Locale = "en-US"
	EnglishGB    Locale = "en-GB"
	Bulgarian    Locale = "bg"
	ChineseCN    Locale = "zh-CN"
	ChineseTW    Locale = "zh-TW"
	Croatian     Locale = "hr"
	Czech        Locale = "cs"
	Danish       Locale = "da"
	Dutch        Locale = "nl"
	Finnish      Locale = "fi"
	French       Locale = "fr"
	German       Locale = "de"
	Greek        Locale = "el"
	Hindi        Locale = "hi"
	Hungarian    Locale = "hu"
	Italian      Locale = "it"
	Japanese     Locale = "ja"
	Korean       Locale = "ko"
	Lithuanian   Locale = "lt"
	Norwegian    Locale = "no"
	Polish       Locale = "pl"
	PortugueseBR Locale = "pt-BR"
	Romanian     Locale = "ro"
	Russian      Locale = "ru"
	SpanishES    Locale = "es-ES"
	SpanishLATAM Locale = "es-419"
	Swedish      Locale = "sv-SE"
	Thai         Locale = "th"
	Turkish      Locale = "tr"
	Ukrainian    Locale = "uk"
	Vietnamese   Locale = "vi"
	Unknown      Locale = ""
)

// Locales is a map of all the languages codes to their names.
var Locales = map[Locale]string{
	EnglishUS:    "English (United States)",
	EnglishGB:    "English (Great Britain)",
	Bulgarian:    "Bulgarian",
	ChineseCN:    "Chinese (China)",
	ChineseTW:    "Chinese (Taiwan)",
	Croatian:     "Croatian",
	Czech:        "Czech",
	Danish:       "Danish",
	Dutch:        "Dutch",
	Finnish:      "Finnish",
	French:       "French",
	German:       "German",
	Greek:        "Greek",
	Hindi:        "Hindi",
	Hungarian:    "Hungarian",
	Italian:      "Italian",
	Japanese:     "Japanese",
	Korean:       "Korean",
	Lithuanian:   "Lithuanian",
	Norwegian:    "Norwegian",
	Polish:       "Polish",
	PortugueseBR: "Portuguese (Brazil)",
	Romanian:     "Romanian",
	Russian:      "Russian",
	SpanishES:    "Spanish (Spain)",
	SpanishLATAM: "Spanish (LATAM)",
	Swedish:      "Swedish",
	Thai:         "Thai",
	Turkish:      "Turkish",
	Ukrainian:    "Ukrainian",
	Vietnamese:   "Vietnamese",
	Unknown:      "unknown",
}
