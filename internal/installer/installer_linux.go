package installer

import "fmt"

func GetInstaller() Installer {
	return &linuxInstaller{}
}

type linuxInstaller struct{}

func (i *linuxInstaller) Install() error {
	fmt.Println("Instalando servicio systemd en Linux...")
	return nil
}

func (i *linuxInstaller) Uninstall() error {
	return nil
}

func (i *linuxInstaller) Start() error {
	fmt.Println("Iniciando servicio systemd...")
	return nil
}

func (i *linuxInstaller) Status() (string, error) {
	return "running", nil
}
