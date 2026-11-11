# Update Go for the HTTP server security patch

Task: PR #552 CI security follow-up
Research: none
Why: The pinned vulnerability checker stopped the Go job on GO-2026-6859, an HTTP/1 server crash caused by a MaxBytesReader header race in Go's net/http package. The job used Go 1.27.3; the official vulnerability database identifies Go 1.27.4 as the fixed version on this release line.
Changed: Raised the go.mod Go version from 1.27.3 to 1.27.4. The check, coverage, lint and release workflows already read this file through setup-go's go-version-file setting, so no second version pin or security-check exception was needed. Historical toolchain provenance records remain unchanged.
Verified: The official Go 1.27.4 toolchain reported go1.27.4 linux/amd64. With that toolchain, the same pinned command used by CI, `go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...`, exited 0 and reported zero reachable vulnerabilities and zero vulnerabilities in imported packages. It still reported one vulnerability in a required module whose vulnerable code does not appear to be called. Five focused HTTP tests passed with the race detector (3.314 seconds), covering JSON policy/body replay, multipart metadata replay, unknown JSON fields, anonymous caller rejection, and readiness/private request logging. All three internal/httpguard tests passed with the race detector (1.013 seconds). There were no failed or skipped tests in these selections.
Limits: These are local checks, not a completed rerun of PR CI or the entire functional suite. No product/release binary, model inference or PostgreSQL workload was run. The focused tests exercise existing HTTP boundaries; they do not independently reproduce the upstream race exploit.

The [official Go release history](https://go.dev/doc/devel/release#go1.27.4) dates Go 1.27.4 to November 10, 2026 and includes net/http security fixes. The [official GO-2026-6859 database entry](https://vuln.go.dev/ID/GO-2026-6859.json) records the affected and fixed versions.

## 한국어 기록

PR #552의 Go 검사는 애플리케이션 테스트에 도달하기 전에 표준 라이브러리 보안 취약점 때문에 중단됐다. 공식 수정 버전인 Go 1.27.4로 기준을 올렸고, 기존 보안 검사 명령을 그대로 실행해 도달 가능한 취약점 0개와 종료 코드 0을 확인했다. 실제 버전 기준은 go.mod 한 곳이며 CI와 릴리스 설정이 이를 읽는다.

HTTP 관련 기존 검사 5개와 HTTP 경계 검사 3개가 모두 race 검사로 통과했다. 전체 CI 통과를 의미하지 않으며, 호출되지 않는 필수 모듈 취약점 1개가 보고되는 한계도 유지한다. 제품 빌드와 모델 호출은 하지 않았다.
