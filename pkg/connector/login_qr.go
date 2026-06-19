// QRLoginProcess implements bridgev2.LoginProcessDisplayAndWait for the "qr"
// login flow using Discord's remote-auth protocol (FR-2).
package connector

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/bwmarrin/discordgo"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"go.mau.fi/mautrix-discord/remoteauth"
)

// QRLoginProcess handles logging in by scanning a QR code with the Discord
// mobile app via the remote-auth WebSocket protocol.
type QRLoginProcess struct {
	Main *DiscordConnector
	User *bridgev2.User

	client   *remoteauth.Client
	qrChan   chan string
	doneChan chan struct{}
}

// Compile-time assertions.
var _ bridgev2.LoginProcess = (*QRLoginProcess)(nil)
var _ bridgev2.LoginProcessDisplayAndWait = (*QRLoginProcess)(nil)

// Start generates an RSA keypair, connects to Discord's remote-auth WebSocket,
// and returns a DisplayAndWait step with the initial QR code URL.
func (p *QRLoginProcess) Start(ctx context.Context) (*bridgev2.LoginStep, error) {
	client, err := remoteauth.New()
	if err != nil {
		return nil, fmt.Errorf("failed to generate RSA keypair: %w", err)
	}
	p.client = client

	// qrChan receives exactly one value (the fingerprint URL) then is closed
	// by the remoteauth library after pending_remote_init.
	// doneChan is closed when the WS session ends (success or error).
	p.qrChan = make(chan string)
	p.doneChan = make(chan struct{})

	if err = client.Dial(ctx, p.qrChan, p.doneChan); err != nil {
		close(p.qrChan)
		close(p.doneChan)
		return nil, fmt.Errorf("failed to connect to Discord remote-auth gateway: %w", err)
	}

	// Block until the server sends pending_remote_init with the fingerprint URL.
	// The library closes qrChan after delivering the single value.
	select {
	case qrURL, ok := <-p.qrChan:
		if !ok || qrURL == "" {
			_, resErr := client.Result()
			if resErr != nil {
				return nil, fmt.Errorf("remote-auth gateway closed before sending QR: %w", resErr)
			}
			return nil, errors.New("remote-auth gateway closed before sending QR")
		}
		return &bridgev2.LoginStep{
			Type:         bridgev2.LoginStepTypeDisplayAndWait,
			StepID:       "fi.mau.discord.qr",
			Instructions: "Scan the QR code with your Discord mobile app (User Settings → Scan QR Code).",
			DisplayAndWaitParams: &bridgev2.LoginDisplayAndWaitParams{
				Type: bridgev2.LoginDisplayTypeQR,
				Data: qrURL,
			},
		}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Wait blocks until the user scans the QR code and the remote-auth flow
// completes. On success it creates a UserLogin and returns a Complete step.
// On CAPTCHA it returns an actionable error directing the user to token login.
func (p *QRLoginProcess) Wait(ctx context.Context) (*bridgev2.LoginStep, error) {
	// Block until the remoteauth WS goroutine closes doneChan (success or error).
	select {
	case <-p.doneChan:
	case <-ctx.Done():
		p.cancelClient()
		return nil, ctx.Err()
	}

	discordUser, err := p.client.Result()
	if err != nil {
		// Surface CAPTCHA errors with a helpful message.
		var restErr *discordgo.RESTError
		if errors.As(err, &restErr) &&
			restErr.Response != nil &&
			restErr.Response.StatusCode == http.StatusBadRequest &&
			bytes.Contains(restErr.ResponseBody, []byte("captcha-required")) {
			return nil, errors.New("CAPTCHA required — CAPTCHAs are not supported in the QR flow; use token login instead")
		}
		return nil, fmt.Errorf("remote-auth login failed: %w", err)
	}
	if discordUser.Token == "" {
		return nil, errors.New("remote-auth flow completed without a token")
	}

	// The login ID is the Discord user snowflake so that the duplicate-login
	// takeover path in NewLogin (DeleteOnConflict=true) handles FR-68.
	ul, err := p.User.NewLogin(ctx, &database.UserLogin{
		ID:         networkid.UserLoginID(discordUser.UserID),
		RemoteName: discordUser.Username,
		Metadata: &UserLoginMeta{
			Token:     discordUser.Token,
			TokenType: TokenTypeUser,
		},
	}, &bridgev2.NewLoginParams{
		DeleteOnConflict: true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create user login: %w", err)
	}

	return &bridgev2.LoginStep{
		Type:   bridgev2.LoginStepTypeComplete,
		StepID: "fi.mau.discord.complete",
		CompleteParams: &bridgev2.LoginCompleteParams{
			UserLoginID: ul.ID,
			UserLogin:   ul,
		},
	}, nil
}

// Cancel disconnects the remote-auth WebSocket and releases resources.
// No other methods will be called after Cancel.
func (p *QRLoginProcess) Cancel() {
	p.cancelClient()
}

// cancelClient drains qrChan if Start hasn't consumed it yet and signals
// the WS goroutine to stop. It is safe to call multiple times.
func (p *QRLoginProcess) cancelClient() {
	if p.client == nil {
		return
	}
	// Drain qrChan so the library's goroutine isn't stuck sending.
	select {
	case <-p.qrChan:
	default:
	}
	// Close doneChan only if not already closed; the library closes it on
	// normal completion, so we guard with a non-blocking select.
	select {
	case <-p.doneChan:
		// already closed
	default:
		close(p.doneChan)
	}
}
