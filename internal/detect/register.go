package detect

// This file is intentionally minimal to avoid import cycles.
// Detector registration is handled at the application level (cmd/anticheat)
// by directly importing and constructing detector sub-packages.
//
// The detect package provides only the Detector interface and BaseDetector.
// Sub-packages (throw, bio, movement, state, pattern) import detect for
// BaseDetector embedding, so detect must NOT import any sub-packages.
