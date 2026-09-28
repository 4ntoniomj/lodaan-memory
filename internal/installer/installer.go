package installer

import "runtime"

type Installer interface {
	Install() error
	Uninstall() error
	Start() error
	Status() (string, error)
}

func GetInstaller() Installer {
	switch runtime.GOOS {
	case "linux":
		return &linuxInstaller{}
	case "darwin":
		return &darwinInstaller{}
	case "windows":
		return &windowsInstaller{}
	default:
		return nil
	}
}
