package remoteauth

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gorilla/websocket"
)

type serverPacket interface {
	process(client *Client) error
}

func (c *Client) processMessages() {
	type rawPacket struct {
		OP string `json:"op"`
	}

	defer c.close()

	for {
		_, packet, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseNormalClosure) {
				c.err = err
			}

			return
		}

		raw := rawPacket{}
		if err := json.Unmarshal(packet, &raw); err != nil {
			c.err = err

			return
		}

		var dest interface{}
		switch raw.OP {
		case "hello":
			dest = new(serverHello)
		case "nonce_proof":
			dest = new(serverNonceProof)
		case "pending_remote_init":
			dest = new(serverPendingRemoteInit)
		case "pending_finish":
			dest = new(serverPendingFinish)
		case "finish":
			dest = new(serverFinish)
		case "cancel":
			dest = new(serverCancel)
		case "heartbeat_ack":
			dest = new(serverHeartbeatAck)
		}

		if err := json.Unmarshal(packet, dest); err != nil {
			c.err = err

			return
		}

		op := dest.(serverPacket)
		err = op.process(c)
		if err != nil {
			c.err = err

			return
		}
	}
}

///////////////////////////////////////////////////////////////////////////////
// Hello
///////////////////////////////////////////////////////////////////////////////
type serverHello struct {
	Timeout           int `json:"timeout_ms"`
	HeartbeatInterval int `json:"heartbeat_interval"`
}

func (h *serverHello) process(client *Client) error {
	// Create our heartbeat handler
	ticker := time.NewTicker(time.Duration(h.HeartbeatInterval) * time.Millisecond)
	go func() {
		defer ticker.Stop()
		for {
			select {
			// case <-client.ctx.Done():
			// 	return
			case <-ticker.C:
				h := clientHeartbeat{}
				if err := h.send(client); err != nil {
					client.err = err
					return
				}
			}
		}
	}()

	go func() {
		duration := time.Duration(h.Timeout) * time.Millisecond

		<-time.After(duration)

		client.err = fmt.Errorf("Timed out after %s", duration)
		client.close()
	}()

	i := clientInit{}

	return i.send(client)
}

///////////////////////////////////////////////////////////////////////////////
// NonceProof
///////////////////////////////////////////////////////////////////////////////
type serverNonceProof struct {
	EncryptedNonce string `json:"encrypted_nonce"`
}

func (n *serverNonceProof) process(client *Client) error {
	plaintext, err := client.decrypt(n.EncryptedNonce)
	if err != nil {
		return err
	}

	rawProof := sha256.Sum256(plaintext)
	// The [:] syntax is to return an unsized slice as the sum function returns
	// one.
	proof := base64.RawURLEncoding.EncodeToString(rawProof[:])

	c := clientNonceProof{Proof: proof}

	return c.send(client)
}

///////////////////////////////////////////////////////////////////////////////
// HeartbeatAck
///////////////////////////////////////////////////////////////////////////////
type serverHeartbeatAck struct{}

func (h *serverHeartbeatAck) process(client *Client) error {
	client.heartbeats -= 1

	return nil
}

///////////////////////////////////////////////////////////////////////////////
// PendingRemoteInit
///////////////////////////////////////////////////////////////////////////////
type serverPendingRemoteInit struct {
	Fingerprint string `json:"fingerprint"`
}

func (p *serverPendingRemoteInit) process(client *Client) error {
	url := "https://discordapp.com/ra/" + p.Fingerprint

	client.qrChan <- url
	close(client.qrChan)

	return nil
}

///////////////////////////////////////////////////////////////////////////////
// PendingFinish
///////////////////////////////////////////////////////////////////////////////
type serverPendingFinish struct {
	EncryptedUserPayload string `json:"encrypted_user_payload"`
}

func (p *serverPendingFinish) process(client *Client) error {
	plaintext, err := client.decrypt(p.EncryptedUserPayload)
	if err != nil {
		return err
	}

	return client.user.update(string(plaintext))
}

///////////////////////////////////////////////////////////////////////////////
// Finish
///////////////////////////////////////////////////////////////////////////////
type serverFinish struct {
	EncryptedToken string `json:"encrypted_token"`
}

func (f *serverFinish) process(client *Client) error {
	plaintext, err := client.decrypt(f.EncryptedToken)
	if err != nil {
		return err
	}

	client.user.Token = string(plaintext)

	client.close()

	return nil
}

///////////////////////////////////////////////////////////////////////////////
// Cancel
///////////////////////////////////////////////////////////////////////////////
type serverCancel struct{}

func (c *serverCancel) process(client *Client) error {
	client.close()

	return nil
}
