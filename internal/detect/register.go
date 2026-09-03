package detect

// Detector sub-packages (throw, bio, movement, state, pattern) embed
// BaseDetector and therefore import this package, so detect must NOT import
// any of them. The catalog is instead populated by each sub-package's init()
// calling MustRegister (see registry.go). Any binary or test that imports
// the sub-packages — cmd/anticheat, cmd/server and internal/testutil already
// do — then sees the complete catalog through Catalog()/BuildAll() without
// maintaining its own hand-rolled constructor list.
