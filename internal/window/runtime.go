package window

// AnnotationJS is injected into every document the window loads
// (Page.addScriptToEvaluateOnNewDocument, so it survives navigation and runs
// before page scripts). Spike scope: select text → floating "mark" pill →
// optional comment → highlight painted + duckMark(json) delivered to the
// host over the CDP binding. __duckApplyMarks re-paints stored highlights
// after navigation (host calls it via Evaluate).
const AnnotationJS = `(() => {
if (window.__duckAnnotate) return; window.__duckAnnotate = true;

const HL = 'rgba(255, 208, 80, .45)';
function paint(range) {
  const m = document.createElement('mark');
  m.style.background = HL; m.style.color = 'inherit'; m.dataset.duckMark = '1';
  try { range.surroundContents(m); return true; } catch (e) { return false; }
}

// Context: up to 40 chars each side, for anchor-drift-tolerant querying.
function context(range) {
  const t = range.startContainer.textContent || '';
  const before = t.slice(Math.max(0, range.startOffset - 40), range.startOffset);
  const t2 = range.endContainer.textContent || '';
  const after = t2.slice(range.endOffset, range.endOffset + 40);
  return { before, after };
}

let pill = null;
function hidePill() { if (pill) { pill.remove(); pill = null; } }

document.addEventListener('mouseup', () => {
  setTimeout(() => {
    hidePill();
    const sel = window.getSelection();
    if (!sel || sel.isCollapsed || !sel.toString().trim()) return;
    const range = sel.getRangeAt(0);
    const r = range.getBoundingClientRect();
    pill = document.createElement('button');
    pill.textContent = '✎ mark';
    Object.assign(pill.style, {
      position: 'fixed', left: (r.left + r.width / 2 - 28) + 'px',
      top: Math.max(4, r.top - 30) + 'px', zIndex: 2147483647,
      background: '#1C2420', color: '#5BC4A8', border: '1px solid #5BC4A8',
      borderRadius: '999px', padding: '2px 10px', font: '12px ui-monospace,monospace',
      cursor: 'pointer',
    });
    pill.addEventListener('mousedown', ev => ev.preventDefault());
    pill.addEventListener('click', () => {
      const text = sel.toString();
      const comment = prompt('comment (optional):') || '';
      const ctx = context(range);
      paint(range);
      sel.removeAllRanges(); hidePill();
      if (window.duckMark) window.duckMark(JSON.stringify({
        url: location.href, text, comment, before: ctx.before, after: ctx.after,
      }));
    });
    document.body.appendChild(pill);
  }, 0);
});

// Re-paint stored marks: first occurrence of each mark's text.
window.__duckApplyMarks = (marks) => {
  for (const m of marks || []) {
    const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
    let node; let done = false;
    while (!done && (node = walker.nextNode())) {
      const i = node.textContent.indexOf(m.text);
      if (i < 0) continue;
      const range = document.createRange();
      range.setStart(node, i); range.setEnd(node, i + m.text.length);
      done = paint(range);
    }
  }
};
})();`
