package connector

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/bwmarrin/discordgo"
)

func TestShouldRetryHistoryFetch(t *testing.T) {
	restError := func(status int) error {
		return &discordgo.RESTError{Response: &http.Response{StatusCode: status}}
	}
	for _, test := range []struct {
		name string
		err  error
		want bool
	}{
		// The rate limit discordgo fails to parse and therefore never retries.
		{"unparseable rate limit body", &json.SyntaxError{}, true},
		{"server error", restError(http.StatusInternalServerError), true},
		{"too many requests", restError(http.StatusTooManyRequests), true},
		// A response that arrived and would not decode is deterministic.
		{"undecodable response", fmt.Errorf("%w: unknown component type: 16", discordgo.ErrJSONUnmarshal), false},
		{"forbidden", restError(http.StatusForbidden), false},
		{"cancelled", context.Canceled, false},
	} {
		if got := shouldRetryHistoryFetch(test.err); got != test.want {
			t.Errorf("%s: got %v, want %v", test.name, got, test.want)
		}
	}
}
