# 서버에 설치하기

여러 PC가 같은 기억을 써야 할 때만 서버가 필요해요. 혼자 한 PC에서 쓰면 [처음 시작하기](start.ko.md)의 **이 PC에서 쓰기**로 충분해요.

앱은 SSH 주소 하나로 서버에 붙어요. 서버에서 따로 포트를 열 필요는 없어요. 대신 서버에 **Arkism이 설치돼 켜져 있어야** 해요. 아직 서버용 설치 파일은 없어서, 지금은 소스에서 빌드해 설치해요. 자세한 설정값은 영어 안내 [Server mode](server.md)에 있어요.

## 서버에서 할 일

1. Postgres 17과 pgvector를 준비하고, 역할 두 개를 만들어요. `arkism_init`은 스키마를 만들고, `arkism_app`은 API와 작업자가 써요. SQL은 [`tools/roles.sql`](../tools/roles.sql)에 있어요.
2. `arkism_init` 연결로 스키마를 만들어요.

   ```bash
   export MEMORY_DATABASE_URL='postgres://arkism_init:비밀번호@127.0.0.1:5432/arkism?sslmode=disable'
   go run ./cmd/memoryd --init-db
   go run ./cmd/memoryd --check-indexes
   ```

3. 주인 토큰과 작업자 토큰을 만들고 등록해요. 토큰은 90일 안에 만료되니 그 전에 새로 만들어요. 방법은 [토큰 안내](principal-tokens.md)에 있어요.
4. `MEMORY_DATABASE_URL`을 `arkism_app` 연결로 바꾼 설정 파일을 만들고, API(`memoryd`)와 작업자(`memoryd --worker`)를 켜요. 계속 켜 두려면 각각 systemd 서비스로 등록해요.
5. `arkism` 도구를 설치하고 주인 토큰을 SSH 사용자만 읽게 저장해요.

   ```bash
   go build -o arkism ./cmd/arkism
   sudo install -d /srv/arkism/bin /srv/arkism/config
   sudo install -m 0755 arkism /srv/arkism/bin/arkism
   printf '%s\n' '주인-토큰' | sudo tee /srv/arkism/config/owner.token >/dev/null
   sudo chown "$USER" /srv/arkism/config/owner.token
   sudo chmod 0600 /srv/arkism/config/owner.token
   ```

6. `/srv/arkism/bin/arkism connect-info`가 접속 정보를 JSON으로 보여 주면 준비가 끝났어요.

## PC에서 할 일

1. 이 PC의 터미널에서 `ssh 사용자@서버주소`로 접속되는지 먼저 확인해요. SSH 키를 쓰면 비밀번호를 묻지 않아요. 비밀번호로도 연결할 수 있고, 그 비밀번호는 PC의 키체인에 저장돼요.
2. 앱의 **서버에 SSH로 연결**에서 **서버 주소**에 `사용자@서버주소`를 넣고 **연결하기**를 눌러요. 포트·키 파일·비밀번호는 **자세히**를 열어 넣어요.
3. 처음 보는 서버면 서버 지문을 확인하는 창이 떠요. 서버 관리자에게 받은 지문과 같을 때만 승인해요.

## 연결이 안 될 때

| 앱에 나온 말 | 할 일 |
| --- | --- |
| 서버에 Arkism이 없어요 | 위의 서버에서 할 일을 마쳤는지, `arkism connect-info`가 되는지 확인해요. `arkism`이 다른 곳에 있으면 **자세히**의 Arkism 경로에 넣어요. |
| SSH 로그인이 거부됐어요 | 터미널에서 `ssh` 명령이 비밀번호 없이 되는지 확인하거나, 인증 방식을 비밀번호로 바꿔요. |
| 서버에 닿지 못했어요 | 주소·포트와 네트워크를 확인해요. |
| 서버의 Arkism이 오래됐어요 | 서버의 `arkism`을 새로 빌드해 바꿔요. |
