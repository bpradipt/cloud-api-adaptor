// (C) Copyright Confidential Containers Contributors
// SPDX-License-Identifier: Apache-2.0

package tlsutil

import "fmt"

// NewCAServiceFromMaterial constructs a CAService from persisted PEM-encoded
// certificate and private key. Used to restore the CA across process restarts.
func NewCAServiceFromMaterial(certPEM, keyPEM []byte) (CAService, error) {
	if len(certPEM) == 0 || len(keyPEM) == 0 {
		return nil, fmt.Errorf("certPEM and keyPEM must not be empty")
	}
	return &caService{
		orgName: "agent-protocol-forwarder",
		certPEM: certPEM,
		keyPEM:  keyPEM,
	}, nil
}
