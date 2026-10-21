package installtxn

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/sys/windows"
)

const uninstallRegistration = `Software\Microsoft\Windows\CurrentVersion\Uninstall\Arkism`

var releaseVersion = regexp.MustCompile(`^(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})$`)

type metadataScope struct {
	Live, SID, Registration string
	Links                   [2]string
}
type metadataCheckpoint struct {
	Format      int
	Scope       metadataScope
	ID, Version string
	Registry    registrySnapshot
	Links       [2]linkSnapshot
	NewLink     []byte
}
type nativeMetadata struct {
	scope      metadataScope
	version    string
	saved      *metadataCheckpoint
	checkpoint func(string) error
}

// NewMetadata binds registration to HKCU and shortcuts to the user's known
// folders. Arbitrary registry keys and shortcut paths are not public options.
func NewMetadata(live, version string) (Metadata, error) {
	layout, err := paths(live)
	if err != nil {
		return nil, err
	}
	folders, err := shortcutFolders()
	if err != nil {
		return nil, err
	}
	return scopedMetadata(metadataScope{live, layout.owner, uninstallRegistration, [2]string{filepath.Join(folders[0], "Arkism.lnk"), filepath.Join(folders[1], "Arkism.lnk")}}, version)
}
func shortcutFolders() (folders [2]string, err error) {
	for i, id := range []*windows.KNOWNFOLDERID{windows.FOLDERID_Programs, windows.FOLDERID_Desktop} {
		path, err := windows.KnownFolderPath(id, windows.KF_FLAG_DEFAULT)
		if err != nil {
			return folders, err
		}
		folders[i], err = resolveKnownDirectory(path)
		if err != nil {
			return folders, err
		}
	}
	return folders, nil
}

// CheckShortcutFolders only compares Windows' resolved known folders. It never
// redirects folders or changes the user's registry. Hosted installation checks
// must call this before starting an installer with an isolated child profile.
func CheckShortcutFolders(programs, desktop string) error {
	actual, err := shortcutFolders()
	if err != nil {
		return err
	}
	for i, expected := range []string{programs, desktop} {
		if !filepath.IsAbs(expected) {
			return errors.New("install_shortcut_expected_path_invalid")
		}
		resolved, err := resolveKnownDirectory(expected)
		if err != nil {
			return err
		}
		if !strings.EqualFold(actual[i], resolved) {
			return errors.New("install_shortcut_folder_not_isolated")
		}
	}
	return nil
}

func scopedMetadata(scope metadataScope, version string) (*nativeMetadata, error) {
	if !releaseVersion.MatchString(version) {
		return nil, errors.New("install_metadata_version_invalid")
	}
	layout, err := paths(scope.Live)
	if err != nil {
		return nil, err
	}
	if scope.SID != layout.owner {
		return nil, errors.New("install_metadata_owner_invalid")
	}
	if strings.EqualFold(scope.Links[0], scope.Links[1]) {
		return nil, errors.New("install_shortcut_scope_overlap")
	}
	for _, link := range scope.Links {
		if !filepath.IsAbs(link) || filepath.Clean(link) != link || filepath.Base(link) != "Arkism.lnk" {
			return nil, errors.New("install_shortcut_path_invalid")
		}
		parent := strings.ToLower(link)
		for _, destination := range []string{strings.ToLower(layout.live), strings.ToLower(layout.versions)} {
			if parent == destination || strings.HasPrefix(parent, destination+`\`) || strings.HasPrefix(destination, parent+`\`) {
				return nil, errors.New("install_shortcut_scope_overlap")
			}
		}
	}
	return &nativeMetadata{scope: scope, version: version}, nil
}
func (m *nativeMetadata) point(name string) error {
	if m.checkpoint != nil {
		return m.checkpoint(name)
	}
	return nil
}
func (m *nativeMetadata) pin() (func(), error) {
	var releases []func()
	release := func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
	}
	for _, link := range m.scope.Links {
		close, err := pinMetadataDirectory(filepath.Dir(link))
		if err != nil {
			release()
			return nil, err
		}
		releases = append(releases, close)
	}
	return release, nil
}
func readOldLinks(scope metadataScope) (result [2]linkSnapshot, err error) {
	for i, path := range scope.Links {
		file, err := openLink(path)
		if err != nil {
			return result, err
		}
		result[i] = snapshotOf(file)
		file.close()
		if result[i].Exists {
			target, err := inspectShortcut(result[i].Data)
			if err != nil {
				return result, err
			}
			if !strings.EqualFold(target.Path, filepath.Join(scope.Live, "Arkism.exe")) || target.Arguments != "" {
				return result, errors.New("install_shortcut_not_owned")
			}
		}
	}
	return result, nil
}
func (m *nativeMetadata) Capture() ([]byte, error) {
	release, err := m.pin()
	if err != nil {
		return nil, err
	}
	defer release()
	registration, err := readRegistration(m.scope.Registration)
	if err != nil {
		return nil, err
	}
	if registration.Exists && !ownsRegistration(registration, m.scope.Live) {
		return nil, errors.New("install_registration_not_owned")
	}
	if err = validateCurrentInstall(m.scope.Live, registration.Exists); err != nil {
		return nil, err
	}
	links, err := readOldLinks(m.scope)
	if err != nil {
		return nil, err
	}
	data, err := newShortcut(filepath.Join(m.scope.Live, "Arkism.exe"))
	if err != nil {
		return nil, err
	}
	id := make([]byte, 16)
	if _, err = rand.Read(id); err != nil {
		return nil, err
	}
	snapshot := metadataCheckpoint{1, m.scope, hex.EncodeToString(id), m.version, registration, links, data}
	if err = m.validate(snapshot); err != nil {
		return nil, err
	}
	m.saved = &snapshot
	return json.Marshal(snapshot)
}
func validateCurrentInstall(live string, registered bool) error {
	exists, err := directoryExists(live)
	if err != nil || !exists {
		return err
	}
	if !registered {
		entries, err := os.ReadDir(live)
		if err != nil {
			return err
		}
		if len(entries) == 0 {
			return nil
		}
		return errors.New("install_existing_payload_not_registered")
	}
	for _, relative := range []string{"Arkism.exe", `integration\arkism.exe`, `runtime\memoryd.exe`, `runtime\memory-principal.exe`, "uninstall.exe"} {
		if err = ordinary(filepath.Join(live, relative), false); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}
func (m *nativeMetadata) validate(snapshot metadataCheckpoint) error {
	if snapshot.Format != 1 || snapshot.Scope != m.scope || !releaseVersion.MatchString(snapshot.Version) || len(snapshot.ID) != 32 {
		return errors.New("install_metadata_checkpoint_scope_invalid")
	}
	id, err := hex.DecodeString(snapshot.ID)
	if err != nil || len(id) != 16 || strings.ToLower(snapshot.ID) != snapshot.ID {
		return errors.New("install_metadata_checkpoint_id_invalid")
	}
	if err := validateRegistrySnapshot(snapshot.Registry, m.scope.Live); err != nil {
		return err
	}
	target := filepath.Join(m.scope.Live, "Arkism.exe")
	for _, link := range snapshot.Links {
		if err := validateOldLink(link, target); err != nil {
			return err
		}
	}
	parsed, err := inspectShortcut(snapshot.NewLink)
	if err != nil {
		return err
	}
	if !strings.EqualFold(parsed.Path, target) || parsed.Arguments != "" || parsed.Flags&fixedShortcutFlags != fixedShortcutFlags {
		return errors.New("install_metadata_target_link_invalid")
	}
	return nil
}
func (m *nativeMetadata) decode(data []byte) (metadataCheckpoint, error) {
	var snapshot metadataCheckpoint
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if len(data) > 1<<20 {
		return snapshot, errors.New("install_metadata_checkpoint_too_large")
	}
	if err := decoder.Decode(&snapshot); err != nil {
		return snapshot, err
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return snapshot, errors.New("install_metadata_checkpoint_trailing_data")
	}
	return snapshot, m.validate(snapshot)
}
func (m *nativeMetadata) slots(snapshot metadataCheckpoint) (result [2]linkSlots, err error) {
	defer func() {
		if err != nil {
			for i := range result {
				result[i].close()
			}
		}
	}()
	for i, path := range m.scope.Links {
		result[i], err = openLinkSlots(path, snapshot.ID, i)
		if err != nil {
			return result, err
		}
	}
	return result, nil
}
func closeSlots(slots *[2]linkSlots) {
	for i := range slots {
		slots[i].close()
	}
}
func (m *nativeMetadata) Apply() error {
	if m.saved == nil {
		return errors.New("install_metadata_capture_required")
	}
	snapshot := *m.saved
	release, err := m.pin()
	if err != nil {
		return err
	}
	defer release()
	slots, err := m.slots(snapshot)
	if err != nil {
		return err
	}
	defer closeSlots(&slots)
	for i := range slots {
		if !matchesLink(slots[i].target, snapshot.Links[i]) || slots[i].backup != nil || slots[i].pending != nil {
			return errors.New("install_shortcut_changed_before_apply")
		}
	}
	for i, path := range m.scope.Links {
		_, pending := linkTemporaryPaths(path, snapshot.ID, i)
		slots[i].pending, err = newLinkFile(pending, snapshot.NewLink)
		if err != nil {
			return err
		}
	}
	if err = mutateRegistration(m.scope.Registration, snapshot.Registry, expectedRegistration(m.scope.Live, snapshot.Version), false, m.point); err != nil {
		return err
	}
	if err = m.point("registration_applied"); err != nil {
		return err
	}
	for i, path := range m.scope.Links {
		if err = m.publishLink(&slots[i], snapshot, path, i); err != nil {
			return err
		}
	}
	return nil
}
func (m *nativeMetadata) publishLink(slots *linkSlots, snapshot metadataCheckpoint, path string, index int) error {
	backup, _ := linkTemporaryPaths(path, snapshot.ID, index)
	if slots.target != nil {
		if err := slots.target.rename(backup); err != nil {
			return err
		}
		slots.backup = slots.target
		slots.target = nil
	}
	if err := m.point(fmt.Sprintf("link_%d_old_moved", index)); err != nil {
		return err
	}
	if err := slots.pending.rename(path); err != nil {
		return err
	}
	slots.target = slots.pending
	slots.pending = nil
	if err := slots.target.makeVisible(); err != nil {
		return err
	}
	return m.point(fmt.Sprintf("link_%d_published", index))
}
func (m *nativeMetadata) Verify() error {
	if m.saved == nil {
		return errors.New("install_metadata_capture_required")
	}
	snapshot := *m.saved
	release, err := m.pin()
	if err != nil {
		return err
	}
	defer release()
	current, err := readRegistration(m.scope.Registration)
	if err != nil {
		return err
	}
	expected := registrySnapshot{true, copyRegistryValues(snapshot.Registry.Values)}
	for name, value := range expectedRegistration(m.scope.Live, snapshot.Version) {
		expected.Values[name] = value
	}
	if !sameRegistry(current, expected) {
		return errors.New("install_registration_verification_failed")
	}
	slots, err := m.slots(snapshot)
	if err != nil {
		return err
	}
	defer closeSlots(&slots)
	for i := range slots {
		if !matchesLink(slots[i].target, linkSnapshot{true, snapshot.NewLink}) || !matchesLink(slots[i].backup, snapshot.Links[i]) || slots[i].pending != nil {
			return errors.New("install_shortcut_verification_failed")
		}
	}
	return nil
}
func copyRegistryValues(values map[string]registryValue) map[string]registryValue {
	result := map[string]registryValue{}
	for name, value := range values {
		result[name] = value
	}
	return result
}
func (m *nativeMetadata) Restore(data []byte) error {
	snapshot, err := m.decode(data)
	if err != nil {
		return err
	}
	release, err := m.pin()
	if err != nil {
		return err
	}
	defer release()
	slots, err := m.slots(snapshot)
	if err != nil {
		return err
	}
	defer closeSlots(&slots)
	current, err := readRegistration(m.scope.Registration)
	if err != nil {
		return err
	}
	target := expectedRegistration(m.scope.Live, snapshot.Version)
	if !registryRecoveryMatches(current, snapshot.Registry, target) {
		return errors.New("install_registration_foreign_change")
	}
	for i := range slots {
		if err = slots[i].validateRestore(snapshot.Links[i], snapshot.NewLink); err != nil {
			return err
		}
	}
	for i := len(slots) - 1; i >= 0; i-- {
		if err = slots[i].restore(m.scope.Links[i], snapshot.Links[i]); err != nil {
			return err
		}
	}
	return mutateRegistration(m.scope.Registration, snapshot.Registry, target, true, nil)
}

// Commit removes only checkpoint-bound temporary links. It deliberately does
// not inspect or reset the current shortcuts, which the user may edit after commit.
func (m *nativeMetadata) Commit(data []byte) error {
	snapshot, err := m.decode(data)
	if err != nil {
		return err
	}
	release, err := m.pin()
	if err != nil {
		return err
	}
	defer release()
	for i, path := range m.scope.Links {
		slots, err := openTemporaryLinkSlots(path, snapshot.ID, i)
		if err != nil {
			return err
		}
		err = commitLinkSlots(&slots, snapshot.Links[i], snapshot.NewLink)
		slots.close()
		if err != nil {
			return err
		}
	}
	return nil
}
func commitLinkSlots(slots *linkSlots, old linkSnapshot, newData []byte) error {
	if slots.backup != nil && !matchesLink(slots.backup, old) {
		return errors.New("install_metadata_foreign_backup")
	}
	if slots.pending != nil && !bytes.HasPrefix(newData, slots.pending.data) {
		return errors.New("install_metadata_foreign_new_file")
	}
	if err := slots.backup.remove(); err != nil {
		return err
	}
	slots.backup = nil
	if err := slots.pending.remove(); err != nil {
		return err
	}
	slots.pending = nil
	return nil
}

var _ Metadata = (*nativeMetadata)(nil)

func validateRegistrySnapshot(snapshot registrySnapshot, live string) error {
	if snapshot.Values == nil || len(snapshot.Values) > 256 || !snapshot.Exists && len(snapshot.Values) != 0 {
		return errors.New("install_metadata_registry_snapshot_invalid")
	}
	total := 0
	for name, value := range snapshot.Values {
		if normalizeRegistryName(name) != name || len(value.Data) > 64<<10 {
			return errors.New("install_metadata_registry_value_invalid")
		}
		total += len(value.Data)
	}
	if total > 256<<10 {
		return errors.New("install_metadata_registry_snapshot_too_large")
	}
	if snapshot.Exists && !ownsRegistration(snapshot, live) {
		return errors.New("install_metadata_original_registration_not_owned")
	}
	return nil
}
func validateOldLink(link linkSnapshot, target string) error {
	if !link.Exists {
		if len(link.Data) != 0 {
			return errors.New("install_metadata_link_snapshot_invalid")
		}
		return nil
	}
	parsed, err := inspectShortcut(link.Data)
	if err != nil {
		return err
	}
	if !strings.EqualFold(parsed.Path, target) || parsed.Arguments != "" {
		return errors.New("install_metadata_original_link_not_owned")
	}
	return nil
}
