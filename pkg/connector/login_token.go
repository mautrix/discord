// TokenLoginProcess implements the token-based Discord login flow (FR-1, FR-68).
// It satisfies bridgev2.LoginProcess and bridgev2.LoginProcessUserInput.
package connector

import (
	"context"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
)

const discordTokenEpoch = 1293840000

// decodeToken parses a Discord token and returns the Discord user ID encoded in
// its first segment. All three base64url segments must be present and decodable.
func decodeToken(token string) (int64, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return 0, fmt.Errorf("invalid number of parts in token")
	}
	userIDStr, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return 0, fmt.Errorf("invalid base64 in user ID part: %w", err)
	}
	if _, err = base64.RawURLEncoding.DecodeString(parts[1]); err != nil {
		return 0, fmt.Errorf("invalid base64 in random part: %w", err)
	}
	if _, err = base64.RawURLEncoding.DecodeString(parts[2]); err != nil {
		return 0, fmt.Errorf("invalid base64 in checksum part: %w", err)
	}
	userID, err := strconv.ParseInt(string(userIDStr), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid number in decoded user ID part: %w", err)
	}
	return userID, nil
}

// TokenLoginProcess handles the token login flow.
// CreateLogin constructs it as &TokenLoginProcess{Main: dc, User: user}.
type TokenLoginProcess struct {
	Main *DiscordConnector
	User *bridgev2.User
}

// Compile-time assertions.
var _ bridgev2.LoginProcess = (*TokenLoginProcess)(nil)
var _ bridgev2.LoginProcessUserInput = (*TokenLoginProcess)(nil)

// Start returns the first (and only) interactive step: a token input field plus
// an optional token-type selector.
func (p *TokenLoginProcess) Start(ctx context.Context) (*bridgev2.LoginStep, error) {
	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeUserInput,
		StepID:       "fi.mau.discord.token",
		Instructions: "Enter your Discord token and its type (user, bot, or oauth).",
		UserInputParams: &bridgev2.LoginUserInputParams{
			Fields: []bridgev2.LoginInputDataField{
				{
					Type: bridgev2.LoginInputFieldTypeToken,
					ID:   "token",
					Name: "Token",
				},
				{
					Type:         bridgev2.LoginInputFieldTypeSelect,
					ID:           "token_type",
					Name:         "Token type",
					Description:  "The kind of Discord credential: user, bot, or oauth.",
					DefaultValue: "user",
					Options:      []string{"user", "bot", "oauth"},
				},
			},
		},
	}, nil
}

// SubmitUserInput validates the submitted token, decodes the Discord user ID
// from it, and creates (or takes over) the UserLogin.
//
// FR-68: if a UserLogin with the same Discord user ID already exists for a
// different Matrix user it is logged out before the new login is created.
func (p *TokenLoginProcess) SubmitUserInput(ctx context.Context, input map[string]string) (*bridgev2.LoginStep, error) {
	rawToken := strings.TrimSpace(input["token"])
	if rawToken == "" {
		return nil, fmt.Errorf("token must not be empty")
	}

	// Strip any existing type prefix so decodeToken always sees the bare token.
	bareToken := rawToken
	for _, prefix := range []string{"Bot ", "Bearer "} {
		if strings.HasPrefix(rawToken, prefix) {
			bareToken = strings.TrimPrefix(rawToken, prefix)
			break
		}
	}

	userID, err := decodeToken(bareToken)
	if err != nil {
		return nil, fmt.Errorf("invalid token: %w", err)
	}

	// Determine the token type from the optional field; default to "user".
	tokenTypeStr := strings.ToLower(strings.TrimSpace(input["token_type"]))
	if tokenTypeStr == "" {
		tokenTypeStr = "user"
	}
	var tokenType TokenType
	var qualifiedToken string
	switch tokenTypeStr {
	case "user":
		tokenType = TokenTypeUser
		qualifiedToken = bareToken
	case "bot":
		tokenType = TokenTypeBot
		qualifiedToken = "Bot " + bareToken
	case "oauth":
		tokenType = TokenTypeOAuth
		qualifiedToken = "Bearer " + bareToken
	default:
		return nil, fmt.Errorf("token type must be user, bot, or oauth")
	}

	discordID := networkid.UserLoginID(strconv.FormatInt(userID, 10))

	// FR-68: take over any existing login for the same Discord user that belongs
	// to a different Matrix account. DeleteOnConflict handles this atomically
	// inside NewLogin.
	ul, err := p.User.NewLogin(ctx, &database.UserLogin{
		ID: discordID,
		Metadata: &UserLoginMeta{
			Token:     qualifiedToken,
			TokenType: tokenType,
		},
	}, &bridgev2.NewLoginParams{
		DeleteOnConflict: true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create login: %w", err)
	}

	return &bridgev2.LoginStep{
		Type:   bridgev2.LoginStepTypeComplete,
		StepID: "fi.mau.discord.token.complete",
		CompleteParams: &bridgev2.LoginCompleteParams{
			UserLoginID: ul.ID,
			UserLogin:   ul,
		},
	}, nil
}

// Cancel is a no-op for token login: there is no pending network connection or
// resource to release.
func (p *TokenLoginProcess) Cancel() {}
