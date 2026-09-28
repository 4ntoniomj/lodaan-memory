package installer

import (
	"os"

	"github.com/kardianos/service"
)

type Installer interface {
	Install() error
	Uninstall() error
	Start() error
	Stop() error
	Status() (string, error)
	Run(func(mode string)) error
}

type svc struct {
	runFunc func(mode string)
}

func (p *svc) Start(s service.Service) error {
	go p.runFunc("http")
	return nil
}

func (p *svc) Stop(s service.Service) error {
	return nil
}

func GetInstaller(runFunc func(mode string)) Installer {
	svcConfig := &service.Config{
		Name:        "LodanMemoryServer",
		DisplayName: "Lodan Memory Server",
		Description: "Servidor de memoria persistente y MCP para Lodan",
	}

	execPath, err := os.Executable()
	if err == nil {
		svcConfig.Executable = execPath
		svcConfig.Arguments = []string{"start"}
	}

	prg := &svc{
		runFunc: runFunc,
	}

	s, err := service.New(prg, svcConfig)
	if err != nil {
		return nil
	}

	return &kardianosInstaller{s: s, prg: prg}
}

type kardianosInstaller struct {
	s   service.Service
	prg *svc
}

func (i *kardianosInstaller) Install() error {
	return i.s.Install()
}

func (i *kardianosInstaller) Uninstall() error {
	return i.s.Uninstall()
}

func (i *kardianosInstaller) Start() error {
	return i.s.Start()
}

func (i *kardianosInstaller) Stop() error {
	return i.s.Stop()
}

func (i *kardianosInstaller) Status() (string, error) {
	status, err := i.s.Status()
	if err != nil {
		return "", err
	}
	switch status {
	case service.StatusRunning:
		return "running", nil
	case service.StatusStopped:
		return "stopped", nil
	default:
		return "unknown", nil
	}
}

func (i *kardianosInstaller) Run(runFunc func(mode string)) error {
	return i.s.Run()
}
