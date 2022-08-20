// mautrix-discord - A Matrix-Discord puppeting bridge.
// Copyright (C) 2022 Tulir Asokan
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

package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEscapeDiscordMarkdown(t *testing.T) {
	type escapeTest struct {
		name     string
		input    string
		expected string
	}

	tests := []escapeTest{
		{"Simple text", "Lorem ipsum dolor sit amet, consectetuer adipiscing elit.", "Lorem ipsum dolor sit amet, consectetuer adipiscing elit."},
		{"Backslash", `foo\bar`, `foo\\bar`},
		{"Underscore", `foo_bar`, `foo\_bar`},
		{"Asterisk", `foo*bar`, `foo\*bar`},
		{"Tilde", `foo~bar`, `foo\~bar`},
		{"Backtick", "foo`bar", "foo\\`bar"},
		{"Forward tick", `foo´bar`, `foo´bar`},
		{"Pipe", `foo|bar`, `foo\|bar`},
		{"Less than", `foo<bar`, `foo\<bar`},
		{"Greater than", `foo>bar`, `foo>bar`},
		{"Multiple things", `\_*~|`, `\\\_\*\~\|`},
		{"URL", `https://example.com/foo_bar`, `https://example.com/foo_bar`},
		{"Multiple URLs", `hello_world https://example.com/foo_bar *testing* https://a_b_c/*def*`, `hello\_world https://example.com/foo_bar \*testing\* https://a_b_c/*def*`},
		{"URL ends with no-break zero-width space", "https://example.com\ufefffoo_bar", "https://example.com\ufefffoo\\_bar"},
		{"URL ends with less than", `https://example.com<foo_bar`, `https://example.com<foo\_bar`},
		{"Short URL", `https://_`, `https://_`},
		{"Insecure URL", `http://example.com/foo_bar`, `http://example.com/foo_bar`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.expected, escapeDiscordMarkdown(test.input))
		})
	}
}
