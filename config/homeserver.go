package config

type homeserver struct {
	Address        string `yaml:"address"`
	Domain         string `yaml:"domain"`
	StatusEndpoint string `yaml:"status_endpoint"`
}
