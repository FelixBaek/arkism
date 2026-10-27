const ACTIONS = { retry: "미확인 부분부터 다시 보내기", ack_part: "이미 받은 메시지 확인하기", redirect: "다른 대화로 처음부터 보내기" };
const DESCRIPTIONS = {
  retry: "원래 대화에서 미확인 부분이 전송되지 않았는지 확인해 주세요. 이미 받은 부분을 다시 보내면 중복될 수 있어요.",
  ack_part: "원래 대화에서 현재 미확인 부분을 받은 메시지의 ID를 입력해요. 확인 후 다음 부분부터 이어서 전달해요.",
  redirect: "입력한 새 대화에 전체 결과를 처음부터 보내요. 기존 대화와 메시지 영수증은 복구 이력에 남아요."
};
const CONFIRMATIONS = {
  retry: "원래 대화를 확인했고 미확인 부분을 다시 보내는 데 동의해요.",
  ack_part: "입력한 메시지가 현재 미확인 부분의 실제 수신 메시지임을 확인했어요.",
  redirect: "새 목적지와 접근 권한을 확인했고 전체 결과를 보내는 데 동의해요."
};

// Opening the control never writes. Submission is a human decision about the
// displayed delivery revision, with no automatic inference of a destination.
export function createFlowDeliveryRecovery({ state, el, button, request, notice }) {
  const paragraph = text => el("p", "flow-history-note", text);
  function field(labelText, name, numeric = false) {
    const label = el("label", "flow-recovery-field", labelText), input = el("input", "");
    input.type = "text"; input.name = name; input.autocomplete = "off";
    input.setAttribute("aria-label", labelText);
    if (numeric) input.inputMode = "numeric";
    label.append(input);
    return { label, input };
  }
  function render(box, record, reload, current) {
    if (!record?.id || !Number.isInteger(record.revision) || record.revision < 1 || !["unknown", "failed"].includes(record.delivery?.status)) return;
    const details = el("details", "flow-delivery-recovery"), summary = el("summary", "", "전달 복구");
    details.append(summary, paragraph("사람 계정의 복구 권한이 필요해요. 원래 대화를 확인한 뒤 방법을 선택해 주세요."));
    const form = el("form", "flow-recovery-form"), label = el("label", "flow-recovery-field", "전달 복구 방법"), select = el("select", "");
    select.setAttribute("aria-label", "전달 복구 방법");
    for (const [value, text] of [["", "복구 방법 선택"], ...Object.entries(ACTIONS)]) {
      if (value === "ack_part" && record.delivery.status !== "unknown") continue;
      const option = el("option", "", text); option.value = value; select.append(option);
    }
    label.append(select);
    const description = paragraph(""), fields = el("div", "flow-recovery-fields"), confirmation = el("label", "flow-recovery-confirm"), check = el("input", ""), confirmationText = el("span", "");
    check.type = "checkbox"; check.setAttribute("aria-label", "복구 내용 확인");
    confirmation.append(check, confirmationText); confirmation.hidden = true;
    const send = button("확인한 내용으로 복구 요청", "primary", null, () => void submit());
    send.type = "button";
    send.disabled = true;
    let inputs = [], sending = false;
    const valid = () => !!select.value && check.checked && inputs.every(input => input.value.trim());
    const update = () => { send.disabled = sending || !valid(); };
    select.addEventListener("change", () => {
      fields.replaceChildren(); inputs = []; check.checked = false;
      description.textContent = DESCRIPTIONS[select.value] || "";
      confirmationText.textContent = CONFIRMATIONS[select.value] || "";
      confirmation.hidden = !select.value;
      const names = select.value === "ack_part" ? [["확인한 Discord 메시지 ID", "message_id", true]] : select.value === "redirect" ? [["Arkism 봇 ID", "bot_id"], ["Discord 서버 ID", "guild_id", true], ["Discord 채널 ID", "channel_id", true], ["Discord 대화·스레드 ID", "thread_id", true]] : [];
      for (const [title, name, numeric] of names) { const item = field(title, name, numeric); fields.append(item.label); inputs.push(item.input); }
      update();
    });
    check.addEventListener("change", update); fields.addEventListener("input", update);
    form.addEventListener("submit", event => { event.preventDefault(); void submit(); });
    async function submit() {
      if (sending || !valid() || !current()) return;
      const action = select.value, values = Object.fromEntries(inputs.map(input => [input.name, input.value.trim()]));
      const payload = { scope: state.scope, expected_revision: record.revision, action, confirmed: true, ...(action === "redirect" ? { destination: values } : values) };
      sending = true; select.disabled = check.disabled = true; for (const input of inputs) input.disabled = true; update();
      try {
        await request(`/v1/crew/deliveries/${encodeURIComponent(record.id)}/recover`, { method: "POST", body: JSON.stringify(payload) });
        if (!current()) return;
        notice("복구 요청을 저장했어요. 실제 전달 상태를 다시 확인해 주세요.");
        reload();
      } catch {
        if (!current()) return;
        notice("복구 요청 결과를 확인하지 못했어요. 새로고침해 현재 상태와 권한을 확인해 주세요.");
        sending = false; select.disabled = check.disabled = false; for (const input of inputs) input.disabled = false; update();
      }
    }
    form.append(label, description, fields, confirmation, send); details.append(form); box.append(details);
  }
  return { render };
}
