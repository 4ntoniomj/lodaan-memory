package installer

import "fmt"

func GetInstaller() Installer {
	return &windowsInstaller{}
}

type windowsInstaller struct{}

func (i *windowsInstaller) Install() error {
	fmt.Println("Instalando Windows Service...")
	return nil
}

func (i *windowsInstaller) Uninstall() error {
	return nil
}

func (i *windowsInstaller) Start() error {
	fmt.Println("Iniciando Windows Service...")
	return nil
}

func (i *windowsInstaller) Status() (string, error) {
	return "running", nil
}
