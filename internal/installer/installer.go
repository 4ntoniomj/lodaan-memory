package installer

type Installer interface {
	Install() error
	Uninstall() error
	Start() error
	Status() (string, error)
}
