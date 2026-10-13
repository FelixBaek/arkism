package skills

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/FelixBaek/arkism/internal/host"
)

// load_skill rules:
//   - the body is never cut: a skill over the load budget is refused (a half workflow is
//     riskier than none);
//   - a session is pinned to the approved version it started with (the SessionStart hook pins
//     the listed skills; the first load pins any other). A mid-session approval does not swap
//     the skill: a bare id keeps serving the pinned version, and a request for another version,
//     or a hub answer that no longer matches the pin, is refused naming both versions.
// Pins live with the capture session state (host.SessionStore), not only in this process.

var (
	skillMaxChars  = envInt("ARKISM_SKILL_MAX_CHARS", 40000)
	routerSessions = host.DefaultSessionStore
)

// routerSessionKey is the agent session this MCP server serves: the id its hooks linked (or
// ARKISM_SESSION_ID), else this process's own thread.
func routerSessionKey() string {
	if sid := routerSessions().AgentSession(); sid != "" {
		return sid
	}
	return "mcp:" + sessionThread
}

// skillFits refuses a skill body over the load budget instead of truncating it.
func skillFits(item map[string]any) error {
	if n := host.Len(str(item["instructions"])); n > skillMaxChars {
		return fmt.Errorf("skill_too_large: %s@%s is %d chars, over the %d-char load budget (ARKISM_SKILL_MAX_CHARS); not loaded, a partial skill is riskier than none",
			pyStr(item["skill_id"]), pyStr(item["resolved_version"]), n, skillMaxChars)
	}
	return nil
}

// splitRef is "id" or "id@latest-approved" (number 0) or "id@N"; ok false for other forms.
func splitRef(ref string) (string, int, bool) {
	id, v, has := strings.Cut(ref, "@")
	if !has || v == "latest-approved" {
		return id, 0, id != ""
	}
	n, err := strconv.Atoi(v)
	return id, n, err == nil && n > 0 && id != ""
}

func pinRef(p host.SkillPin) string {
	if p.Version > 0 {
		return p.SkillID + "@" + strconv.Itoa(p.Version)
	}
	return p.SkillID + " (version_id " + p.VersionID + ")"
}

func servedPin(item map[string]any) host.SkillPin {
	n, _ := pyInt(item["resolved_version"])
	return host.SkillPin{SkillID: str(item["skill_id"]), VersionID: str(item["version_id"]), Version: int(n)}
}

func pinMatches(pin, served host.SkillPin) bool {
	return (pin.Version == 0 || pin.Version == served.Version) && (pin.VersionID == "" || pin.VersionID == served.VersionID)
}

func changedErr(pin, served host.SkillPin) error {
	return fmt.Errorf("skill_changed_in_session: this session is pinned to %s but the hub now serves %s@%d (version_id %s); not loaded. A new session picks up the new version",
		pinRef(pin), served.SkillID, served.Version, served.VersionID)
}

// loadedItem is the item loadSkillResolved registered for its answer.
func loadedItem(out host.Obj) (string, map[string]any) {
	ref, _ := out.Get("ref")
	r, _ := ref.(string)
	return r, loadedSkills[r].item
}

// loadSkill is load_skill: trials and raw version ids resolve as before (a raw id comes back
// through here as id@N); everything else follows the session pin.
func loadSkill(skillID, trialID string) (host.Obj, error) {
	if trialID != "" || versionIDRx.MatchString(skillID) {
		return loadSkillResolved(skillID, trialID)
	}
	id, number, ok := splitRef(skillID)
	if !ok {
		return loadSkillResolved(skillID, "") // the hub names the bad ref
	}
	store, sid := routerSessions(), routerSessionKey()
	pin, pinned := store.SkillPin(sid, id)
	if !pinned {
		out, err := loadSkillResolved(skillID, "")
		if err != nil {
			return nil, err
		}
		ref, item := loadedItem(out)
		served := servedPin(item)
		kept, err := store.Pin(sid, served)
		if err != nil {
			return out, nil // no pin store: still serve, as before pins existed
		}
		if !pinMatches(kept, served) { // another load of this session pinned first
			delete(loadedSkills, ref)
			return nil, changedErr(kept, served)
		}
		return out, nil
	}
	if number != 0 && pin.Version != 0 && number != pin.Version {
		return nil, fmt.Errorf("skill_version_not_session_pinned: this session is pinned to %s; requested %s@%d. A new session picks up other versions",
			pinRef(pin), id, number)
	}
	target := skillID
	if number == 0 && pin.Version != 0 {
		target = pinRef(pin)
	}
	out, err := loadSkillResolved(target, "")
	if err != nil {
		return nil, fmt.Errorf("skill_pinned_version_unavailable: this session is pinned to %s: %w", pinRef(pin), err)
	}
	ref, item := loadedItem(out)
	served := servedPin(item)
	if pinMatches(pin, served) {
		return out, nil
	}
	delete(loadedSkills, ref)
	if number == 0 && pin.Version == 0 && pin.VersionID != "" && versionIDRx.MatchString(pin.VersionID) {
		// Approved again since the session started: serve the pinned version by its id.
		out, err := loadSkillResolved(pin.VersionID, "")
		if err != nil {
			var pinErr error = changedErr(pin, served)
			return nil, errors.Join(pinErr, err)
		}
		return out, nil
	}
	return nil, changedErr(pin, served)
}
