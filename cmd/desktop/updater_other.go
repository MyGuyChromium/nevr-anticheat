//go:build !windows

package main

import "errors"

func updateInstallSupport() (bool, string) {
	return false, "One-click installation is available in the packaged Windows app"
}

func launchUpdateHelper(updateLaunchRequest) error {
	return errors.New("one-click installation is only supported on Windows")
}

func maybeRunUpdateHelper(args []string) (bool, error) {
	if len(args) > 0 && args[0] == "--nevr-update-helper" {
		return true, errors.New("one-click installation is only supported on Windows")
	}
	return false, nil
}
