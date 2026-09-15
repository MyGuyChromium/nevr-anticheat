package flyagent

// DigestArenaConfig validates and fingerprints the complete deterministic
// arena configuration for artifact binding and reproducibility checks.
func DigestArenaConfig(config ArenaConfig) (string, error) {
	if err := config.validate(); err != nil {
		return "", err
	}
	return arenaConfigDigest(config)
}
