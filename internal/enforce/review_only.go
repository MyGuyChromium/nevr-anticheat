package enforce

// ReviewOnly is an immutable safety property of this build, not a default
// setting. Detector modes, imported profiles, moderator decisions, environment
// variables, retries and restored databases cannot enable punitive dispatch.
// Recommendations may still describe an action for human review; they are
// never authorization to execute it. Changing this boundary requires a code
// change and a separately reviewed deployment.
const ReviewOnly = true

// PolicyVersion identifies the executable safety boundary in diagnostic and
// provenance reports. It does not identify or validate detector thresholds.
const PolicyVersion = "review-only-v1"
