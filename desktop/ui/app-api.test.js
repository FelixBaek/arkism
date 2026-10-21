const test = require("node:test"), assert = require("node:assert/strict");

test("loadFailure tells an unreachable server from each kind of answer", async () => {
  const { loadFailure } = await import("./modules/appApi.mjs");
  const cases = [
    [{ code: "memory_list_invalid" }, true, "서버 답을 읽지 못했어요"],
    [{ status: 401 }, true, "서버가 접속을 거절했어요"],
    [{ code: "access_denied" }, true, "서버가 접속을 거절했어요"],
    [{ status: 503, code: "dependency_unavailable" }, true, "서버에 문제가 생겼어요"],
    [{ status: 404 }, true, "서버가 요청을 받지 않았어요"],
    [new Error("Failed to fetch"), false, "서버 연결이 끊겼어요"],
    [undefined, false, "서버 연결이 끊겼어요"],
  ];
  for (const [error, reached, title] of cases) {
    const got = loadFailure(error);
    assert.equal(got.reached, reached, title);
    assert.equal(got.title, title);
  }
  assert.equal(loadFailure({ code: "x_invalid" }).code, "x_invalid");
});
