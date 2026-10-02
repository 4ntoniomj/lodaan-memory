//go:build windows

package service

import (
	"context"
	"errors"
	"fmt"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

const (
	// stopTimeout is how long Stop waits for the service to reach Stopped.
	stopTimeout = 45 * time.Second

	// startTimeout is how long Start waits for the service to reach Running.
	startTimeout = 60 * time.Second

	// pollInterval is the interval between status queries while waiting.
	pollInterval = 250 * time.Millisecond
)

// errNotInstalled marks a service that the SCM does not know.
var errNotInstalled = errors.New("el servicio no está instalado")

// windowsManager manages a service of the Service Control Manager.
type windowsManager struct{}

// NewManager returns the SCM manager for this platform.
func NewManager() Manager { return windowsManager{} }

// wrapErr adds context to an error and, for "access denied", a hint about
// administrator privileges.
func wrapErr(action string, err error) error {
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		return fmt.Errorf("%s: acceso denegado, se necesitan permisos de administrador: %w", action, err)
	}
	return fmt.Errorf("%s: %w", action, err)
}

// connect opens the Service Control Manager.
func connect() (*mgr.Mgr, error) {
	m, err := mgr.Connect()
	if err != nil {
		return nil, wrapErr("no se pudo conectar con el Administrador de control de servicios", err)
	}
	return m, nil
}

// withService opens the named service, runs fn and closes everything. If the
// service does not exist it returns an error wrapping errNotInstalled.
func withService(name string, fn func(s *mgr.Service) error) error {
	if err := validateName(name); err != nil {
		return err
	}
	m, err := connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(name)
	if err != nil {
		if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			return fmt.Errorf("%w: %s", errNotInstalled, name)
		}
		return wrapErr(fmt.Sprintf("no se pudo abrir el servicio %s", name), err)
	}
	defer s.Close()
	return fn(s)
}

// withServiceReadOnly is like withService but opens the SCM with
// SC_MANAGER_CONNECT and the service with SERVICE_QUERY_STATUS and
// SERVICE_QUERY_CONFIG only, so querying works without administrator rights.
// The service passed to fn must only be used for Query and Config.
func withServiceReadOnly(name string, fn func(s *mgr.Service) error) error {
	if err := validateName(name); err != nil {
		return err
	}
	namePtr, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return fmt.Errorf("nombre de servicio no válido %q: %w", name, err)
	}
	hm, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return wrapErr("no se pudo conectar con el Administrador de control de servicios", err)
	}
	defer windows.CloseServiceHandle(hm)
	hs, err := windows.OpenService(hm, namePtr, windows.SERVICE_QUERY_STATUS|windows.SERVICE_QUERY_CONFIG)
	if err != nil {
		if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			return fmt.Errorf("%w: %s", errNotInstalled, name)
		}
		return wrapErr(fmt.Sprintf("no se pudo abrir el servicio %s", name), err)
	}
	s := &mgr.Service{Name: name, Handle: hs}
	defer s.Close() // calls windows.CloseServiceHandle(hs)
	return fn(s)
}

func (windowsManager) Install(spec Spec) error {
	if err := spec.validate(); err != nil {
		return err
	}
	m, err := connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()

	// The SCM has no per-service environment in its API. The variables travel
	// as "--env K=V" arguments, which "lodan service run" must accept.
	// See the report of task 5: the alternative is the undocumented registry
	// value HKLM\SYSTEM\CurrentControlSet\Services\<name>\Environment.
	args := append(append([]string{}, spec.Args...), envArgs(spec.Env)...)

	// TODO: PostgreSQL refuses to run under an administrator account, so the
	// definitive service account is decided with the real test. With an empty
	// Spec.User the SCM uses LocalSystem. Only accounts without a password
	// work here (Spec has no password): LocalSystem, LocalService,
	// NetworkService and virtual accounts such as "NT SERVICE\lodan".
	cfg := mgr.Config{
		DisplayName:      spec.DisplayName,
		Description:      spec.Description,
		StartType:        mgr.StartAutomatic,
		ServiceStartName: spec.User,
	}

	existing, err := m.OpenService(spec.Name)
	if err == nil {
		defer existing.Close()
		return updateService(existing, spec, args)
	}
	if !errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return wrapErr(fmt.Sprintf("no se pudo comprobar el servicio %s", spec.Name), err)
	}

	s, err := m.CreateService(spec.Name, spec.Exec, cfg, args...)
	if err != nil {
		return wrapErr(fmt.Sprintf("no se pudo crear el servicio %s", spec.Name), err)
	}
	defer s.Close()
	if err := configureRecovery(s); err != nil {
		_ = s.Delete() // do not leave a half-configured service behind
		return wrapErr("no se pudieron configurar las acciones de recuperación", err)
	}
	return nil
}

// updateService reconfigures a service that already exists (reinstall after an
// upgrade or a change of binary path).
func updateService(s *mgr.Service, spec Spec, args []string) error {
	cfg, err := s.Config()
	if err != nil {
		return wrapErr(fmt.Sprintf("no se pudo leer la configuración del servicio %s", spec.Name), err)
	}
	cmdLine := syscall.EscapeArg(spec.Exec)
	for _, a := range args {
		cmdLine += " " + syscall.EscapeArg(a)
	}
	cfg.BinaryPathName = cmdLine
	cfg.DisplayName = spec.DisplayName
	cfg.Description = spec.Description
	cfg.StartType = mgr.StartAutomatic
	if spec.User != "" {
		cfg.ServiceStartName = spec.User
	}
	if err := s.UpdateConfig(cfg); err != nil {
		return wrapErr(fmt.Sprintf("no se pudo actualizar el servicio %s", spec.Name), err)
	}
	if err := configureRecovery(s); err != nil {
		return wrapErr("no se pudieron configurar las acciones de recuperación", err)
	}
	return nil
}

// configureRecovery makes the SCM restart the service when it fails, the
// equivalent of systemd's Restart=on-failure.
func configureRecovery(s *mgr.Service) error {
	actions := []mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second}, // repeated for later failures
	}
	// Reset the failure count after 24 hours without failures.
	if err := s.SetRecoveryActions(actions, 24*60*60); err != nil {
		return err
	}
	// Also restart when the service exits with a non-zero code.
	return s.SetRecoveryActionsOnNonCrashFailures(true)
}

func (windowsManager) Uninstall(name string) error {
	err := withService(name, func(s *mgr.Service) error {
		// Best effort: stop it first. Delete only marks the service for
		// deletion, and the SCM removes it once it has stopped.
		_ = stopService(s)
		if err := s.Delete(); err != nil {
			return wrapErr(fmt.Sprintf("no se pudo borrar el servicio %s", name), err)
		}
		return nil
	})
	if errors.Is(err, errNotInstalled) {
		return nil // nothing to remove
	}
	return err
}

func (windowsManager) Start(name string) error {
	return withService(name, startService)
}

func (windowsManager) Stop(name string) error {
	return withService(name, stopService)
}

func (windowsManager) Restart(name string) error {
	return withService(name, func(s *mgr.Service) error {
		if err := stopService(s); err != nil {
			return err
		}
		return startService(s)
	})
}

func (windowsManager) Enable(name string) error {
	return setStartType(name, mgr.StartAutomatic)
}

func (windowsManager) Disable(name string) error {
	return setStartType(name, mgr.StartDisabled)
}

func setStartType(name string, startType uint32) error {
	return withService(name, func(s *mgr.Service) error {
		cfg, err := s.Config()
		if err != nil {
			return wrapErr(fmt.Sprintf("no se pudo leer la configuración del servicio %s", name), err)
		}
		cfg.StartType = startType
		if err := s.UpdateConfig(cfg); err != nil {
			return wrapErr(fmt.Sprintf("no se pudo cambiar el tipo de inicio del servicio %s", name), err)
		}
		return nil
	})
}

func (windowsManager) Status(name string) (Status, error) {
	var st Status
	// Read-only: querying must not need administrator rights.
	err := withServiceReadOnly(name, func(s *mgr.Service) error {
		q, err := s.Query()
		if err != nil {
			return wrapErr(fmt.Sprintf("no se pudo consultar el servicio %s", name), err)
		}
		cfg, err := s.Config()
		if err != nil {
			return wrapErr(fmt.Sprintf("no se pudo leer la configuración del servicio %s", name), err)
		}
		st.Enabled = cfg.StartType == mgr.StartAutomatic
		st.Detail = fmt.Sprintf("SCM: estado=%s, inicio=%s", stateName(q.State), startTypeName(cfg.StartType))
		switch q.State {
		case svc.Running:
			st.State = StateRunning
		case svc.Stopped:
			st.State = StateStopped
		default: // pending or paused states
			st.State = StateUnknown
		}
		return nil
	})
	if errors.Is(err, errNotInstalled) {
		return Status{State: StateNotInstalled, Detail: "el servicio no está registrado en el SCM"}, nil
	}
	if err != nil {
		return Status{}, err
	}
	return st, nil
}

// startService starts the service and waits until it is Running.
func startService(s *mgr.Service) error {
	if err := s.Start(); err != nil && !errors.Is(err, windows.ERROR_SERVICE_ALREADY_RUNNING) {
		return wrapErr(fmt.Sprintf("no se pudo arrancar el servicio %s", s.Name), err)
	}
	return waitState(s, svc.Running, startTimeout)
}

// stopService asks the service to stop and waits until it is Stopped.
func stopService(s *mgr.Service) error {
	_, err := s.Control(svc.Stop)
	switch {
	case err == nil:
	case errors.Is(err, windows.ERROR_SERVICE_NOT_ACTIVE):
		return nil // already stopped
	case errors.Is(err, windows.ERROR_SERVICE_CANNOT_ACCEPT_CTRL):
		// Probably a stop is already in progress: just wait for it.
	default:
		return wrapErr(fmt.Sprintf("no se pudo parar el servicio %s", s.Name), err)
	}
	return waitState(s, svc.Stopped, stopTimeout)
}

// waitState polls the service until it reaches want or the timeout expires.
func waitState(s *mgr.Service, want svc.State, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		q, err := s.Query()
		if err != nil {
			return wrapErr(fmt.Sprintf("no se pudo consultar el servicio %s", s.Name), err)
		}
		if q.State == want {
			return nil
		}
		if want == svc.Running && q.State == svc.Stopped {
			return fmt.Errorf("el servicio %s se detuvo durante el arranque (código de salida %d, específico %d)",
				s.Name, q.Win32ExitCode, q.ServiceSpecificExitCode)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("el servicio %s no alcanzó el estado «%s» en %s (estado actual: %s)",
				s.Name, stateName(want), timeout, stateName(q.State))
		}
		time.Sleep(pollInterval)
	}
}

func stateName(s svc.State) string {
	switch s {
	case svc.Stopped:
		return "detenido"
	case svc.StartPending:
		return "arrancando"
	case svc.StopPending:
		return "deteniéndose"
	case svc.Running:
		return "en ejecución"
	case svc.ContinuePending:
		return "reanudándose"
	case svc.PausePending:
		return "pausándose"
	case svc.Paused:
		return "en pausa"
	default:
		return fmt.Sprintf("desconocido (%d)", uint32(s))
	}
}

func startTypeName(t uint32) string {
	switch t {
	case mgr.StartAutomatic:
		return "automático"
	case mgr.StartManual:
		return "manual"
	case mgr.StartDisabled:
		return "deshabilitado"
	default:
		return fmt.Sprintf("desconocido (%d)", t)
	}
}

// IsWindowsService reports whether the process was started by the SCM.
func IsWindowsService() (bool, error) {
	return svc.IsWindowsService()
}

// RunAsWindowsService runs run under the SCM. The context passed to run is
// cancelled when the SCM sends Stop or Shutdown; the service reports
// StopPending while run finishes and the svc package reports Stopped when the
// handler returns. It blocks until the service ends.
func RunAsWindowsService(name string, run func(ctx context.Context) error) error {
	if err := svc.Run(name, &serviceHandler{run: run}); err != nil {
		return fmt.Errorf("no se pudo ejecutar como servicio de Windows: %w", err)
	}
	return nil
}

// serviceHandler adapts a run function to the svc.Handler interface.
type serviceHandler struct {
	run func(ctx context.Context) error
}

// stopWait is the maximum time the handler waits for run to return after Stop.
// It stays below stopTimeout so the handler answers before Stop gives up.
const stopWait = 40 * time.Second

func (h *serviceHandler) Execute(_ []string, requests <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	const accepted = svc.AcceptStop | svc.AcceptShutdown

	status <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- h.run(ctx) }()
	status <- svc.Status{State: svc.Running, Accepts: accepted}

	for {
		select {
		case req := <-requests:
			switch req.Cmd {
			case svc.Interrogate:
				status <- req.CurrentStatus
			case svc.Stop, svc.Shutdown:
				return h.stop(cancel, done, status)
			}
		case err := <-done:
			// run ended by itself: report it, and a failure as a
			// service-specific exit code so the SCM recovery actions apply.
			if err != nil && !errors.Is(err, context.Canceled) {
				return true, 1
			}
			return false, 0
		}
	}
}

// stop cancels run's context and waits for it to return, keeping the SCM
// informed with StopPending checkpoints.
func (h *serviceHandler) stop(cancel context.CancelFunc, done <-chan error, status chan<- svc.Status) (bool, uint32) {
	cancel()
	checkpoint := uint32(1)
	status <- svc.Status{State: svc.StopPending, CheckPoint: checkpoint, WaitHint: 10000}

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	timeout := time.After(stopWait)
	for {
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				return true, 1
			}
			return false, 0
		case <-ticker.C:
			checkpoint++
			status <- svc.Status{State: svc.StopPending, CheckPoint: checkpoint, WaitHint: 10000}
		case <-timeout:
			return true, 2 // run did not finish in time
		}
	}
}
