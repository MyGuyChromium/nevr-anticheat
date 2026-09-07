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
	doc, err := os.ReadFile(os.Args[2])
	if err != nil {
		panic(err)
	}
	for _, line := range strings.Split(string(doc), "\n") {
		if !strings.HasPrefix(line, "db_path = ") {
			continue
		}
		var path string
		if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "db_path = "))), &path); err != nil {
			panic(err)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			panic("runner escaped its isolated test root")
		}
		if err := os.WriteFile(path, []byte("synthetic runner marker, not a database"), 0o600); err != nil {
			panic(err)
		}
		fmt.Println("synthetic runner success")
		return
	}
	panic("runner omitted database configuration")
}
