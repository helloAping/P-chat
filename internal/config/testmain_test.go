package config

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	oldDataHome, hadDataHome := os.LookupEnv("PCHAT_DATA_HOME")
	_ = os.Unsetenv("PCHAT_DATA_HOME")
	code := m.Run()
	if hadDataHome {
		_ = os.Setenv("PCHAT_DATA_HOME", oldDataHome)
	} else {
		_ = os.Unsetenv("PCHAT_DATA_HOME")
	}
	os.Exit(code)
}
