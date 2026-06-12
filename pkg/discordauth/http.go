// mautrix-discord - A Matrix-Discord puppeting bridge.
// Copyright (C) 2026 Tulir Asokan
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

package discordauth

import (
	"context"
	"fmt"
	"io"
	"net/http"
)

type HTTP interface {
	Do(req *http.Request) (*http.Response, error)
}

func respIsOk(resp *http.Response) bool {
	if resp == nil {
		return false
	}

	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

type HTTPError struct {
	body []byte
	resp *http.Response
}

func (err HTTPError) Error() string {
	if err.body != nil && len(err.body) < 1_024*16 { // arbitrarily cap at 16 KiB
		return fmt.Sprintf("Discord replied with HTTP %d: %s", err.resp.StatusCode, string(err.body))
	}

	return fmt.Sprintf("Discord replied with HTTP %d", err.resp.StatusCode)
}

func refreshReq(ctx context.Context, req *http.Request) (*http.Request, error) {
	var newBody io.ReadCloser
	var err error

	if req.Body != nil && req.ContentLength > 0 {
		newBody, err = req.GetBody()
		if err != nil {
			return nil, fmt.Errorf("failed to clone request body when retrying: %w", err)
		}
	}
	req = req.Clone(ctx)

	if newBody != nil {
		req.Body = newBody
	}

	return req, nil
}
