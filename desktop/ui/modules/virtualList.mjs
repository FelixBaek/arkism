// Measured, variable-height rows. A Fenwick tree keeps scroll lookup logarithmic.
export function createHeightIndex(count, estimate = 76) {
  const heights = new Float64Array(count).fill(estimate), tree = new Float64Array(count + 1);
  function add(index, delta) { for (let i = index + 1; i <= count; i += i & -i) tree[i] += delta; }
  for (let i = 0; i < count; i++) add(i, estimate);
  function offset(end) { let total = 0; for (let i = end; i > 0; i -= i & -i) total += tree[i]; return total; }
  function measure(index, height) {
    if (!Number.isFinite(height) || height <= 0 || index < 0 || index >= count) return 0;
    const delta = height - heights[index]; heights[index] = height; add(index, delta); return delta;
  }
  function at(position) {
    let index = 0, sum = 0, step = 1;
    while (step * 2 <= count) step *= 2;
    for (; step; step >>= 1) {
      const next = index + step;
      if (next <= count && sum + tree[next] <= position) { index = next; sum += tree[next]; }
    }
    return Math.min(index, Math.max(0, count - 1));
  }
  return { offset, at, measure };
}

const focusable = node => [...node.querySelectorAll("button:not(:disabled), summary, input:not(:disabled), select:not(:disabled), a[href], [tabindex='0']")].filter(el => el.getClientRects().length);
const targets = node => node.matches("button, summary, [tabindex='0']") ? [node, ...focusable(node)] : focusable(node);

// One active list per screen. clear() disconnects observers before the app draws
// another screen; no scrolling listener or row cache survives a lock/navigation.
export function createVirtualList() {
  let dispose = () => {};
  let measurementKey = "", measured = new Map(), previousPosition = { key: "", top: 0 };
  function clear() { dispose(); dispose = () => {}; }
  function mount({ container, scrollport = container, items, row, key = "", id = item => item.id, threshold = 150 }) {
    clear();
    const initialScroll = previousPosition.key === key ? previousPosition.top : scrollport.scrollTop;
    if (items.length <= threshold) { container.replaceChildren(...items.map(row)); return; }
    const document = container.ownerDocument, host = document.createElement("div");
    host.className = "virtual-list"; host.setAttribute("role", "list");
    container.replaceChildren(host);
    const layout = `${key}|${scrollport.clientWidth}|${getComputedStyle(scrollport).fontSize}|${getComputedStyle(document.documentElement).getPropertyValue("--fs")}`;
    if (measurementKey !== layout) { measurementKey = layout; measured = new Map(); }
    const sizes = createHeightIndex(items.length), nodes = new Map();
    items.forEach((item, index) => { if (measured.has(id(item))) sizes.measure(index, measured.get(id(item))); });
    let frame = 0, stopped = false, start = -1, end = -1, focused = -1;
    const top = () => host.getBoundingClientRect().top - scrollport.getBoundingClientRect().top + scrollport.scrollTop - scrollport.clientTop;
    const schedule = () => { if (!frame && !stopped) frame = requestAnimationFrame(() => { frame = 0; draw(); }); };
    const observe = new ResizeObserver(entries => {
      const anchor = sizes.at(Math.max(0, scrollport.scrollTop - top()));
      let correction = 0;
      for (const entry of entries) {
        const index = Number(entry.target.dataset.virtualIndex);
        const height = entry.borderBoxSize?.[0]?.blockSize || entry.target.getBoundingClientRect().height;
        const delta = sizes.measure(index, height);
        measured.set(id(items[index]), height);
        if (index < anchor) correction += delta;
      }
      if (correction) scrollport.scrollTop += correction;
      start = -1; schedule();
    });
    const viewport = new ResizeObserver(() => { start = -1; schedule(); });
    viewport.observe(scrollport);
    function spacer(height) {
      const gap = document.createElement("div"); gap.className = "virtual-spacer"; gap.setAttribute("aria-hidden", "true"); gap.style.height = `${Math.max(0, height)}px`; return gap;
    }
    function nodeAt(index) {
      if (nodes.has(index)) return nodes.get(index);
      const node = row(items[index], index); node.dataset.virtualIndex = index;
      // Keep native button/summary roles. Position information belongs to the
      // listitem wrapper, including the single focused row retained off screen.
      const wrap = document.createElement("div"); wrap.className = "virtual-item"; wrap.setAttribute("role", "listitem");
      wrap.setAttribute("aria-posinset", index + 1); wrap.setAttribute("aria-setsize", items.length); wrap.dataset.virtualIndex = index;
      if (index === items.length - 1) wrap.setAttribute("data-virtual-last", "");
      wrap.append(node);
      nodes.set(index, wrap); observe.observe(wrap); return wrap;
    }
    function draw(force = false) {
      if (stopped || !host.isConnected) return;
      const y = Math.max(0, scrollport.scrollTop - top());
      const first = Math.max(0, sizes.at(y) - 6), last = Math.min(items.length, sizes.at(y + scrollport.clientHeight) + 8);
      const active = document.activeElement?.closest(".virtual-item"), pin = active?.parentElement === host ? Number(active.dataset.virtualIndex) : -1;
      if (!force && first === start && last === end && pin === focused) return;
      start = first; end = last; focused = pin;
      const indices = Array.from({ length: last - first }, (_, i) => first + i);
      if (pin >= 0 && (pin < first || pin >= last)) indices.push(pin);
      indices.sort((a, b) => a - b);
      const keep = new Set(indices);
      for (const [index, node] of nodes) if (!keep.has(index)) { observe.unobserve(node); node.remove(); nodes.delete(index); }
      host.querySelectorAll(":scope > .virtual-spacer").forEach(node => node.remove());
      let cursor = 0, previous = null;
      for (const index of indices) {
        const gap = spacer(sizes.offset(index) - sizes.offset(cursor));
        host.insertBefore(gap, previous ? previous.nextSibling : host.firstChild);
        const node = nodeAt(index);
        // Leave an already positioned focused node connected, preserving focus.
        if (gap.nextSibling !== node) host.insertBefore(node, gap.nextSibling);
        previous = node; cursor = index + 1;
      }
      host.append(spacer(sizes.offset(items.length) - sizes.offset(cursor)));
    }
    function focusIndex(index, backwards = false) {
      index = Math.max(0, Math.min(items.length - 1, index));
      scrollport.scrollTop = top() + sizes.offset(index);
      draw(true);
      const choices = targets(nodeAt(index));
      (backwards ? choices.at(-1) : choices[0])?.focus({ preventScroll: true });
      schedule();
    }
    function keydown(event) {
      const wrap = event.target.closest(".virtual-item");
      if (!wrap || event.altKey || event.ctrlKey || event.metaKey || event.target.matches("input,textarea,select,[contenteditable]")) return;
      const index = Number(wrap.dataset.virtualIndex), choices = targets(wrap), at = choices.indexOf(event.target);
      if (event.key === "Tab") {
        const next = index + (event.shiftKey ? -1 : 1), edge = event.shiftKey ? at === 0 : at === choices.length - 1;
        if (edge && next >= 0 && next < items.length) { event.preventDefault(); focusIndex(next, event.shiftKey); }
        return;
      }
      const destination = { ArrowDown: index + 1, ArrowUp: index - 1, Home: 0, End: items.length - 1 }[event.key];
      if (destination !== undefined) { event.preventDefault(); focusIndex(destination); }
    }
    host.addEventListener("keydown", keydown);
    host.addEventListener("focusout", schedule);
    scrollport.addEventListener("scroll", schedule, { passive: true });
    host.append(spacer(sizes.offset(items.length)));
    scrollport.scrollTop = initialScroll;
    draw();
    dispose = () => {
      previousPosition = { key, top: scrollport.scrollTop };
      stopped = true; cancelAnimationFrame(frame); observe.disconnect(); viewport.disconnect();
      scrollport.removeEventListener("scroll", schedule); host.removeEventListener("keydown", keydown); host.removeEventListener("focusout", schedule); nodes.clear();
    };
  }
  return { mount, clear };
}
