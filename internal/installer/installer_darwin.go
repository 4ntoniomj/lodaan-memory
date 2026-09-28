package installer

import "fmt"

func GetInstaller() Installer {
	return &darwinInstaller{}
}

type darwinInstaller struct{}

func (i *darwinInstaller) Install() error {
	fmt.Println("Instalando servicio launchd en macOS...")
	return nil
}

func (i *darwinInstaller) Uninstall() error {
	return nil
}

func (i *darwinInstaller) Start() error {
	fmt.Println("Iniciando servicio launchd...")
	return nil
}

func (i *darwinInstaller) Status() (string, error) {
	return "running", nil
}
