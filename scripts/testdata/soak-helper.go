// This synthetic child is built only by test-soak-runner.ps1. It never opens
// SQLite or a replay; it validates the runner's config quoting and isolation.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	root := os.Getenv("NEVR_SOAK_TEST_ROOT")
	if root == "" || len(os.Args) < 3 || os.Args[1] != "--config" {
		os.Exit(2)
	}
	switch os.Getenv("NEVR_SOAK_TEST_MODE") {
	case "hang":
		time.Sleep(10 * time.Minute)
		return
	case "fail":
		os.Exit(3)
	}
	if err := runSynthetic(root, os.Args[2]); err != nil {
		panic(err)
	}
}

func runSynthetic(rootPath, configPath string) error {
	rootPath, err := filepath.Abs(rootPath)
	if err != nil {
		return err
	}
	configPath, err = pathWithinRoot(rootPath, configPath)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return err
	}
	defer root.Close()
	doc, err := root.ReadFile(configPath)
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(doc), "\n") {
		if !strings.HasPrefix(line, "db_path = ") {
			continue
		}
		var path string
		if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "db_path = "))), &path); err != nil {
			return err
		}
		path, err = pathWithinRoot(rootPath, path)
		if err != nil {
			return err
		}
		if err := root.WriteFile(path, []byte("synthetic runner marker, not a database"), 0o600); err != nil {
			return err
		}
		fmt.Println("synthetic runner success")
		return nil
	}
	return fmt.Errorf("runner omitted database configuration")
}

// The runner supplies absolute paths; tests may also use paths relative to the
// isolated root. Root methods enforce the boundary again when following links.
func pathWithinRoot(root, path string) (string, error) {
	if filepath.IsAbs(path) {
		var err error
		path, err = filepath.Rel(root, path)
		if err != nil {
			return "", err
		}
	}
	if !filepath.IsLocal(path) {
		return "", fmt.Errorf("runner escaped its isolated test root")
	}
	return path, nil
}
