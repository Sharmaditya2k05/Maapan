package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDotEnv(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".env")
	os.WriteFile(p, []byte("A1=7004   # port\nA2=\"x # not comment\" # c\nA3='q'\nA4=pass#word\nexport A5=v\nA6= # empty\n"), 0o600)
	LoadDotEnv(p)
	want := map[string]string{"A1": "7004", "A2": "x # not comment", "A3": "q", "A4": "pass#word", "A5": "v", "A6": ""}
	for k, v := range want {
		if got := os.Getenv(k); got != v {
			t.Errorf("%s=%q want %q", k, got, v)
		}
	}
}
