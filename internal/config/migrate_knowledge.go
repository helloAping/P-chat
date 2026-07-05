package config

// migrateKnowledgeDefaults detects configs loaded from older JSON files
// that lack the "knowledge" section, and injects safe defaults.
func migrateKnowledgeDefaults(cfg *Config) {
	if len(cfg.Knowledge.Bases) > 0 || cfg.Knowledge.Enabled {
		return
	}
	// Check if the Knowledge section exists by looking for the Enabled
	// key. A legacy config without "knowledge" key unmarshals to the
	// zero value, so we inject defaults.
	defaults := Default()
	cfg.Knowledge = defaults.Knowledge
}
