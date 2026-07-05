package config

// migrateKnowledgeDefaults fills in defaults for legacy configs that
// lack the "knowledge" section. Once migrated the section is marked
// initialized so deliberate user clears are preserved across loads.
func migrateKnowledgeDefaults(cfg *Config) {
	if cfg.Knowledge.Initialized {
		return
	}
	if len(cfg.Knowledge.Bases) > 0 || cfg.Knowledge.Enabled {
		cfg.Knowledge.Initialized = true
		return
	}
	defaults := Default()
	cfg.Knowledge = defaults.Knowledge
	cfg.Knowledge.Initialized = true
}
