package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func writeConfig(t *testing.T, configPath, outputPath string) {
	t.Helper()
	quoted, err := json.Marshal(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, append([]byte("db_path = "), quoted...), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSyntheticConfinedQuotedPaths(t *testing.T) {
	for _, absolute := range []bool{false, true} {
		name := "relative"
		if absolute {
			name = "absolute"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			directory := "O'Brien test runs"
			if err := os.Mkdir(filepath.Join(root, directory), 0o700); err != nil {
				t.Fatal(err)
			}
			config := filepath.Join(directory, "runner config.toml")
			output := filepath.Join(directory, "soak.db")
			configOnDisk, outputOnDisk := filepath.Join(root, config), filepath.Join(root, output)
			if absolute {
				config, output = configOnDisk, outputOnDisk
			}
			writeConfig(t, configOnDisk, output)
			if err := runSynthetic(root, config); err != nil {
				t.Fatal(err)
			}
			marker, err := os.ReadFile(outputOnDisk)
			if err != nil || string(marker) != "synthetic runner marker, not a database" {
				t.Fatalf("wrong marker: %q, error: %v", marker, err)
			}
		})
	}
}

func TestSyntheticRejectsEscapedConfig(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "isolated")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "soak.db")
	outsideConfig := filepath.Join(parent, "outside.toml")
	writeConfig(t, outsideConfig, output)
	for _, config := range []string{outsideConfig, filepath.Join("..", "outside.toml")} {
		if err := runSynthetic(root, config); err == nil {
			t.Errorf("accepted escaped config %q", config)
		}
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("escaped config produced output: %v", err)
	}
}

func TestSyntheticRejectsEscapedOutput(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "isolated")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	outsideOutput := filepath.Join(parent, "soak.db")
	if err := os.WriteFile(outsideOutput, []byte("untouched sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(root, "runner.toml")
	for _, output := range []string{outsideOutput, filepath.Join("..", "soak.db")} {
		writeConfig(t, config, output)
		if err := runSynthetic(root, config); err == nil {
			t.Errorf("accepted escaped output %q", output)
		}
	}
	marker, err := os.ReadFile(outsideOutput)
	if err != nil || string(marker) != "untouched sentinel" {
		t.Fatalf("outside output changed: %q, error: %v", marker, err)
	}
}

func TestSyntheticRejectsSymlinkEscapes(t *testing.T) {
	for _, target := range []string{"config", "output"} {
		t.Run(target, func(t *testing.T) {
			parent := t.TempDir()
			root := filepath.Join(parent, "isolated")
			if err := os.Mkdir(root, 0o700); err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(parent, "outside")
			config := filepath.Join(root, "runner.toml")
			output := filepath.Join(root, "soak.db")
			link := config
			if target == "config" {
				writeConfig(t, outside, output)
			} else {
				link = output
				writeConfig(t, config, output)
				if err := os.WriteFile(outside, []byte("untouched sentinel"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(filepath.Join("..", "outside"), link); err != nil {
				if runtime.GOOS != "windows" {
					t.Fatalf("symlink escape coverage must run on this platform: %v", err)
				}
				t.Skipf("symlinks unavailable: %v", err)
			}
			if err := runSynthetic(root, config); err == nil {
				t.Fatal("accepted symlink escape")
			}
			if target == "config" {
				if _, err := os.Stat(output); !os.IsNotExist(err) {
					t.Fatalf("escaped config produced output: %v", err)
				}
			} else {
				marker, err := os.ReadFile(outside)
				if err != nil || string(marker) != "untouched sentinel" {
					t.Fatalf("outside output changed: %q, error: %v", marker, err)
				}
			}
		})
	}
}
