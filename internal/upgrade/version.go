package upgrade

// AppVersion tracks the structural version of the user's ~/.p-chat
// installation. The upgrade system reads the version file on startup
// and runs any pending steps in order.
//
// ADDING A NEW VERSION:
//  1. Add a constant for the new version below.
//  2. Update Current to point to it.
//  3. Register a step function in steps.go.
//  4. Bump the version in testdata if needed.
//
// The version file is a plain-text file at ~/.p-chat/version containing
// just the integer version number (e.g. "3").
type AppVersion int

const (
	// V0 — no version file exists (pre-upgrade-system installs).
	V0 AppVersion = 0

	// V1 — baseline version tracked from this point forward.
	// Users on V1 have the file-system prompt layout (identity/ + soul/).
	V1 AppVersion = 1

	// V2 — identity + soul merged into single style/ directory.
	V2 AppVersion = 2

	// V3 — styles migrated to SQLite (styles table), built-in
	// prompts embedded in the binary.
	V3 AppVersion = 3

	// V4 — PCHAT_HOME split into PCHAT_HOME (install root,
	// used in PATH) + PCHAT_DATA_HOME (data dir override).
	// stepV3toV4 rescues any memory / config that the old
	// code wrote under the install directory on machines
	// that had run install.ps1 -AddToPath.
	V4 AppVersion = 4

	// V5 — work_mode added to config/session metadata. JSON
	// fields are backward compatible and Normalize() handles
	// missing values, so no data migration is required.
	V5 AppVersion = 5

	// Current is the latest AppVersion. When adding a new version,
	// update this constant and register a step in steps.go.
	Current AppVersion = V5
)
