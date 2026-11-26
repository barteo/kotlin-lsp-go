package main

import (
	"encoding/json"
	"os"
	"testing"
)

// TestPluginVersion checks that the Claude Code marketplace lists the
// kcls plugin at the version of its plugin.json.
func TestPluginVersion(t *testing.T) {
	var market struct {
		Plugins []struct{ Name, Source, Version string }
	}
	var plugin struct{ Name, Version string }
	read := func(path string, v any) {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(b, v); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
	}
	read("../../.claude-plugin/marketplace.json", &market)
	read("../../editors/claude-code/.claude-plugin/plugin.json", &plugin)
	for _, p := range market.Plugins {
		if p.Name == plugin.Name {
			if p.Version != plugin.Version || p.Source != "./editors/claude-code" {
				t.Errorf("marketplace entry %+v, plugin.json version %s", p, plugin.Version)
			}
			return
		}
	}
	t.Errorf("no marketplace entry for %s", plugin.Name)
}
