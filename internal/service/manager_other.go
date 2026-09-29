//go:build !linux && !darwin && !windows

package service

import (
	"fmt"
	"runtime"
)

// unsupportedManager is the Manager of platforms without service support.
type unsupportedManager struct{}

// NewManager returns a Manager whose every operation fails: this platform has
// no supported service manager.
func NewManager() Manager { return unsupportedManager{} }

func errUnsupported() error {
	return fmt.Errorf("no soportado: la gestión de servicios no está disponible en %s", runtime.GOOS)
}

func (unsupportedManager) Install(Spec) error {
	return errUnsupported()
}

func (unsupportedManager) Uninstall(string) error {
	return errUnsupported()
}

func (unsupportedManager) Start(string) error {
	return errUnsupported()
}

func (unsupportedManager) Stop(string) error {
	return errUnsupported()
}

func (unsupportedManager) Restart(string) error {
	return errUnsupported()
}

func (unsupportedManager) Enable(string) error {
	return errUnsupported()
}

func (unsupportedManager) Disable(string) error {
	return errUnsupported()
}

func (unsupportedManager) Status(string) (Status, error) {
	return Status{}, errUnsupported()
}
