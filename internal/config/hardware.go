package config

// HardwareConfig holds settings for the Hardware page.
type HardwareConfig struct {
	// AllowPowerCap enables changing GPU power limits through the API.
	AllowPowerCap bool `yaml:"allowPowerCap"`
}
