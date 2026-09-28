//go:build !linux && !darwin && !windows

package installer

func GetInstaller() Installer {
	return nil
}
