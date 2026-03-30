// Copyright Confidential Containers Contributors
// SPDX-License-Identifier: Apache-2.0

package proxy

import (
	"time"

	"github.com/confidential-containers/cloud-api-adaptor/src/cloud-api-adaptor/pkg/util/tlsutil"
)

type Factory interface {
	New(serverName, socketPath string) AgentProxy
}

type factory struct {
	pauseImage   string
	tlsConfig    *tlsutil.TLSConfig
	caService    tlsutil.CAService
	proxyTimeout time.Duration
}

func NewFactory(pauseImage string, tlsConfig *tlsutil.TLSConfig, proxyTimeout time.Duration) Factory {

	if tlsConfig != nil && !tlsConfig.HasCertAuth() {

		certPEM, keyPEM, err := tlsutil.NewClientCertificate("cloud-api-adaptor")
		if err != nil {
			panic(err)
		}
		tlsConfig.CertData = certPEM
		tlsConfig.KeyData = keyPEM
	}

	var caService tlsutil.CAService

	if tlsConfig != nil && !tlsConfig.HasCA() {

		s, err := tlsutil.NewCAService("agent-protocol-forwarder")
		if err != nil {
			panic(err)
		}
		caService = s
		tlsConfig.CAData = caService.RootCertificate()
	}

	return &factory{
		pauseImage:   pauseImage,
		tlsConfig:    tlsConfig,
		caService:    caService,
		proxyTimeout: proxyTimeout,
	}
}

// NewFactoryWithCAService creates a Factory using pre-built TLS material.
// Use this when TLS material has been loaded from persistent storage (e.g.
// after a CAA restart) to ensure the same CA and client certificate are reused
// across process restarts. tlsConfig must already have CertData, KeyData, and
// CAData populated. caService must be able to issue server certificates for
// peer pod VMs.
func NewFactoryWithCAService(pauseImage string, tlsConfig *tlsutil.TLSConfig, proxyTimeout time.Duration, caService tlsutil.CAService) Factory {
	return &factory{
		pauseImage:   pauseImage,
		tlsConfig:    tlsConfig,
		caService:    caService,
		proxyTimeout: proxyTimeout,
	}
}

func (f *factory) New(serverName, socketPath string) AgentProxy {

	return NewAgentProxy(serverName, socketPath, f.pauseImage, f.tlsConfig, f.caService, f.proxyTimeout)
}
