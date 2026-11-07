# 기억 문맥 옵션 / Memory context options

기존 호출의 기본 동작은 같습니다. MCP `recall` 도구와 같은 도구를 실행하는 stdio CLI에서 호출마다 다음 선택지를 지정할 수 있습니다.

| 옵션 | 기본 | 의미 |
|---|---|---|
| `conversation_context` | `false` | 원본 대화도 검토된 기억과 같은 토큰 한도 안에 담습니다. 원본 대화는 검토되지 않은 자료이며 지시가 아닙니다. |
| `whole_items` | `false` | 긴 기억을 중간에서 자르는 대신 들어가지 않는 기억을 건너뜁니다. |
| `best_effort_lanes` | `false` | 선택적 검색 경로가 일시적으로 실패하면 나머지 결과와 `degraded` 이유를 반환합니다. 권한 거부를 무시하지 않습니다. |

예를 들어 `recall` arguments는 다음과 같습니다. `ids`를 지정하는 직접 조회에는 이 문맥 옵션을 같이 쓸 수 없습니다.

```json
{
  "query": "회의 장소를 정한 대화",
  "conversation_context": true,
  "whole_items": true,
  "best_effort_lanes": true,
  "budget_tokens": 3000,
  "token_encoding": "cl100k_base"
}
```

`conversation_context`에는 `budget_tokens`(1–64000)와 `token_encoding`(`cl100k_base` 또는 `o200k_base`)을 함께 지정해야 합니다. 예산을 지정하면 서버가 선택한 `context_data` 문자열을 바꾸지 않고 반환합니다. 이 경우 `memories`도 그 서버 선택을 보존하며 별도의 호스트 점수 하한을 다시 적용하지 않습니다. 토큰 한도는 `context_data`만 대상으로 하고, 도구 응답의 나머지 설명과 미리보기 필드는 추가입니다. 최종 모델 입력을 구성하는 호출자는 `context_data`를 한 번만 담고 별도 시스템 안내·질문·도구 봉투의 예산도 계산해야 합니다.

관리 UI의 **기억 → 검색 살펴보기 → 자세히 → 결과에 붙일 것**에서 같은 세 선택지를 켤 수 있습니다. 원본 대화를 선택하면 토큰 한도를 입력해야 합니다. 조회 결과의 **최종 문맥 미리보기**는 서버의 문맥 원문을 자료로 표시하며 모델을 호출하지 않습니다.

이 변경은 자동 프롬프트 hook을 일괄 활성화하지 않습니다. 고정된 에이전트 설정과 지원하지 않는 조합은 서버에서 계속 거부합니다. 동적 기억의 native 경로도 이 선택지에서는 기존 baseline fallback을 유지합니다. 반환·전달 확인은 모델의 실제 사용이나 작업 성공의 증거와 다릅니다.

Existing invocations stay unchanged. Options are explicit per invocation in the host recall router, MCP and its stdio CLI. With a token budget, consume the exact `context_data` packet once; the surrounding response is not included in that budget. The search inspection UI previews this packet without calling a model. Pinned-agent rejection and native baseline fallback remain server contracts. Automatic prompt hooks are not enabled by these options.
