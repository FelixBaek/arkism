package skills

// skill_install: a skill from a GitHub repository, applied by the host like every agent_tools request.
// A request that is not enabled is a preview (what would be installed, at which commit); an enabled one
// installs the same commit into the chosen agents' skill folders. No git binary: the repository tarball
// at a pinned commit, read in memory, with size, file-count, link and path checks. Nothing is run.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

const (
	skillMaxBytes   = 5 << 20  // what one install writes
	skillMaxFiles   = 200      // files one install writes
	skillMaxSkills  = 10       // skill folders one source may hold
	tarballMaxBytes = 60 << 20 // the download itself (compressed)
	repoMaxBytes    = 80 << 20 // files under the chosen path, read in memory before choosing the skills
	repoMaxFiles    = 20000
	skillSourceFile = ".arkism-source.json"
)

var (
	// The skill folders skill_install may write; ~ is the agents' home.
	installTargets = map[string]string{"claude_code": "~/.claude/skills", "codex": "~/.codex/skills", "agents": "~/.agents/skills", "zcode": "~/.zcode/skills"}
	installOrder   = []string{"claude_code", "codex", "agents", "zcode"}

	githubAPI = "https://api.github.com"
	// Test seams: the GitHub download, the skill listing started after an install, the clock.
	githubFetch    = fetchGitHub
	startSkillSync = func() {
		startDetached([]string{host.Executable(), "sync-skills", "--execute", "--limit", "5000"})
	}
	installNow = func() time.Time { return time.Now().UTC() }

	ghSegRx      = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}$`)
	ghOwnerRx    = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,39}$`)
	ghRefRx      = regexp.MustCompile(`^[A-Za-z0-9_./-]{1,100}$`)
	shaRx        = regexp.MustCompile(`^[0-9a-f]{40}$`)
	skillNameRx  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	errSkillLink = runtimeError{"링크(심볼릭·하드 링크)가 들어 있어서 설치하지 않아요"}
)

type skillSource struct{ owner, repo, path, ref string }

func (s skillSource) String() string {
	out := s.owner + "/" + s.repo
	if s.path != "" {
		out += "/" + s.path
	}
	if s.ref != "" {
		out += "@" + s.ref
	}
	return out
}

// parseSkillSource reads owner/repo[/path][@ref] or a github.com address (…/tree/<ref>/<path>, …/blob/<ref>/<path>/SKILL.md).
func parseSkillSource(raw string) (skillSource, error) {
	bad := runtimeError{"GitHub 주소를 owner/repo 또는 owner/repo/경로 형태로 넣어 주세요"}
	s := strings.TrimSpace(raw)
	var ref string
	for _, p := range []string{"https://", "http://"} {
		s = strings.TrimPrefix(s, p)
	}
	url := strings.HasPrefix(s, "github.com/") || strings.HasPrefix(s, "www.github.com/")
	if url {
		s = s[strings.Index(s, "/")+1:]
		s = strings.SplitN(strings.SplitN(s, "?", 2)[0], "#", 2)[0]
	} else if at := strings.LastIndex(s, "@"); at >= 0 {
		s, ref = s[:at], s[at+1:]
		if !ghRefRx.MatchString(ref) || strings.Contains(ref, "..") {
			return skillSource{}, bad
		}
	}
	parts := strings.Split(strings.Trim(s, "/"), "/")
	if len(parts) < 2 {
		return skillSource{}, bad
	}
	src := skillSource{owner: parts[0], repo: strings.TrimSuffix(parts[1], ".git"), ref: ref}
	rest := parts[2:]
	if url && len(rest) >= 2 && (rest[0] == "tree" || rest[0] == "blob") {
		src.ref, rest = rest[1], rest[2:]
		if len(rest) > 0 && rest[len(rest)-1] == "SKILL.md" {
			rest = rest[:len(rest)-1]
		}
	} else if url && len(rest) > 0 {
		return skillSource{}, bad
	}
	if !ghOwnerRx.MatchString(src.owner) || !ghSegRx.MatchString(src.repo) || src.ref != "" && !ghRefRx.MatchString(src.ref) {
		return skillSource{}, bad
	}
	for _, seg := range append([]string{src.owner, src.repo}, rest...) {
		if seg == "" || seg == "." || seg == ".." || !ghSegRx.MatchString(seg) {
			return skillSource{}, bad
		}
	}
	src.path = strings.Join(rest, "/")
	return src, nil
}

func fetchGitHub(url, accept string, max int64) ([]byte, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", "arkism-skill-install")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	res, err := (&http.Client{Timeout: 90 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode == 404 {
		return nil, runtimeError{"GitHub에서 찾지 못했어요 (공개 저장소만 설치할 수 있어요)"}
	}
	if res.StatusCode != 200 {
		return nil, runtimeError{fmt.Sprintf("GitHub이 HTTP %d로 답했어요", res.StatusCode)}
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > max {
		return nil, runtimeError{"저장소가 너무 커서 받지 않았어요"}
	}
	return raw, nil
}

// resolveCommit pins the ref (the default branch when none) to one commit.
func resolveCommit(src skillSource) (string, error) {
	if shaRx.MatchString(src.ref) {
		return src.ref, nil
	}
	base := githubAPI + "/repos/" + src.owner + "/" + src.repo
	ref := src.ref
	if ref == "" {
		raw, err := githubFetch(base, "application/vnd.github+json", 1<<20)
		if err != nil {
			return "", err
		}
		v, err := host.Loads(string(raw))
		if err != nil {
			return "", err
		}
		if ref = str(asMap(v)["default_branch"]); !ghRefRx.MatchString(ref) {
			return "", runtimeError{"기본 브랜치를 알 수 없어요"}
		}
	}
	raw, err := githubFetch(base+"/commits/"+ref, "application/vnd.github.sha", 4096)
	if err != nil {
		return "", err
	}
	sha := strings.TrimSpace(string(raw))
	if !shaRx.MatchString(sha) {
		return "", runtimeError{"커밋을 알 수 없어요"}
	}
	return sha, nil
}

type repoFile struct {
	data []byte
	exec bool
}

// readTarball keeps the regular files under dir (repository-relative). A link or an escaping
// path under dir refuses the whole source; anything outside dir is skipped unread.
func readTarball(raw []byte, dir string) (map[string]repoFile, error) {
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	files, total := map[string]repoFile{}, 0
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		_, rel, _ := strings.Cut(h.Name, "/") // GitHub's top folder: owner-repo-sha/
		rel = strings.TrimSuffix(rel, "/")
		if rel == "" {
			continue
		}
		if dir != "" && rel != dir && !strings.HasPrefix(rel, dir+"/") {
			continue
		}
		if strings.HasPrefix(rel, "/") || strings.Contains(rel, "\\") || path.Clean(rel) != rel || slicesHas(strings.Split(rel, "/"), "..") {
			return nil, runtimeError{"저장소 밖을 가리키는 경로가 있어서 설치하지 않아요"}
		}
		switch h.Typeflag {
		case tar.TypeDir:
			continue
		case tar.TypeSymlink, tar.TypeLink:
			return nil, errSkillLink
		case tar.TypeReg:
		default:
			return nil, runtimeError{"일반 파일이 아닌 항목이 있어서 설치하지 않아요"}
		}
		if total += int(h.Size); total > repoMaxBytes || len(files) >= repoMaxFiles {
			return nil, runtimeError{"이 경로가 너무 커요. 스킬 폴더 경로까지 넣어 주세요"}
		}
		data, err := io.ReadAll(io.LimitReader(tr, h.Size+1))
		if err != nil {
			return nil, err
		}
		files[rel] = repoFile{data: data, exec: h.Mode&0o111 != 0}
	}
	return files, nil
}

func slicesHas(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

type foundSkill struct {
	dir, name, desc string
	files           []string
	bytes           int
}

// findSkills: each outermost folder holding a SKILL.md, with the files under it.
func findSkills(files map[string]repoFile, repo string) ([]foundSkill, error) {
	var dirs []string
	for rel := range files {
		if path.Base(rel) == "SKILL.md" {
			dirs = append(dirs, path.Dir(rel))
		}
	}
	sort.Strings(dirs)
	var outer []string
	for _, d := range dirs {
		if d == "." {
			d = ""
		}
		nested := false
		for _, o := range outer {
			if o == "" || strings.HasPrefix(d, o+"/") {
				nested = true
			}
		}
		if !nested {
			outer = append(outer, d)
		}
	}
	if len(outer) == 0 {
		return nil, runtimeError{"이 경로에서 SKILL.md를 찾지 못했어요"}
	}
	if len(outer) > skillMaxSkills {
		return nil, runtimeError{fmt.Sprintf("스킬이 %d개나 있어요. 설치할 스킬 폴더 경로를 넣어 주세요", len(outer))}
	}
	var out []foundSkill
	seen := map[string]bool{}
	for _, d := range outer {
		meta := frontmatter(decodeReplace(files[path.Join(d, "SKILL.md")].data))
		name := meta["name"]
		if !skillNameRx.MatchString(name) {
			name = path.Base(d)
			if d == "" {
				name = repo
			}
		}
		if !skillNameRx.MatchString(name) || seen[name] {
			return nil, runtimeError{"스킬 이름이 올바르지 않거나 겹쳐요: " + runeCut(name, 64)}
		}
		seen[name] = true
		sk := foundSkill{dir: d, name: name, desc: runeCut(pyStrip(meta["description"]), 200)}
		for rel, f := range files {
			if d == "" || strings.HasPrefix(rel, d+"/") {
				sk.files = append(sk.files, rel)
				sk.bytes += len(f.data)
			}
		}
		sort.Strings(sk.files)
		out = append(out, sk)
	}
	return out, nil
}

// loadSkillSource downloads and checks one source: the pinned commit and its skills, within the limits.
func loadSkillSource(src skillSource) (string, []foundSkill, map[string]repoFile, error) {
	commit, err := resolveCommit(src)
	if err != nil {
		return "", nil, nil, err
	}
	raw, err := githubFetch(githubAPI+"/repos/"+src.owner+"/"+src.repo+"/tarball/"+commit, "application/vnd.github+json", tarballMaxBytes)
	if err != nil {
		return "", nil, nil, err
	}
	files, err := readTarball(raw, src.path)
	if err != nil {
		return "", nil, nil, err
	}
	skills, err := findSkills(files, src.repo)
	if err != nil {
		return "", nil, nil, err
	}
	n, size := 0, 0
	for _, sk := range skills {
		n, size = n+len(sk.files), size+sk.bytes
	}
	if n > skillMaxFiles || size > skillMaxBytes {
		return "", nil, nil, runtimeError{fmt.Sprintf("파일 %d개 · %.1fMB예요. 한 번에 파일 %d개, 5MB까지만 설치해요", n, float64(size)/(1<<20), skillMaxFiles)}
	}
	return commit, skills, files, nil
}

// installDir is one target's skill folder, or "" for an unknown target.
func installDir(target string) string {
	d, ok := installTargets[target]
	if !ok {
		return ""
	}
	return expand(d)
}

func previewDetail(src skillSource, commit string, skills []foundSkill) string {
	list := []any{}
	for _, sk := range skills {
		var have []any
		for _, t := range installOrder {
			if _, err := os.Lstat(filepath.Join(installDir(t), sk.name)); err == nil {
				have = append(have, t)
			}
		}
		row := host.Obj{{Key: "name", Value: sk.name}, {Key: "description", Value: sk.desc}, {Key: "dir", Value: sk.dir},
			{Key: "files", Value: len(sk.files)}, {Key: "bytes", Value: sk.bytes}}
		if have != nil {
			row.Set("installed", have)
		}
		list = append(list, row)
	}
	src.ref = ""
	out := host.Obj{{Key: "source", Value: src.String()}, {Key: "commit", Value: commit}, {Key: "skills", Value: list}}
	text := host.MustDumps(out, host.DumpOpts{Compact: true, NoASCII: true})
	if len(text) > 8000 { // descriptions are the only open-ended part
		for i, r := range list {
			row := r.(host.Obj)
			row.Set("description", "")
			list[i] = row
		}
		out.Set("skills", list)
		text = host.MustDumps(out, host.DumpOpts{Compact: true, NoASCII: true})
	}
	return text
}

// writeSkill writes one skill into a fresh folder beside dest, then moves it into place;
// with replace an existing dest is swapped out and removed only after the new one is in.
func writeSkill(sk foundSkill, files map[string]repoFile, src skillSource, commit, dest string, replace bool) error {
	parent := filepath.Dir(dest)
	tmp, err := os.MkdirTemp(parent, "."+sk.name+".arkism-new-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err := os.Chmod(tmp, 0o755); err != nil {
		return err
	}
	for _, rel := range sk.files {
		inner := rel
		if sk.dir != "" {
			inner = strings.TrimPrefix(rel, sk.dir+"/")
		}
		if inner == skillSourceFile {
			continue
		}
		p := filepath.Join(tmp, filepath.FromSlash(inner))
		if !strings.HasPrefix(p, tmp+string(filepath.Separator)) {
			return runtimeError{"저장소 밖을 가리키는 경로가 있어서 설치하지 않아요"}
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if files[rel].exec {
			mode = 0o755
		}
		if err := os.WriteFile(p, files[rel].data, mode); err != nil {
			return err
		}
		if err := os.Chmod(p, mode); err != nil { // umask may have taken bits; never more than these
			return err
		}
	}
	meta := host.Obj{{Key: "repo", Value: src.owner + "/" + src.repo}, {Key: "path", Value: sk.dir}, {Key: "commit", Value: commit},
		{Key: "installed_at", Value: installNow().Format(time.RFC3339)}}
	if err := os.WriteFile(filepath.Join(tmp, skillSourceFile), []byte(host.MustDumps(meta, host.DumpOpts{})+"\n"), 0o644); err != nil {
		return err
	}
	if replace {
		old := tmp + "-old"
		if err := os.Rename(dest, old); err != nil {
			return err
		}
		if err := os.Rename(tmp, dest); err != nil {
			os.Rename(old, dest)
			return err
		}
		return os.RemoveAll(old)
	}
	return os.Rename(tmp, dest)
}

// installSkills writes the skills into each target; every target is checked before anything is written.
func installSkills(src skillSource, targets []string, update bool) (string, error) {
	if !shaRx.MatchString(src.ref) {
		return "", runtimeError{"미리보기에서 확인한 커밋이 없어요. 다시 미리보기해 주세요"}
	}
	if len(targets) == 0 {
		return "", runtimeError{"넣을 에이전트를 골라 주세요"}
	}
	type job struct{ target, dir string }
	var jobs []job
	real := map[string]bool{}
	for _, t := range targets {
		d := installDir(t)
		if d == "" {
			return "", runtimeError{"알 수 없는 스킬 폴더예요: " + runeCut(t, 40)}
		}
		if err := os.MkdirAll(d, 0o755); err != nil {
			return "", err
		}
		r, err := filepath.EvalSymlinks(d) // ~/.claude/skills may be a link to ~/.agents/skills: write once
		if err != nil {
			return "", err
		}
		if !real[r] {
			real[r] = true
			jobs = append(jobs, job{t, d})
		}
	}
	commit, skills, files, err := loadSkillSource(src)
	if err != nil {
		return "", err
	}
	for _, j := range jobs {
		for _, sk := range skills {
			dest := filepath.Join(j.dir, sk.name)
			if _, err := os.Lstat(dest); err == nil && !update {
				return "", runtimeError{fmt.Sprintf("%s에 %s 스킬이 이미 있어요. 업데이트로 다시 요청해 주세요", installTargets[j.target], sk.name)}
			}
		}
	}
	var done []any
	for _, j := range jobs {
		for _, sk := range skills {
			dest := filepath.Join(j.dir, sk.name)
			_, statErr := os.Lstat(dest)
			if err := writeSkill(sk, files, src, commit, dest, statErr == nil); err != nil {
				return "", err
			}
			done = append(done, installTargets[j.target]+"/"+sk.name)
		}
	}
	return host.MustDumps(host.Obj{{Key: "commit", Value: commit}, {Key: "installed", Value: done}}, host.DumpOpts{Compact: true, NoASCII: true}), nil
}

// applySkillInstall runs one skill_install request: a preview when not enabled, otherwise the install.
func applySkillInstall(req map[string]any) (string, error) {
	src, err := parseSkillSource(str(req["name"]))
	if err != nil {
		return "", err
	}
	if !truthy(req["enabled"]) {
		commit, skills, _, err := loadSkillSource(src)
		if err != nil {
			return "", err
		}
		return previewDetail(src, commit, skills), nil
	}
	var targets []string
	for _, t := range asList(req["targets"]) {
		targets = append(targets, pyStr(t))
	}
	detail, err := installSkills(src, targets, truthy(req["update"]))
	if err == nil {
		startSkillSync() // the skill list picks it up now rather than in ten minutes
	}
	return detail, err
}

// mcpCommandWarning: a stdio server whose program is not on this host's PATH is still added,
// with a warning the app shows (npx needs Node.js, uvx needs uv, docker needs Docker).
func mcpCommandWarning(req map[string]any) string {
	cmd := asList(req["command"])
	if truthy(req["url"]) || len(cmd) == 0 {
		return ""
	}
	prog := pyStr(cmd[0])
	if filepath.IsAbs(prog) {
		if st, err := os.Stat(prog); err == nil && !st.IsDir() {
			return ""
		}
	} else if _, err := lookPath(prog); err == nil {
		return ""
	}
	return host.MustDumps(host.Obj{{Key: "warning", Value: "command_missing"}, {Key: "command", Value: runeCut(prog, 100)}}, host.DumpOpts{Compact: true})
}

// applyCatalogRequest handles the kinds whose result carries a detail (skill_install, mcp_add);
// ok is false for every other kind, which applyRequest handles as before.
func applyCatalogRequest(req, stash map[string]any) (host.Obj, bool) {
	var detail string
	var err error
	switch str(req["kind"]) {
	case "skill_install":
		detail, err = applySkillInstall(req)
	case "mcp_add":
		if err = applyRequest(req, stash); err == nil {
			detail = mcpCommandWarning(req)
		}
	default:
		return nil, false
	}
	res := host.Obj{{Key: "id", Value: req["id"]}}
	if err != nil {
		return append(res, host.Obj{{Key: "status", Value: "failed"}, {Key: "error", Value: runeCut(err.Error(), 300)}}...), true
	}
	res.Set("status", "done")
	if detail != "" {
		res.Set("detail", detail)
	}
	return res, true
}
