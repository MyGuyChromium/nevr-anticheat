package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
)

// startupFailure is what the person at the desk needs to read when the app
// cannot start: a title, the reason in plain words, and where the details are.
type startupFailure struct {
	Title   string
	Message string
}

// describeStartupFailure turns a startup error into that message. A database
// from a newer build gets its own wording: the fix is to update, and nothing
// is wrong with the data.
func describeStartupFailure(err error, dbPath, logPath string) startupFailure {
	out := startupFailure{Title: "NEVR-Anticheat could not start", Message: err.Error()}
	var newer *sqlite.NewerSchemaError
	if errors.As(err, &newer) {
		out.Title = "NEVR-Anticheat needs an update"
		out.Message = newer.Error() + "."
		if dbPath != "" {
			out.Message += "\n\nDatabase: " + dbPath
		}
		out.Message += "\n\nInstall the latest NEVR-Anticheat and start it again. Your matches, reviews and evidence are untouched."
	}
	if logPath != "" {
		out.Message += "\n\nLog: " + logPath
	}
	return out
}

// reportStartupFailure is the desktop startup error surface. The message is
// always written to stderr (the console, or the log file of a GUI build); when
// there is no console to read it from, it is also shown in a dialog, because a
// GUI-subsystem app that fails silently looks like an app that does nothing.
func reportStartupFailure(failure startupFailure, dialog bool) {
	fmt.Fprintf(os.Stderr, "%s: %s\n", failure.Title, failure.Message)
	if dialog {
		showStartupDialog(failure.Title, failure.Message)
	}
}
