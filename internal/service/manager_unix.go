//go:build !windows

package service

import (
	"context"
	"errors"
)

// IsWindowsService always reports false outside Windows.
func IsWindowsService() (bool, error) {
	return false, nil
}

// RunAsWindowsService is only available on Windows.
func RunAsWindowsService(name string, run func(ctx context.Context) error) error {
	return errors.New("ejecutar como servicio de Windows solo es posible en Windows")
}
