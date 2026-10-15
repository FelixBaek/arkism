# Publishing from a public checkout

Private development branches can contain confidential files in old commits even
after the files have been deleted. Never push or open a public PR from such a
branch. Deleting a directory from the latest tree does not remove its history.

Use a separate clone of the public repository, fetch its public main branch, and
create a codex/ branch from that public base. Copy only reviewed code changes
into that clone. Do not merge private development branches or copy their Git
metadata. Keep private documentation and the content denylist outside the clone.
Use .git/info/exclude for local ignore rules; never commit .gitignore,
.dockerignore, .env, or any .env.* file to this public repository.

Before committing, run the preflight to check the index and tracked working
files. After committing, run it again immediately before pushing:

    git fetch origin main
    python3 tools/check_public_push.py --base origin/main --denylist /outside/repository/public-denylist.txt

The denylist contains one Python regular expression per line, matched without
case sensitivity. Empty lines and comment lines beginning with # are ignored.
Use organization-specific private identifiers and other known sensitive markers;
the tool cannot discover unknown secrets by itself. This syntax differs from
the Perl regular expressions accepted by tools/check_public.sh.

The base must be an ancestor of HEAD and must be verified as coming from the
public upstream. The checker cannot establish trust in an arbitrary local ref.
If upstream advances, update the public branch before running the check.
A successful result reports the resolved public base and outgoing commit count.

The checker inspects every outgoing commit's complete tree and commit message,
including merge parents and intermediate files later deleted. It also checks
HEAD, the index, and tracked working files. The already-public history preceding
the trusted base is outside this outgoing-history check. Submodules require a
separate audit and are rejected. Untracked files are not pushed; review the
staging list and rerun after adding files. LICENSE content is scanned too; there are no filename content exemptions.

Reports withhold matching file content and names matching denylist patterns. File paths and commit IDs can still be
private metadata: keep the report local. Exit codes are 0 for clean, 1 for
findings, and 2 when the check cannot run. The command is a preflight, not an
installed Git hook or a guarantee against every possible information leak.

Do not delete someone else's private checkout or rewrite a branch another
session is using. Move future public work to the clean clone; any cleanup of
existing private history is a separate, coordinated operation.

## Reviewed immutable public content

A private denylist may also match intentionally public copyright text. After
reviewing that exact content in the trusted upstream base, obtain its blob hash
with git rev-parse origin/main:LICENSE and pass --allow-public-blob HASH.
Repeat the option only for individually reviewed public blobs. The checker
requires each complete hash to identify a blob in the trusted base tree.
It exempts only those exact bytes from content matching, including identical
tracked working files. Changed content is scanned again. Forbidden paths,
private path patterns, and commit messages remain checked. There is no
filename-wide LICENSE exception and no global denylist suppression.
