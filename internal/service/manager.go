// Package service manages lodan as a native system service: a systemd unit on
// Linux, a LaunchDaemon on macOS and a Service Control Manager service on
// Windows. Every implementation satisfies the Manager interface.
package service

// DefaultName is the name under which lodan registers itself as a service.
const DefaultName = "lodan"

// Spec describes the service to register.
type Spec struct {
	// Name is the service identifier (unit name, launchd label suffix, SCM name).
	Name string

	// DisplayName is the human-readable name (used on Windows).
	DisplayName string

	// Description is a one-line description of the service.
	Description string

	// Exec is the absolute path of the executable.
	Exec string

	// Args are the arguments passed to Exec.
	Args []string

	// User is the account the service runs as. Empty means the platform default.
	User string

	// Env holds the environment variables of the service process.
	Env map[string]string

	// LogDir is the directory for the service logs (launchd only; systemd uses
	// the journal and the SCM has no stdout).
	LogDir string
}

// State is the run state of a service.
type State string

const (
	StateRunning      State = "running"
	StateStopped      State = "stopped"
	StateNotInstalled State = "not-installed"
	StateUnknown      State = "unknown"
)

// Status is the result of querying a service.
type Status struct {
	State State

	// Enabled reports whether the service starts automatically at boot.
	Enabled bool

	// Detail is free text with the raw platform state, for diagnostics.
	Detail string
}

// Manager controls a system service. Installing, removing and controlling a
// service requires administrator privileges; the Manager does not elevate.
type Manager interface {
	Install(s Spec) error
	Uninstall(name string) error
	Start(name string) error
	Stop(name string) error
	Restart(name string) error
	Enable(name string) error
	Disable(name string) error
	Status(name string) (Status, error)
}

// The rest of the public API is defined per platform through build tags:
//
//	func NewManager() Manager
//	func IsWindowsService() (bool, error)
//	func RunAsWindowsService(name string, run func(ctx context.Context) error) error
//
// NewManager lives in manager_linux.go, manager_darwin.go, manager_windows.go
// and manager_other.go; the two Windows functions live in manager_windows.go
// (real implementation) and manager_unix.go (stubs for every other platform).
