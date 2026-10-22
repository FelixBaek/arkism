package skills

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"

	"github.com/FelixBaek/arkism/internal/host"
)

const projectUsage = `Deploy hub-approved skill versions into runtime skill folders.

Codex and Claude Code only load skills from their own folders. Instead of keeping
originals there (or symlinks), <skill tree>/projection.json (ARKISM_SKILLS_ROOT) lists which
skills each folder needs; this writes the latest approved version exactly as the
hub pinned it (SKILL.md plus stored companion files) and leaves a .arkism.json
sidecar. Hash-only companions (binary/oversized) are copied from the source folder
only while their bytes still match the pin; the local folder is never the version.

Never silent: a version without pinned companions keeps the previous copy; a copy
that was edited in place, or a folder this tool did not write, is not overwritten.
All are reported.

  arkism project-skills [--check]
`

func init() { host.Register("project-skills", projectMain) }

var projectSource = host.Env("SKILL_SOURCE_ROOT", host.SkillsRoot)

func pluginSkill(src string) bool {
	for d := src; ; d = filepath.Dir(d) {
		if d == projectSource || isUnder(d, projectSource) {
			if isDir(filepath.Join(d, ".claude-plugin")) {
				return true
			}
		}
		if filepath.Dir(d) == d {
			return false
		}
	}
}

func readSidecar(dst string) (map[string]any, error) {
	raw, err := os.ReadFile(filepath.Join(dst, ".arkism.json"))
	if err != nil {
		return nil, err
	}
	v, err := host.Loads(string(raw))
	return asMap(v), err
}

func managedUnedited(dst string) (bool, error) {
	if isLink(dst) || !exists(filepath.Join(dst, ".arkism.json")) {
		return false, nil
	}
	meta, err := readSidecar(dst)
	if err != nil {
		return false, err
	}
	h, err := treeHash(dst)
	return err == nil && h == meta["tree_hash"], err
}

// resolveAll maps skill_id to the approved item or the hub's per-skill refusal code.
func resolveAll(ids []string) (map[string]any, error) {
	out := map[string]any{}
	for _, id := range ids {
		item, err := reviewResolve(id + "@latest-approved")
		var hubE *host.SkillHubError
		if errors.As(err, &hubE) && hubE.Code != "hub_error" { // hub_error is a transport/auth failure: no answer about this skill
			out[id] = hubE.Code
		} else if err != nil {
			return nil, err
		} else {
			out[id] = item
		}
	}
	return out, nil
}

// projectOne returns the report line, or "" when the copy is already current.
func projectOne(target, name, skillID string, check bool, approved any, mayRemove bool) (string, error) {
	src, dst := filepath.Join(projectSource, skillID), filepath.Join(target, name)
	if approved == nil {
		answers, err := resolveAll([]string{skillID})
		if err != nil {
			return "", err
		}
		approved = answers[skillID]
	}
	if code, refused := approved.(string); refused {
		return projectOneRefused(dst, code, check, mayRemove)
	}
	item := asMap(approved)
	ref := skillID + "@" + pyStr(item["resolved_version"])
	if _, ok := item["bundle"]; !ok {
		return fmt.Sprintf("%s: %s에 고정된 보조 파일이 없어 이전 사본 유지 (동기화 후 다시 시도)", dst, ref), nil
	}
	if pluginSkill(src) {
		// The bundle root is the plugin root; a single skill folder cannot hold that layout.
		return fmt.Sprintf("%s: 플러그인 스킬은 폴더 배포를 지원하지 않음 — 덮어쓰지 않음", dst), nil
	}
	if exists(filepath.Join(dst, ".arkism.json")) && !isLink(dst) {
		meta, err := readSidecar(dst)
		if err != nil {
			return "", err
		}
		h, err := treeHash(dst)
		if err != nil {
			return "", err
		}
		if h != meta["tree_hash"] {
			return fmt.Sprintf("%s: 사본이 직접 수정됨 — 덮어쓰지 않음", dst), nil
		}
		if sameJSON(meta["version_id"], item["version_id"]) && sameJSON(meta["bundle_sha256"], asMap(item["bundle"])["sha256"]) {
			return "", nil
		}
	} else if exists(dst) && !isLink(dst) {
		// A folder without the sidecar is not a copy this tool made (user or plugin skill): never delete it.
		return fmt.Sprintf("%s: 관리 표시(.arkism.json) 없는 기존 폴더 — 덮어쓰지 않음", dst), nil
	}
	if check {
		return fmt.Sprintf("%s: 배포 예정 (%s)", dst, ref), nil
	}
	if _, err := materialize(item, dst, src, host.Obj{{Key: "source", Value: src}, {Key: "note", Value: "배포된 사본이다. 고치려면 source를 고친다."}}); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s: 배포 %s", dst, ref), nil
}

// projectOneRefused removes an unedited projected copy of a skill the hub refused.
func projectOneRefused(dst, code string, check, mayRemove bool) (string, error) {
	// The hub refused this skill for this token and scope (deleted, quarantined, approval or
	// access revoked): an unedited projected copy is derived content and must not outlive it.
	if !exists(dst) {
		return fmt.Sprintf("%s: 서버 응답 %s — 사본 없음", dst, code), nil
	}
	ok, err := managedUnedited(dst)
	if err != nil {
		return "", err
	}
	switch {
	case !ok:
		return fmt.Sprintf("%s: 서버 응답 %s, 직접 수정됐거나 관리 사본이 아님 — 지우지 않음", dst, code), nil
	case !mayRemove:
		return fmt.Sprintf("%s: 서버 응답 %s, 모든 스킬이 거절되어 설정 오류로 보고 지우지 않음", dst, code), nil
	case check:
		return fmt.Sprintf("%s: 서버 응답 %s — 삭제 예정", dst, code), nil
	}
	if err := os.RemoveAll(dst); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s: 서버 응답 %s — 배포 사본 삭제", dst, code), nil
}

func projectMain(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if slices.Contains(args, "-h") || slices.Contains(args, "--help") {
		io.WriteString(stdout, projectUsage)
		return 0
	}
	p := newArgs("project-skills", stderr)
	check := p.flag("--check")
	if !p.parse(args, 0, 0) {
		return 2
	}
	raw, err := os.ReadFile(filepath.Join(projectSource, "projection.json"))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	v, err := loadsOrdered(string(raw))
	config, _ := v.(host.Obj)
	targets, _ := objGet(config, "targets").(host.Obj)
	if err != nil || targets == nil {
		fmt.Fprintln(stderr, "projection.json: targets missing or invalid")
		return 1
	}
	ids := map[string]bool{}
	for _, t := range targets {
		for _, kv := range asObj(t.Value) {
			ids[pyStr(kv.Value)] = true
		}
	}
	answers, err := resolveAll(sortedKeys(ids))
	if err != nil {
		fmt.Fprintf(stdout, "허브 조회 실패 — 아무것도 바꾸지 않음: %v\n", err)
		return 1
	}
	// If the hub refused every skill, the token/scope is more likely wrong than everything deleted.
	mayRemove := false
	for _, a := range answers {
		if _, refused := a.(string); !refused {
			mayRemove = true
		}
	}
	for _, t := range targets {
		for _, kv := range asObj(t.Value) {
			id := pyStr(kv.Value)
			msg, err := projectOne(t.Key, kv.Key, id, *check, answers[id], mayRemove)
			if err != nil {
				msg = fmt.Sprintf("%s/%s: 실패 — %v", t.Key, kv.Key, err)
			}
			if msg != "" {
				fmt.Fprintln(stdout, msg)
			}
		}
	}
	return 0
}

func asObj(v any) host.Obj { o, _ := v.(host.Obj); return o }
