package remoteauth

import (
	"crypto/x509"
	"encoding/base64"
	"fmt"
)

type clientPacket interface {
	send(client *Client) error
}

///////////////////////////////////////////////////////////////////////////////
// Heartbeat
///////////////////////////////////////////////////////////////////////////////
type clientHeartbeat struct {
	OP string `json:"op"`
}

func (h *clientHeartbeat) send(client *Client) error {
	// make sure our op string is set
	h.OP = "heartbeat"

	client.heartbeats += 1
	if client.heartbeats > 2 {
		return fmt.Errorf("server failed to acknowledge our heartbeats")
	}

	return client.write(h)
}

///////////////////////////////////////////////////////////////////////////////
// Init
///////////////////////////////////////////////////////////////////////////////
type clientInit struct {
	OP               string `json:"op"`
	EncodedPublicKey string `json:"encoded_public_key"`
}

func (i *clientInit) send(client *Client) error {
	i.OP = "init"

	pubkey := client.privateKey.Public()

	raw, err := x509.MarshalPKIXPublicKey(pubkey)
	if err != nil {
		return err
	}

	i.EncodedPublicKey = base64.RawStdEncoding.EncodeToString(raw)

	return client.write(i)
}

///////////////////////////////////////////////////////////////////////////////
// NonceProof
///////////////////////////////////////////////////////////////////////////////
type clientNonceProof struct {
	OP    string `json:"op"`
	Proof string `json:"proof"`
}

func (n *clientNonceProof) send(client *Client) error {
	n.OP = "nonce_proof"

	// All of the other work was taken care of by the server packet as it knows
	// the payload.

	return client.write(n)
}
