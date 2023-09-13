package main

const (
	programName          = "process-user-data"
	providerAzure        = "azure"
	providerAws          = "aws"
	AzureImdsUrl         = "http://169.254.169.254/metadata/instance/compute?api-version=2021-01-01"
	AzureUserDataImdsUrl = "http://169.254.169.254/metadata/instance/compute/userData?api-version=2021-01-01&format=text"
	AWSImdsUrl           = "http://169.254.169.254/latest/meta-data/"
	AWSUserDataImdsUrl   = "http://169.254.169.254/latest/user-data"

	defaultAgentConfigPath = "/etc/agent-config.toml"
)

type Config struct {
	daemonConfigPath     string
	agentConfigPath      string
	userData             string
	userDataFetchTimeout int
}

type Endpoints struct {
	Allowed []string `toml:"allowed"`
}

type AgentConfig struct {
	EnableSignatureVerification bool      `toml:"enable_signature_verification"`
	ServerAddr                  string    `toml:"server_addr"`
	AaKbcParams                 string    `toml:"aa_kbc_params"`
	ImageRegistryAuthFile       string    `toml:"image_registry_auth_file"`
	Endpoints                   Endpoints `toml:"endpoints"`
}
