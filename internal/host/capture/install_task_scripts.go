package capture

import (
	"embed"
	"path/filepath"
)

// Explicit embed paths make a missing helper a compile error on every platform.
//
//go:embed install_tasks/arkism_scheduled_tasks.ps1 install_tasks/install_arkism_export_task.ps1 install_tasks/install_arkism_work_task.ps1
var instTaskPayload embed.FS

var instSupport = []string{"arkism_scheduled_tasks.ps1", "install_arkism_export_task.ps1", "install_arkism_work_task.ps1"}

func instWriteTaskScripts(s *instSaver, folder string) error {
	for _, name := range instSupport {
		raw, err := instTaskPayload.ReadFile("install_tasks/" + name)
		if err != nil {
			return err
		}
		if err := s.text(filepath.Join(folder, name), string(raw)); err != nil {
			return err
		}
	}
	return nil
}
