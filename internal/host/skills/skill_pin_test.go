package skills

import (
	"strconv"
	"strings"
	"testing"

	"github.com/FelixBaek/arkism/internal/host"
)

func vid(c string) string { return "skill-" + strings.Repeat(c, 64) }

// versionHub answers resolve for one skill: @latest-approved is *latest, @N is version N,
// whose version id is ids[N].
func versionHub(t *testing.T, latest *int, ids map[int]string) *hubFake {
	t.Helper()
	h := newHubFake(t)
	h.answer = func(p map[string]any) any {
		ref := asList(p["refs"])[0].(string)
		id, v, _ := strings.Cut(ref, "@")
		n := *latest
		if v != "latest-approved" {
			n, _ = strconv.Atoi(v)
		}
		body := "body v" + strconv.Itoa(n)
		return map[string]any{"items": []any{map[string]any{"ref": ref, "skill_id": id, "resolved_version": n, "version_id": ids[n],
			"content_hash": digest([]byte(body)), "instructions": body, "name": "x", "validation_status": "active"}}}
	}
	return h
}

func lastRef(t *testing.T, h *hubFake) string { return canon(t, h.payloads[len(h.payloads)-1]["refs"]) }

func hostPin(id, versionID string) host.SkillPin {
	return host.SkillPin{SkillID: id, VersionID: versionID}
}

// A mid-session approval does not swap the skill: the session keeps the version it pinned,
// even after the MCP server restarts, and refuses another version by name.
func TestLoadSkillKeepsTheSessionPin(t *testing.T) {
	swapRouter(t)
	t.Setenv("ARKISM_SESSION_ID", "sess")
	latest, ids := 2, map[int]string{2: vid("2"), 3: vid("3")}
	h := versionHub(t, &latest, ids)
	out, err := loadSkill("agent/x", "")
	if err != nil || field(out, "ref") != "agent/x@2" {
		t.Fatal(out, err)
	}
	if pin, ok := routerSessions().SkillPin("sess", "agent/x"); !ok || pin.Version != 2 || pin.VersionID != vid("2") {
		t.Fatal("pin not stored with the session", pin)
	}
	latest = 3                              // approved mid-session
	loadedSkills = map[string]loadedSkill{} // the MCP server restarted
	out, err = loadSkill("agent/x", "")
	if err != nil || field(out, "content") != "body v2" || lastRef(t, h) != `["agent/x@2"]` {
		t.Fatal(out, err, lastRef(t, h))
	}
	if _, err := loadSkill("agent/x@3", ""); err == nil || !strings.Contains(err.Error(), "skill_version_not_session_pinned") || !strings.Contains(err.Error(), "agent/x@2") {
		t.Fatal(err)
	}
	// The hub's answer for the pinned number no longer matches: refuse and name what it serves now.
	ids[2] = vid("9")
	if _, err := loadSkill("agent/x", ""); err == nil || !strings.Contains(err.Error(), "skill_changed_in_session") || !strings.Contains(err.Error(), vid("9")) {
		t.Fatal(err)
	}
	if _, ok := loadedSkills["agent/x@2"]; ok {
		t.Fatal("a refused load stays usable for usage records")
	}
	t.Setenv("ARKISM_SESSION_ID", "next")
	if out, err := loadSkill("agent/x", ""); err != nil || field(out, "content") != "body v3" {
		t.Fatal("a new session gets the new version", out, err)
	}
}

// A SessionStart pin knows only the version id: once a newer version is approved, a bare load
// serves the pinned one by its id.
func TestLoadSkillServesTheSessionStartVersion(t *testing.T) {
	swapRouter(t)
	t.Setenv("ARKISM_SESSION_ID", "sess")
	latest, ids := 2, map[int]string{1: vid("1"), 2: vid("2")}
	h := versionHub(t, &latest, ids)
	if _, err := routerSessions().Pin("sess", hostPin("agent/y", vid("1"))); err != nil {
		t.Fatal(err)
	}
	routerRequest = func(path string, _ any) (map[string]any, error) {
		switch {
		case strings.HasPrefix(path, "/v1/skill_versions/"):
			return j(t, `{"skill_id":"agent/y","id":"`+vid("1")+`","updated_at":"","analysis":{"version":"1","name":"y","description":"","source_path":""}}`), nil
		case strings.HasPrefix(path, "/v1/skills/"):
			return j(t, `{"version_history":["`+vid("1")+`","`+vid("2")+`"]}`), nil
		}
		t.Fatal(path)
		return nil, nil
	}
	out, err := loadSkill("agent/y", "")
	if err != nil || field(out, "content") != "body v1" || lastRef(t, h) != `["agent/y@1"]` {
		t.Fatal(out, err)
	}
}

// An oversized body is refused whole, never cut, and nothing is pinned or registered.
func TestLoadSkillRefusesABodyOverBudget(t *testing.T) {
	swapRouter(t)
	t.Setenv("ARKISM_SESSION_ID", "sess")
	latest := 1
	versionHub(t, &latest, map[int]string{1: vid("1")})
	skillMaxChars = len("body v1") - 1
	out, err := loadSkill("agent/z", "")
	if err == nil || out != nil || !strings.Contains(err.Error(), "skill_too_large") {
		t.Fatal(out, err)
	}
	if _, ok := routerSessions().SkillPin("sess", "agent/z"); ok || len(loadedSkills) != 0 {
		t.Fatal("refused skill was pinned or registered")
	}
	skillMaxChars = len("body v1")
	if _, err := loadSkill("agent/z", ""); err != nil {
		t.Fatal(err)
	}
}
