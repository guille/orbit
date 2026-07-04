package systemd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ChangeSet describes the set of changes needed to reconcile desired state.
type ChangeSet struct {
	Create []Unit // new units to create
	Update []Unit // existing units whose content changed
	Remove []Unit // units to delete
	Keep   []Unit // units that are already up-to-date
}

// ClassifyChanges compares desired units against what's on disk and returns
// a ChangeSet.
func (m *Manager) ClassifyChanges(desired []Unit, existingNames []string) ChangeSet {
	systemdDir := m.UnitDir()

	existingSet := make(map[string]bool, len(existingNames))
	for _, name := range existingNames {
		existingSet[name] = true
	}

	desiredSet := make(map[string]bool, len(desired))

	var cs ChangeSet

	for _, unit := range desired {
		desiredSet[unit.Name] = true

		if !existingSet[unit.Name] {
			cs.Create = append(cs.Create, unit)
			continue
		}

		// Read existing content to check for updates
		existing, err := os.ReadFile(filepath.Join(systemdDir, unit.Name))
		if err != nil {
			// Can't read — treat as create
			cs.Create = append(cs.Create, unit)
			continue
		}

		if string(existing) != unit.Content {
			cs.Update = append(cs.Update, unit)
		} else {
			cs.Keep = append(cs.Keep, unit)
		}
	}

	for _, name := range existingNames {
		if !desiredSet[name] {
			cs.Remove = append(cs.Remove, Unit{Name: name})
		}
	}

	return cs
}

// UnitDir returns the path to the user systemd unit directory.
func (m *Manager) UnitDir() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: cannot determine user config directory: %v\n", err)
	}
	return filepath.Join(dir, "systemd", "user")
}

// WriteUnits creates a temporary directory, writes all unit files into it,
// and returns the directory path along with a cleanup function that removes
// the temporary directory. The caller must call cleanup when done (typically
// via defer). InstallUnits removes the directory on success, so cleanup
// becomes a no-op after a successful install.
func (m *Manager) WriteUnits(units []Unit) (tmpDir string, cleanup func(), err error) {
	systemdDir := m.UnitDir()
	if err := os.MkdirAll(systemdDir, 0755); err != nil {
		return "", nil, fmt.Errorf("creating systemd directory: %w", err)
	}
	tmpDir, err = os.MkdirTemp(systemdDir, ".orbit-staging-*")
	if err != nil {
		return "", nil, fmt.Errorf("creating temp directory: %w", err)
	}
	cleanup = func() { _ = os.RemoveAll(tmpDir) }
	for _, unit := range units {
		dest := filepath.Join(tmpDir, unit.Name)
		if err := os.WriteFile(dest, []byte(unit.Content), 0644); err != nil {
			cleanup()
			return "", nil, fmt.Errorf("writing unit file %s: %w", unit.Name, err)
		}
	}
	return tmpDir, cleanup, nil
}

// InstallUnits moves unit files from a staging directory (e.g. returned by
// WriteUnits) into the systemd user unit directory, performs a daemon-reload,
// and enables/starts any timer units. The staging directory is NOT removed
// — the caller's cleanup callback handles that.
func (m *Manager) InstallUnits(units []Unit, fromDir string) error {
	systemdDir := m.UnitDir()
	if err := os.MkdirAll(systemdDir, 0755); err != nil {
		return fmt.Errorf("creating systemd directory: %w", err)
	}

	for _, unit := range units {
		src := filepath.Join(fromDir, unit.Name)
		dst := filepath.Join(systemdDir, unit.Name)
		if err := os.Rename(src, dst); err != nil {
			return fmt.Errorf("installing unit file %s: %w", unit.Name, err)
		}
	}

	if err := m.daemonReload(); err != nil {
		return err
	}

	var timers []string
	for _, unit := range units {
		if strings.HasSuffix(unit.Name, ".timer") {
			timers = append(timers, unit.Name)
		}
	}
	if len(timers) > 0 {
		args := append([]string{"enable", "--now"}, timers...)
		output, err := m.systemctlOutput(args[0], args[1:]...)
		if err != nil {
			return fmt.Errorf("enabling timers: %w (output: %s)", err, output)
		}
	}

	return nil
}

// RemoveUnits stops, disables, and deletes the given units,
// then reloads the daemon once.
func (m *Manager) RemoveUnits(units []Unit) error {
	systemdDir := m.UnitDir()

	var allNames []string
	var timerNames []string
	for _, unit := range units {
		allNames = append(allNames, unit.Name)
		if strings.HasSuffix(unit.Name, ".timer") {
			timerNames = append(timerNames, unit.Name)
		}
	}

	if len(allNames) > 0 {
		m.systemctl("stop", allNames...)
	}
	if len(timerNames) > 0 {
		m.systemctl("disable", timerNames...)
	}

	for _, unit := range units {
		unitPath := filepath.Join(systemdDir, unit.Name)
		if err := os.Remove(unitPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("removing unit file %s: %w", unit.Name, err)
		}
	}

	return m.daemonReload()
}
