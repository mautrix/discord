package config

type encryption struct {
	Allow   bool `yaml:"allow"`
	Default bool `yaml:"default"`

	KeySharing struct {
		Allow               bool `yaml:"allow"`
		RequireCrossSigning bool `yaml:"require_cross_signing"`
		RequireVerification bool `yaml:"require_verification"`
	} `yaml:"key_sharing"`
}

func (e *encryption) validate() error {
	return nil
}

func (e *encryption) UnmarshalYAML(unmarshal func(interface{}) error) error {
	type rawEncryption encryption

	raw := rawEncryption{}
	if err := unmarshal(&raw); err != nil {
		return err
	}

	*e = encryption(raw)

	return e.validate()
}
