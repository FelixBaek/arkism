package skills

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

func gitT(t *testing.T, repo string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatal(args, string(out), err)
	}
	return strings.TrimSpace(string(out))
}

func swapToolinfo(t *testing.T, cache string) string {
	t.Helper()
	root := t.TempDir()
	st, tc, uc, pr := tiState, toolsCache, updatesCache, claudeProjects
	tiState, toolsCache, updatesCache, claudeProjects = root, filepath.Join(root, "t.json"), filepath.Join(root, cache), filepath.Join(root, "projects")
	t.Cleanup(func() { tiState, toolsCache, updatesCache, claudeProjects = st, tc, uc, pr })
	return root
}

func TestCallsFromTranscripts(t *testing.T) {
	root := swapToolinfo(t, "u.json")
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	rows := []string{
		`{"timestamp": "` + now + `", "message": {"content": [{"type": "tool_use", "id": "t1", "name": "mcp__github__create_pull_request"}]}}`,
		`{"timestamp": "` + now + `", "message": {"content": [{"type": "tool_result", "tool_use_id": "t1", "is_error": false}]}}`,
		`{"timestamp": "` + now + `", "message": {"content": [{"type": "tool_use", "id": "t2", "name": "Bash"}]}}`,
	}
	write(t, filepath.Join(root, "projects", "p", "s.jsonl"), strings.Join(rows, "\n"))
	got := asObj(objGet(toolCalls(time.Time{}), "github"))
	recent := objGet(got, "recent").([]any)
	if objGet(got, "today") != 1 || objGet(asObj(recent[0]), "tool") != "create_pull_request" || objGet(asObj(recent[0]), "ok") != true {
		t.Fatal(got)
	}
}

func TestRiskyAndCachedToolLists(t *testing.T) {
	if !risky(map[string]any{"name": "merge_pull_request"}) || risky(map[string]any{"name": "list_issues"}) ||
		risky(map[string]any{"name": "delete_x", "annotations": map[string]any{"readOnlyHint": true}}) {
		t.Fatal("risky")
	}
	swapToolinfo(t, "u.json")
	calls := 0
	prober := func(map[string]any) []any {
		calls++
		return []any{map[string]any{"name": "send_mail"}, map[string]any{"name": "search"}}
	}
	defs := map[string]any{"mail": map[string]any{"command": "mail-mcp"}, "web": map[string]any{"url": "https://x"}}
	got, err := serverTools(defs, 1, prober)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := serverTools(defs, 1, prober)
	if calls != 1 || host.MustDumps(got, host.DumpOpts{}) != host.MustDumps(again, host.DumpOpts{}) {
		t.Fatal(calls, got, again)
	}
	mail := objGet(got, "mail").([]any)
	if objGet(asObj(mail[0]), "risky") != true || objGet(asObj(mail[1]), "risky") != false {
		t.Fatal(mail)
	}
}

func TestStdioToolsListsAServer(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	script := `read a; echo '{"jsonrpc":"2.0","id":1,"result":{}}'; read b; read c; echo 'noise'; echo '{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"x"}]}}'`
	got := stdioTools(map[string]any{"command": "sh", "args": []any{"-c", script}})
	if len(got) != 1 || asMap(got[0])["name"] != "x" {
		t.Fatal(got)
	}
}

func TestPluginContents(t *testing.T) {
	p := filepath.Join(t.TempDir(), "plug")
	write(t, filepath.Join(p, ".claude-plugin/plugin.json"), `{"version": "0.3.1", "description": "메일"}`)
	write(t, filepath.Join(p, "skills/mail-triage/SKILL.md"), "---\nname: mail-triage\ndescription: 급한 순서로\n---\n")
	write(t, filepath.Join(p, "commands/mail.md"), "---\ndescription: 오늘 메일 요약\n---\n")
	write(t, filepath.Join(p, ".mcp.json"), `{"mcpServers": {"demo-mail": {}}}`)
	got := pluginContents(p)
	var pairs []string
	for _, c := range objGet(got, "contents").([]any) {
		pairs = append(pairs, pyStr(objGet(asObj(c), "kind"))+":"+pyStr(objGet(asObj(c), "name")))
	}
	if objGet(got, "version") != "0.3.1" || strings.Join(pairs, ",") != "skill:mail-triage,mcp:demo-mail,command:/mail" {
		t.Fatal(got)
	}
}

func cloneWithIdentity(t *testing.T, root, up, name string) string {
	gitT(t, root, "clone", "-q", up, name)
	repo := filepath.Join(root, name)
	gitT(t, repo, "config", "user.email", "t@t")
	gitT(t, repo, "config", "user.name", "t")
	return repo
}

func TestSkillUpdatesAndPull(t *testing.T) {
	root := swapToolinfo(t, "u.json")
	gitT(t, root, "init", "-q", "--bare", "up")
	mine := cloneWithIdentity(t, root, filepath.Join(root, "up"), "mine")
	write(t, filepath.Join(mine, "release-notes/SKILL.md"), "---\nname: release-notes\nversion: 1.2.0\n---\n")
	gitT(t, mine, "add", "-A")
	gitT(t, mine, "commit", "-qm", "a")
	gitT(t, mine, "push", "-q", "origin", "HEAD")
	other := cloneWithIdentity(t, root, filepath.Join(root, "up"), "other")
	write(t, filepath.Join(other, "release-notes/SKILL.md"), "---\nname: release-notes\nversion: 1.3.0\n---\n")
	gitT(t, other, "commit", "-qam", "b")
	gitT(t, other, "push", "-q", "origin", "HEAD")
	got, err := skillUpdates([]string{mine}, 0)
	if err != nil || len(got) != 1 {
		t.Fatal(got, err)
	}
	u := asObj(got[0])
	if objGet(u, "name") != "release-notes" || objGet(u, "from") != "1.2.0" || objGet(u, "to") != "1.3.0" || canon(t, objGet(u, "commits")) != "1" {
		t.Fatal(u)
	}
	if again, _ := skillUpdates([]string{mine}, 0); len(again) != 1 { // the cached answer is sent again
		t.Fatal(again)
	}
	repos := skillRepoChecks()
	if len(repos) != 1 || canon(t, objGet(asObj(repos[0]), "behind")) != "1" || !strings.HasSuffix(filepath.ToSlash(str(objGet(asObj(repos[0]), "remote"))), "/up") {
		t.Fatal(repos)
	}
	if err := updateRepo(mine); err != nil {
		t.Fatal(err)
	}
	if got, _ := skillUpdates([]string{mine}, 0); len(got) != 0 || canon(t, objGet(asObj(skillRepoChecks()[0]), "behind")) != "0" {
		t.Fatal(got, skillRepoChecks())
	}
}

func TestLongNonASCIIRemoteIsCutInBytesAndTheUpstreamRemoteIsUsed(t *testing.T) {
	if len(cutBytes(strings.Repeat("가", 150), 200)) > 200 || cutBytes("가나", 4) != "가" {
		t.Fatal("cutBytes")
	}
	root := swapToolinfo(t, "u2.json")
	gitT(t, root, "init", "-q", "--bare", "up2")
	mine := cloneWithIdentity(t, root, filepath.Join(root, "up2"), "mine2")
	write(t, filepath.Join(mine, "a/SKILL.md"), "---\nname: a\n---\n")
	gitT(t, mine, "add", "-A")
	gitT(t, mine, "commit", "-qm", "a")
	gitT(t, mine, "push", "-q", "origin", "HEAD")
	gitT(t, mine, "remote", "add", "decoy", "https://example.org/decoy/repo.git")
	skillUpdates([]string{mine}, 0)
	if r := str(objGet(asObj(skillRepoChecks()[0]), "remote")); !strings.HasSuffix(filepath.ToSlash(r), "/up2") { // the upstream's remote, not the other one
		t.Fatal(r)
	}
	gitT(t, mine, "remote", "rename", "origin", "team/upstream") // a remote name with a slash must still resolve
	updatesCache = filepath.Join(root, "u3.json")
	skillUpdates([]string{mine}, 0)
	if r := str(objGet(asObj(skillRepoChecks()[0]), "remote")); !strings.HasSuffix(filepath.ToSlash(r), "/up2") {
		t.Fatal(r)
	}
}

func TestRecheckForgetsTheCacheAndALocalUpstreamHasNoRemote(t *testing.T) {
	root := swapToolinfo(t, "u4.json")
	gitT(t, root, "init", "-q", "loc")
	mine := filepath.Join(root, "loc")
	gitT(t, mine, "config", "user.email", "t@t")
	gitT(t, mine, "config", "user.name", "t")
	write(t, filepath.Join(mine, "a/SKILL.md"), "---\nname: a\n---\n")
	gitT(t, mine, "add", "-A")
	gitT(t, mine, "commit", "-qm", "a")
	base := gitT(t, mine, "symbolic-ref", "--short", "HEAD")
	gitT(t, mine, "checkout", "-q", "-b", "side", "--track", base) // side's upstream is the local branch (remote ".")
	skillUpdates([]string{mine}, 0)
	if checks := skillRepoChecks(); len(checks) != 1 || objGet(asObj(checks[0]), "remote") != "" { // the entry stays, without an address
		t.Fatal(checks)
	}
	if !exists(updatesCache) {
		t.Fatal("no cache")
	}
	if err := recheck(mine); err != nil || exists(updatesCache) {
		t.Fatal(err)
	}
	if err := recheck(filepath.Join(root, "nowhere")); err == nil {
		t.Fatal("recheck accepted a non-repository")
	}
}

func TestRemoteAddressesLoseCredentials(t *testing.T) {
	for raw, want := range map[string]string{"https://user:tok3n@github.com/o/r.git": "github.com/o/r", "git@github.com:o/r.git": "github.com/o/r",
		"ssh://git@host.example/o/r": "host.example/o/r", "/srv/git/up.git": "/srv/git/up"} {
		if got := publicRemote(raw); got != want {
			t.Fatal(raw, got)
		}
	}
}
