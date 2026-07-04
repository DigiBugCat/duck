package window

// AnnotationJS is injected into every document the window loads
// (Page.addScriptToEvaluateOnNewDocument, so it survives navigation and runs
// before page scripts). It provides highlight and draw modes plus an in-page
// comment composer because WKWebView does not present JavaScript dialogs.
const AnnotationJS = `(() => {
if (window.__duckAnnotate) return; window.__duckAnnotate = true;

const HL = 'rgba(255, 208, 80, .45)';
const RED = '#e53935';
const Z = 2147483647;
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

function button(label) {
  const b = document.createElement('button');
  b.type = 'button';
  b.textContent = label;
  Object.assign(b.style, {
    border: '1px solid #5BC4A8', background: '#1C2420', color: '#DFFAF2',
    borderRadius: '6px', padding: '6px 9px', font: '12px ui-monospace,monospace',
    cursor: 'pointer',
  });
  return b;
}

let mode = 'highlight';
const toolbar = document.createElement('div');
toolbar.setAttribute('data-duck-toolbar', '1');
Object.assign(toolbar.style, {
  position: 'fixed', right: '14px', bottom: '14px', zIndex: Z,
  display: 'flex', gap: '6px', padding: '6px', background: 'rgba(28,36,32,.94)',
  border: '1px solid rgba(91,196,168,.8)', borderRadius: '8px',
  boxShadow: '0 8px 24px rgba(0,0,0,.24)',
});
const highlightBtn = button('Highlight');
const drawBtn = button('Draw');
toolbar.append(highlightBtn, drawBtn);
document.documentElement.appendChild(toolbar);

let composer = null;
function hideComposer() {
  if (composer) { composer.remove(); composer = null; }
}
function showComposer(onSend, onCancel) {
  hideComposer();
  composer = document.createElement('form');
  composer.setAttribute('data-duck-composer', '1');
  Object.assign(composer.style, {
    position: 'fixed', left: '50%', bottom: '16px', transform: 'translateX(-50%)',
    zIndex: Z, display: 'flex', alignItems: 'center', gap: '8px',
    width: 'min(560px, calc(100vw - 32px))', padding: '8px',
    background: 'rgba(28,36,32,.96)', border: '1px solid rgba(91,196,168,.8)',
    borderRadius: '8px', boxShadow: '0 10px 30px rgba(0,0,0,.28)',
  });
  const input = document.createElement('input');
  input.placeholder = 'Add a note…';
  Object.assign(input.style, {
    flex: '1 1 auto', minWidth: '80px', border: '1px solid rgba(223,250,242,.28)',
    background: '#111715', color: '#F5FFFC', borderRadius: '6px',
    padding: '8px 10px', font: '13px ui-sans-serif,system-ui,sans-serif',
  });
  const cancel = button('Cancel');
  const send = button('Send');
  send.style.background = '#5BC4A8'; send.style.color = '#10201B';
  composer.append(input, cancel, send);
  composer.addEventListener('submit', ev => {
    ev.preventDefault();
    const comment = input.value || '';
    hideComposer();
    onSend(comment);
  });
  cancel.addEventListener('click', ev => {
    ev.preventDefault();
    hideComposer();
    if (onCancel) onCancel();
  });
  document.documentElement.appendChild(composer);
  input.focus();
}

const canvas = document.createElement('canvas');
canvas.setAttribute('data-duck-draw-canvas', '1');
Object.assign(canvas.style, {
  position: 'fixed', inset: '0', width: '100vw', height: '100vh',
  zIndex: Z - 1, pointerEvents: 'none', background: 'transparent',
});
document.documentElement.appendChild(canvas);
const ctx2d = canvas.getContext('2d');
let sentStrokes = [];
let pendingStrokes = [];
let activeStroke = null;

function sizeCanvas() {
  const dpr = window.devicePixelRatio || 1;
  canvas.width = Math.max(1, Math.floor(window.innerWidth * dpr));
  canvas.height = Math.max(1, Math.floor(window.innerHeight * dpr));
  ctx2d.setTransform(dpr, 0, 0, dpr, 0, 0);
  redrawStrokes();
}
function drawStroke(stroke) {
  if (!stroke || stroke.length < 2) return;
  ctx2d.strokeStyle = RED;
  ctx2d.lineWidth = 3;
  ctx2d.lineCap = 'round';
  ctx2d.lineJoin = 'round';
  ctx2d.beginPath();
  ctx2d.moveTo(stroke[0].x, stroke[0].y);
  for (const p of stroke.slice(1)) ctx2d.lineTo(p.x, p.y);
  ctx2d.stroke();
}
function redrawStrokes() {
  ctx2d.clearRect(0, 0, window.innerWidth, window.innerHeight);
  for (const s of sentStrokes) drawStroke(s);
  for (const s of pendingStrokes) drawStroke(s);
}
function setMode(next) {
  mode = next;
  canvas.style.pointerEvents = mode === 'draw' ? 'auto' : 'none';
  highlightBtn.style.background = mode === 'highlight' ? '#5BC4A8' : '#1C2420';
  highlightBtn.style.color = mode === 'highlight' ? '#10201B' : '#DFFAF2';
  drawBtn.style.background = mode === 'draw' ? '#5BC4A8' : '#1C2420';
  drawBtn.style.color = mode === 'draw' ? '#10201B' : '#DFFAF2';
  hidePill();
}
function strokeRect(strokes) {
  let minX = Infinity, minY = Infinity, maxX = -Infinity, maxY = -Infinity;
  for (const s of strokes) for (const p of s) {
    minX = Math.min(minX, p.x); minY = Math.min(minY, p.y);
    maxX = Math.max(maxX, p.x); maxY = Math.max(maxY, p.y);
  }
  if (!isFinite(minX)) return { x: 0, y: 0, w: 0, h: 0 };
  return { x: minX, y: minY, w: maxX - minX, h: maxY - minY };
}
function sendDrawing(comment) {
  const strokes = pendingStrokes;
  if (!strokes.length) return;
  pendingStrokes = [];
  sentStrokes = sentStrokes.concat(strokes);
  redrawStrokes();
  if (window.duckMark) window.duckMark(JSON.stringify({
    type: 'drawing', url: location.href, comment, strokes, rect: strokeRect(strokes),
  }));
}

highlightBtn.addEventListener('click', () => setMode('highlight'));
drawBtn.addEventListener('click', () => setMode('draw'));
window.addEventListener('resize', sizeCanvas);
sizeCanvas();
setMode('highlight');

canvas.addEventListener('pointerdown', ev => {
  if (mode !== 'draw') return;
  ev.preventDefault();
  canvas.setPointerCapture(ev.pointerId);
  activeStroke = [{ x: ev.clientX, y: ev.clientY }];
  pendingStrokes.push(activeStroke);
});
canvas.addEventListener('pointermove', ev => {
  if (!activeStroke) return;
  ev.preventDefault();
  activeStroke.push({ x: ev.clientX, y: ev.clientY });
  redrawStrokes();
});
function endStroke(ev) {
  if (!activeStroke) return;
  ev.preventDefault();
  if (activeStroke.length === 1) activeStroke.push({ x: ev.clientX, y: ev.clientY });
  activeStroke = null;
  showComposer(sendDrawing, () => { pendingStrokes = []; redrawStrokes(); });
}
canvas.addEventListener('pointerup', endStroke);
canvas.addEventListener('pointercancel', endStroke);

document.addEventListener('mouseup', () => {
  setTimeout(() => {
    if (mode !== 'highlight') return;
    hidePill();
    const sel = window.getSelection();
    if (!sel || sel.isCollapsed || !sel.toString().trim()) return;
    const range = sel.getRangeAt(0).cloneRange();
    const r = range.getBoundingClientRect();
    pill = document.createElement('button');
    pill.textContent = 'Mark';
    Object.assign(pill.style, {
      position: 'fixed', left: (r.left + r.width / 2 - 28) + 'px',
      top: Math.max(4, r.top - 30) + 'px', zIndex: Z,
      background: '#1C2420', color: '#5BC4A8', border: '1px solid #5BC4A8',
      borderRadius: '999px', padding: '3px 10px', font: '12px ui-monospace,monospace',
      cursor: 'pointer',
    });
    pill.addEventListener('mousedown', ev => ev.preventDefault());
    pill.addEventListener('click', () => {
      const text = sel.toString();
      const ctx = context(range);
      showComposer((comment) => {
        paint(range);
        sel.removeAllRanges(); hidePill();
        if (window.duckMark) window.duckMark(JSON.stringify({
          type: 'highlight', url: location.href, text, comment, before: ctx.before, after: ctx.after,
        }));
      }, () => hidePill());
    });
    document.body.appendChild(pill);
  }, 0);
});

// Re-paint stored marks: first occurrence of each mark's text.
window.__duckApplyMarks = (marks) => {
  for (const m of marks || []) {
    if ((m.type && m.type !== 'highlight') || !m.text) continue;
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
