// Copyright Confidential Containers Contributors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/avast/retry-go/v4"
	"github.com/confidential-containers/cloud-api-adaptor/cmd"
	daemon "github.com/confidential-containers/cloud-api-adaptor/pkg/forwarder"
	"github.com/confidential-containers/cloud-api-adaptor/pkg/forwarder/interceptor"
	"github.com/confidential-containers/cloud-api-adaptor/pkg/podnetwork"
	"github.com/confidential-containers/cloud-api-adaptor/pkg/util/tlsutil"
)

const programName = "agent-protocol-forwarder"

type Config struct {
	tlsConfig           *tlsutil.TLSConfig
	daemonConfig        daemon.Config
	configPath          string
	listenAddr          string
	kataAgentSocketPath string
	kataAgentNamespace  string
	HostInterface       string
}

// Add a method to retrieve userData from Azure IMDS (Instance Metadata Service)
// and return it as a string
func getUserData(ctx context.Context) string {

	// Create a new HTTP client
	client := &http.Client{}

	// Create a new request to retrieve the VM's userData
	// curl -H Metadata:true --noproxy "*" "http://169.254.169.254/metadata/instance/compute/userData?api-version=2021-01-01&format=text" | base64 --decode
	// Set Metadata to true in the request header
	// Set the request method to GET
	// Set the url to "http://169.254.169.254/metadata/instance/compute/userData?api-version=2021-01-01&format=text"

	imdsURL := "http://169.254.169.254/metadata/instance/compute/userData?api-version=2021-01-01&format=text"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, imdsURL, nil)
	if err != nil {
		fmt.Printf("failed to create request: %s", err)
		return ""
	}
	// Add the required headers to the request
	req.Header.Add("Metadata", "true")

	// Send the request and retrieve the response
	resp, err := client.Do(req)
	if err != nil {
		fmt.Printf("failed to send request: %s", err)
		return ""
	}
	defer resp.Body.Close()

	// Check if the response was successful
	if resp.StatusCode != http.StatusOK {
		fmt.Printf("failed to retrieve userData: %s", resp.Status)
		return ""
	}

	// Read the response body and return it as a string
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Printf("failed to read response body: %s", err)
		return ""
	}

	// Sample data
	/*
			{
		    "pod-network": {
		        "podip": "10.244.0.19/24",
		        "pod-hw-addr": "0e:8f:62:f3:81:ad",
		        "interface": "eth0",
		        "worker-node-ip": "10.224.0.4/16",
		        "tunnel-type": "vxlan",
		        "routes": [
		            {
		                "Dst": "",
		                "GW": "10.244.0.1",
		                "Dev": "eth0"
		            }
		        ],
		        "mtu": 1500,
		        "index": 1,
		        "vxlan-port": 8472,
		        "vxlan-id": 555001,
		        "dedicated": false
		    },
		    "pod-namespace": "default",
		    "pod-name": "nginx-866fdb5bfb-b98nw",
		    "tls-server-key": "-----BEGIN PRIVATE KEY-----\n....\n-----END PRIVATE KEY-----\n",
		    "tls-server-cert": "-----BEGIN CERTIFICATE-----\n....\n-----END CERTIFICATE-----\n",
		    "tls-client-ca": "-----BEGIN CERTIFICATE-----\n....\n-----END CERTIFICATE-----\n"
		}
	*/

	// The response is base64 encoded

	// Decode the base64 response
	decoded, err := base64.StdEncoding.DecodeString(string(body))
	if err != nil {
		fmt.Printf("failed to decode b64 encoded userData: %s", err)
		return ""
	}

	return string(decoded)
}

// Add method to parse userData and copy it to a file
func parseUserData(userData string, path string) error {

	// Write userData to file specified in the path var
	// Create the directory and the file - /peerpod/daemon.json

	// Split the path into directory and file name
	splitPath := strings.Split(path, "/")
	dir := strings.Join(splitPath[:len(splitPath)-1], "/")

	// Create the directory
	err := os.MkdirAll(dir, 0755)
	if err != nil {
		return fmt.Errorf("failed to create directory: %s", err)

	}

	// Create the file
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("failed to create file: %s", err)
	}
	defer file.Close()

	// Write userData to file
	_, err = file.WriteString(userData)
	if err != nil {
		return fmt.Errorf("failed to write userData to file: %s", err)
	}

	return nil

}

func load(path string, obj interface{}) error {

	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("failed to open %s: %w", path, err)
	}

	if err := json.NewDecoder(file).Decode(obj); err != nil {
		return fmt.Errorf("failed to decode a Agent Protocol Forwarder config file file: %s: %w", path, err)
	}

	return nil
}

func (cfg *Config) Setup() (cmd.Starter, error) {

	var (
		showVersion          bool
		disableTLS           bool
		tlsConfig            tlsutil.TLSConfig
		userDataFetchTimeout time.Duration
		userData             string
		ctx                  context.Context
		cancel               context.CancelFunc
	)

	cmd.Parse(programName, os.Args, func(flags *flag.FlagSet) {
		flags.BoolVar(&showVersion, "version", false, "Show version")
		flags.StringVar(&cfg.configPath, "config", daemon.DefaultConfigPath, "Path to a deamon config file")
		flags.StringVar(&cfg.listenAddr, "listen", daemon.DefaultListenAddr, "Listen address")
		flags.StringVar(&cfg.kataAgentSocketPath, "kata-agent-socket", daemon.DefaultKataAgentSocketPath, "Path to a kata agent socket")
		flags.StringVar(&cfg.kataAgentNamespace, "kata-agent-namespace", daemon.DefaultKataAgentNamespace, "Path to the network namespace where kata agent runs")
		flags.StringVar(&cfg.HostInterface, "host-interface", "", "network interface name that is used for network tunnel traffic")
		flags.StringVar(&tlsConfig.CAFile, "ca-cert-file", "", "CA cert file")
		flags.StringVar(&tlsConfig.CertFile, "cert-file", "", "cert file")
		flags.StringVar(&tlsConfig.KeyFile, "cert-key", "", "cert key")
		flags.BoolVar(&tlsConfig.SkipVerify, "tls-skip-verify", false, "Skip TLS certificate verification - use it only for testing")
		flags.BoolVar(&disableTLS, "disable-tls", false, "Disable TLS encryption - use it only for testing")
		// flag to specify the timeout for retrieving the VM's userData
		flags.DurationVar(&userDataFetchTimeout, "userdata-fetch-timeout", 0, "Timeout for retrieving the VM's userData. Default is infinite.")
	})

	if !disableTLS {
		cfg.tlsConfig = &tlsConfig
	}

	cmd.ShowVersion(programName)

	if showVersion {
		cmd.Exit(0)
	}

	// Use retry.Do to retry the getUserData function until it succeeds
	// This is needed because the VM's userData is not available immediately
	// Have an option to either wait forever or timeout after a certain amount of time
	// https://github.com/avast/retry-go

	// If userDataFetchTimeout is set to 0, then create context with infinite timeout
	// Else create context with the specified timeout
	if userDataFetchTimeout == 0 {
		ctx, cancel = context.WithCancel(context.Background())
		defer cancel()
	} else {
		ctx, cancel = context.WithTimeout(context.Background(), userDataFetchTimeout)
		defer cancel()
	}

	err := retry.Do(
		func() error {
			userData = getUserData(ctx)
			if userData != "" && strings.Contains(userData, "podip") {
				return nil // Valid user data, stop retrying
			}
			return fmt.Errorf("invalid user data")
		},
		retry.Context(ctx),                // Use the context with timeout
		retry.Delay(5*time.Second),        // Set the delay between retries
		retry.LastErrorOnly(true),         // Only consider the last error for retry decision
		retry.DelayType(retry.FixedDelay), // Use fixed delay between retries
		retry.OnRetry(func(n uint, err error) { // Optional: log retry attempts
			fmt.Printf("Retry attempt %d: %v\n", n, err)
		}),
	)

	if err != nil {
		fmt.Println("Error: Failed to get valid user data")
	} else {
		fmt.Printf("Valid user data: %s\n", userData)
	}

	// Parse the userData and copy the specified values to the cfg.configPath file
	if err := parseUserData(userData, cfg.configPath); err != nil {
		fmt.Printf("Error: Failed to parse userData: %s\n", err)
		return nil, err
	}

	for path, obj := range map[string]interface{}{
		cfg.configPath: &cfg.daemonConfig,
	} {
		if err := load(path, obj); err != nil {
			return nil, err
		}
	}

	interceptor := interceptor.NewInterceptor(cfg.kataAgentSocketPath, cfg.kataAgentNamespace)

	podNode := podnetwork.NewPodNode(cfg.kataAgentNamespace, cfg.HostInterface, cfg.daemonConfig.PodNetwork)

	daemon := daemon.NewDaemon(&cfg.daemonConfig, cfg.listenAddr, cfg.tlsConfig, interceptor, podNode)

	return cmd.NewStarter(daemon), nil
}

var config cmd.Config = &Config{}

func main() {

	starter, err := config.Setup()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %s\n", os.Args[0], err)
		cmd.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := starter.Start(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %s\n", os.Args[0], err)
		cmd.Exit(1)
	}
}
