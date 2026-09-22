/**
 * logd - OpenAI Platform UI Interaction Logic
 */

document.addEventListener("DOMContentLoaded", () => {
  initClipboardHandlers();
  initConfirmHandlers();
  initDetailTabs();
  initTreeNavigation();
  initTreeContextMenu();
  initLogFilterAjax();
  initLogListAndDetail();
});

/* ==========================================================================
   Clipboard Copy Handlers
   ========================================================================== */

function initClipboardHandlers() {
  document.addEventListener("click", async (event) => {
    const copyButton = event.target.closest("[data-copy]");
    if (copyButton) {
      const target = document.querySelector(copyButton.dataset.copy);
      if (target) {
        const text = target.textContent.trim();
        if (text && text !== "-") {
          await copyText(text);
          triggerCopyFeedback(copyButton, "已复制");
        }
      }
      return;
    }

    const copyIdBtn = event.target.closest("#card-copy-id-btn");
    if (copyIdBtn) {
      const eventIdElem = document.getElementById("card-event-id");
      if (eventIdElem) {
        const text = eventIdElem.textContent.trim();
        if (text && text !== "-") {
          await copyText(text);
          triggerCopyFeedback(copyIdBtn, "已复制");
        }
      }
      return;
    }
  });
}

async function copyText(text) {
  try {
    await navigator.clipboard.writeText(text);
  } catch {
    const textarea = document.createElement("textarea");
    textarea.value = text;
    textarea.style.position = "fixed";
    textarea.style.opacity = "0";
    document.body.appendChild(textarea);
    textarea.select();
    document.execCommand("copy");
    document.body.removeChild(textarea);
  }
}

function triggerCopyFeedback(btn, feedbackText) {
  const original = btn.textContent;
  btn.textContent = feedbackText;
  btn.style.color = "var(--oai-green)";
  setTimeout(() => {
    btn.textContent = original;
    btn.style.color = "";
  }, 1600);
}

/* ==========================================================================
   Confirm Dialog Handlers
   ========================================================================== */

function initConfirmHandlers() {
  document.addEventListener("submit", (event) => {
    const submitter = event.submitter;
    const message = submitter?.dataset.confirm;
    if (message && !window.confirm(message)) {
      event.preventDefault();
    }
  });
}

/* ==========================================================================
   Detail Panel Tabs
   ========================================================================== */

function initDetailTabs() {
  const tabBtns = document.querySelectorAll(".detail-tab-btn");
  tabBtns.forEach((btn) => {
    btn.addEventListener("click", () => {
      const targetId = btn.dataset.tab;
      if (!targetId) return;

      tabBtns.forEach((b) => b.classList.remove("active"));
      btn.classList.add("active");

      const panes = document.querySelectorAll(".tab-pane");
      panes.forEach((p) => p.classList.remove("active"));

      const targetPane = document.getElementById(targetId);
      if (targetPane) {
        targetPane.classList.add("active");
      }
    });
  });
}

/* ==========================================================================
   Attribute Directory Tree Navigation
   ========================================================================== */

function initTreeNavigation() {
  const treeBody = document.getElementById("attribute-tree");
  if (!treeBody) return;

  const form = document.getElementById("log-filter-form");
  const projectInput = document.getElementById("filter-project");
  const nodeInput = document.getElementById("filter-node");
  const levelSelect = document.getElementById("filter-level");

  // Toggle node expand/collapse
  treeBody.addEventListener("click", (e) => {
    const chevron = e.target.closest(".tree-chevron");
    if (chevron) {
      e.stopPropagation();
      const node = chevron.closest(".tree-node");
      if (node) {
        node.classList.toggle("is-open");
        chevron.textContent = node.classList.contains("is-open") ? "▾" : "▸";
      }
      return;
    }

    const label = e.target.closest(".tree-label-content");
    if (label) {
      e.preventDefault();
      const project = label.dataset.project || "";
      const node = label.dataset.node || "";
      const level = label.dataset.level || "";

      if (projectInput) projectInput.value = project;
      if (nodeInput) nodeInput.value = node;
      if (levelSelect) levelSelect.value = level;

      if (form) submitLogFilters();
    }
  });

  // Reset button in tree
  const resetBtn = document.getElementById("tree-reset-btn");
  if (resetBtn) {
    resetBtn.addEventListener("click", () => {
      if (projectInput) projectInput.value = "";
      if (nodeInput) nodeInput.value = "";
      if (levelSelect) levelSelect.value = "";
      if (form) submitLogFilters();
    });
  }

  // Toggle all expand/collapse
  const toggleAllBtn = document.getElementById("tree-toggle-all");
  let allExpanded = true;
  if (toggleAllBtn) {
    toggleAllBtn.addEventListener("click", () => {
      allExpanded = !allExpanded;
      const nodes = treeBody.querySelectorAll(".tree-node:not(.is-leaf)");
      nodes.forEach((node) => {
        if (allExpanded) {
          node.classList.add("is-open");
          const btn = node.querySelector(":scope > .tree-row > .tree-chevron");
          if (btn) btn.textContent = "▾";
        } else {
          node.classList.remove("is-open");
          const btn = node.querySelector(":scope > .tree-row > .tree-chevron");
          if (btn) btn.textContent = "▸";
        }
      });
      toggleAllBtn.textContent = allExpanded ? "折叠全部" : "展开全部";
    });
  }

  // Search input in tree
  const searchInput = document.getElementById("tree-search-input");
  if (searchInput) {
    searchInput.addEventListener("input", () => {
      const q = searchInput.value.trim().toLowerCase();
      const nodes = treeBody.querySelectorAll(".tree-node");
      if (!q) {
        nodes.forEach((n) => (n.style.display = ""));
        return;
      }

      nodes.forEach((node) => {
        const row = node.querySelector(":scope > .tree-row");
        const text = row ? row.textContent.toLowerCase() : "";
        if (text.includes(q)) {
          node.style.display = "";
          let p = node.parentElement?.closest(".tree-node");
          while (p) {
            p.style.display = "";
            p.classList.add("is-open");
            const btn = p.querySelector(":scope > .tree-row > .tree-chevron");
            if (btn) btn.textContent = "▾";
            p = p.parentElement?.closest(".tree-node");
          }
        } else if (node.classList.contains("is-leaf")) {
          node.style.display = "none";
        }
      });
    });
  }
}

/* ==========================================================================
   Attribute Directory Context Menu
   ========================================================================== */

function initTreeContextMenu() {
  const treeBody = document.getElementById("attribute-tree");
  const menu = document.getElementById("tree-context-menu");
  const deleteButton = document.getElementById("tree-context-delete");
  if (!treeBody || !menu || !deleteButton) return;

  let scope = null;
  const hideMenu = () => {
    menu.hidden = true;
    scope = null;
  };

  treeBody.addEventListener("contextmenu", (event) => {
    const row = event.target.closest(".tree-row");
    const label = row?.querySelector(".tree-label-content");
    if (!row || !label) return;

    event.preventDefault();
    scope = {
      project: label.dataset.project || "",
      node: label.dataset.node || "",
      level: label.dataset.level || "",
    };

    menu.hidden = false;
    const left = Math.min(event.clientX, window.innerWidth - menu.offsetWidth - 8);
    const top = Math.min(event.clientY, window.innerHeight - menu.offsetHeight - 8);
    menu.style.left = `${Math.max(8, left)}px`;
    menu.style.top = `${Math.max(8, top)}px`;
  });

  document.addEventListener("click", (event) => {
    if (!menu.contains(event.target)) hideMenu();
  });
  document.addEventListener("keydown", (event) => {
    if (event.key === "Escape") hideMenu();
  });
  treeBody.addEventListener("scroll", hideMenu);
  window.addEventListener("blur", hideMenu);

  deleteButton.addEventListener("click", async () => {
    if (!scope) return;
    const target = { ...scope };
    hideMenu();
    if (!window.confirm(scopeDeleteMessage(target))) return;

    const body = new URLSearchParams({
      project_key: target.project,
      node_key: target.node,
      level: target.level,
    });
    try {
      const data = await postAdminAction(
        "/admin/logs/delete-scope",
        body.toString(),
        "application/x-www-form-urlencoded",
      );
      reloadAfterDelete(data.deleted, data.skipped_locked);
    } catch (err) {
      window.alert(err.message || "删除失败");
    }
  });
}

function scopeDeleteMessage(scope) {
  let target = scope.project;
  if (scope.node) target += " / " + scope.node;
  if (scope.level) target += " / " + scope.level;
  return `确认删除目录「${target}」及其所有下级目录和日志？已锁定的日志不会被删除，但目录仍会被移除。此操作不可恢复。`;
}

/* ==========================================================================
   Log Filter AJAX Navigation
   ========================================================================== */

let activeLogListController = null;

function initLogFilterAjax() {
  const form = document.getElementById("log-filter-form");
  if (!form) return;

  form.addEventListener("submit", (event) => {
    event.preventDefault();
    submitLogFilters();
  });

  const resetBtn = document.getElementById("log-filter-reset");
  resetBtn?.addEventListener("click", () => {
    [
      "start_time",
      "end_time",
      "project_key",
      "node_key",
      "level",
      "error_scene",
      "member",
      "session_id",
      "request_ip",
    ].forEach((name) => {
      const input = form.elements.namedItem(name);
      if (input) input.value = "";
    });
    submitLogFilters();
  });
}

function submitLogFilters() {
  const form = document.getElementById("log-filter-form");
  if (!form) return;

  const params = new URLSearchParams(new FormData(form));
  ["cursor", "trail", "deleted", "skipped_locked"].forEach((key) => params.delete(key));
  const url = new URL(form.getAttribute("action") || "/admin/logs", window.location.origin);
  url.search = params.toString();
  loadLogList(url.toString());
}

async function loadLogList(url) {
  const table = document.getElementById("logs-table");
  if (!table) return;

  if (activeLogListController) activeLogListController.abort();
  const controller = new AbortController();
  activeLogListController = controller;
  table.dataset.listVersion = String((Number(table.dataset.listVersion) || 0) + 1);

  try {
    const response = await fetch(url, {
      headers: { Accept: "text/html" },
      signal: controller.signal,
    });
    if (!response.ok) {
      throw new Error(`HTTP ${response.status}`);
    }

    const html = await response.text();
    const doc = new DOMParser().parseFromString(html, "text/html");
    const newBody = doc.querySelector("#logs-table tbody");
    const body = table.querySelector("tbody");
    if (!newBody || !body) {
      throw new Error("日志列表响应无效");
    }

    body.replaceChildren(...Array.from(newBody.children));

    const sentinel = document.getElementById("logs-load-more");
    const newSentinel = doc.getElementById("logs-load-more");
    if (sentinel) {
      sentinel.dataset.nextUrl = newSentinel?.dataset.nextUrl || "";
      sentinel.hidden = !sentinel.dataset.nextUrl;
    }

    const newCount = doc.getElementById("logs-count");
    const count = document.getElementById("logs-count");
    if (newCount && count) count.textContent = newCount.textContent;

    const container = table.closest(".logs-table-container");
    if (container) container.scrollTop = 0;

    history.replaceState(null, "", url);
    updateTreeActiveState();
    updateLogSelectionUI(table);
    table.dispatchEvent(new CustomEvent("logs:reloaded"));
  } catch (err) {
    if (err.name === "AbortError") return;
    console.error("Failed to filter logs:", err);
    window.alert("加载日志失败：" + (err.message || "未知错误"));
  } finally {
    if (activeLogListController === controller) activeLogListController = null;
  }
}

function updateTreeActiveState() {
  const form = document.getElementById("log-filter-form");
  const tree = document.getElementById("attribute-tree");
  if (!form || !tree) return;

  const project = form.elements.namedItem("project_key")?.value || "";
  const node = form.elements.namedItem("node_key")?.value || "";
  const level = form.elements.namedItem("level")?.value || "";

  tree.querySelectorAll(".tree-label-content").forEach((label) => {
    const matches =
      (label.dataset.project || "") === project &&
      (label.dataset.node || "") === node &&
      (label.dataset.level || "") === level;
    label.closest(".tree-row")?.classList.toggle("active", matches);
  });
}

/* ==========================================================================
   Logs Table & Detail Card Interaction
   ========================================================================== */

function initLogListAndDetail() {
  const table = document.getElementById("logs-table");
  if (!table) return;

  initColumnResizing(table);

  const tableBody = table.querySelector("tbody");
  let currentRow = null;
  let dragSelecting = false;
  let dragSelectChecked = true;
  let dragSelectMoved = false;
  let dragSelectLastRow = null;

  tableBody?.addEventListener("click", async (e) => {
    const lockButton = e.target.closest(".log-lock-btn");
    if (lockButton) {
      e.preventDefault();
      e.stopPropagation();
      const row = lockButton.closest(".log-row");
      if (row) {
        await setLogRowsLocked(table, [row], !isLogRowLocked(row));
      }
      return;
    }

    if (dragSelectMoved) {
      dragSelectMoved = false;
      return;
    }
    if (e.target.closest("a") || e.target.closest("button") || e.target.closest("input")) {
      return;
    }
    const row = e.target.closest(".log-row");
    if (!row) return;
    currentRow = row;
    selectLogRow(row);
  });

  tableBody?.addEventListener("change", (e) => {
    const checkbox = e.target.closest(".log-select-checkbox");
    if (!checkbox) return;
    const row = checkbox.closest(".log-row");
    if (row) row.classList.toggle("is-checked", checkbox.checked);
    updateLogSelectionUI(table);
  });

  tableBody?.addEventListener("mousedown", (e) => {
    if (e.button !== 0 || e.target.closest("a, button, input")) return;
    const row = e.target.closest(".log-row");
    const checkbox = row?.querySelector(".log-select-checkbox");
    if (!row || !checkbox || checkbox.disabled) return;

    dragSelecting = true;
    dragSelectChecked = !checkbox.checked;
    dragSelectMoved = false;
    dragSelectLastRow = row;
    document.body.classList.add("is-log-dragging");
    e.preventDefault();
  });

  tableBody?.addEventListener("mouseover", (e) => {
    if (!dragSelecting) return;
    const row = e.target.closest(".log-row");
    if (!row || row === dragSelectLastRow) return;

    if (!dragSelectMoved && dragSelectLastRow) {
      setLogRowChecked(dragSelectLastRow, dragSelectChecked);
    }
    dragSelectMoved = true;
    dragSelectLastRow = row;
    setLogRowChecked(row, dragSelectChecked);
    updateLogSelectionUI(table);
  });

  document.addEventListener("mouseup", () => {
    if (!dragSelecting) return;
    dragSelecting = false;
    dragSelectLastRow = null;
    document.body.classList.remove("is-log-dragging");
  });

  const selectAll = document.getElementById("logs-select-all");
  selectAll?.addEventListener("change", () => {
    const checked = selectAll.checked;
    getLogRows(table).forEach((row) => {
      setLogRowChecked(row, checked);
    });
    updateLogSelectionUI(table);
  });

  // Keyboard navigation
  document.addEventListener("keydown", (e) => {
    if (["input", "textarea", "select"].includes(document.activeElement?.tagName?.toLowerCase())) {
      return;
    }
    const rows = getLogRows(table);
    if (rows.length === 0) return;

    let currentIndex = currentRow ? rows.indexOf(currentRow) : -1;
    if (e.key === "ArrowDown") {
      if (currentIndex < rows.length - 1) {
        e.preventDefault();
        currentIndex++;
        currentRow = rows[currentIndex];
        selectLogRow(currentRow);
        currentRow.scrollIntoView({ block: "nearest" });
      }
    } else if (e.key === "ArrowUp") {
      if (currentIndex > 0) {
        e.preventDefault();
        currentIndex--;
        currentRow = rows[currentIndex];
        selectLogRow(currentRow);
        currentRow.scrollIntoView({ block: "nearest" });
      }
    }
  });

  const initialRows = getLogRows(table);
  if (initialRows.length > 0) {
    currentRow = initialRows[0];
    selectLogRow(currentRow);
  }

  table.addEventListener("logs:reloaded", () => {
    currentRow = null;
    const rows = getLogRows(table);
    if (rows.length > 0) {
      currentRow = rows[0];
      selectLogRow(currentRow);
    } else {
      clearLogDetail();
    }
  });

  initInfiniteLogScroll(table);
  initLogDeleteActions(table);
  updateLogSelectionUI(table);
}

function getLogRows(table) {
  return Array.from(table.querySelectorAll("tbody .log-row"));
}

function setLogRowChecked(row, checked) {
  const checkbox = row.querySelector(".log-select-checkbox");
  if (!checkbox) return;
  checkbox.checked = checked;
  row.classList.toggle("is-checked", checked);
}

function logRowRef(row) {
  return {
    event_id: row.dataset.eventId,
    level: row.dataset.level,
    event_time: row.dataset.eventTime,
  };
}

function isLogRowLocked(row) {
  return row.dataset.locked === "true";
}

function initInfiniteLogScroll(table) {
  const container = table.closest(".logs-table-container");
  const sentinel = document.getElementById("logs-load-more");
  if (!container || !sentinel) return;

  let loading = false;

  const observer = new IntersectionObserver((entries) => {
    if (entries.some((entry) => entry.isIntersecting)) {
      loadNextPage();
    }
  }, {
    root: container,
    rootMargin: "240px 0px",
  });

  const isNearBottom = () => {
    const containerRect = container.getBoundingClientRect();
    const sentinelRect = sentinel.getBoundingClientRect();
    return sentinelRect.top <= containerRect.bottom + 240;
  };

  async function loadNextPage() {
    if (loading) return;

    const nextURL = sentinel.dataset.nextUrl;
    if (!nextURL) {
      sentinel.hidden = true;
      return;
    }

    const listVersion = table.dataset.listVersion || "";
    loading = true;
    sentinel.hidden = false;
    const status = sentinel.querySelector("span");
    if (status) status.textContent = "加载中...";
    let failed = false;

    try {
      const response = await fetch(nextURL, {
        headers: { Accept: "text/html" },
      });
      if (!response.ok) {
        throw new Error(`HTTP ${response.status}`);
      }

      const html = await response.text();
      const doc = new DOMParser().parseFromString(html, "text/html");
      const body = table.querySelector("tbody");
      const newRows = Array.from(doc.querySelectorAll("#logs-table tbody .log-row"));
      if ((table.dataset.listVersion || "") !== listVersion) return;

      if (body && newRows.length > 0) {
        body.append(...newRows);
      }

      const nextSentinel = doc.getElementById("logs-load-more");
      sentinel.dataset.nextUrl = nextSentinel?.dataset.nextUrl || "";
      updateLogCount(table);
      updateLogSelectionUI(table);

      if (!sentinel.dataset.nextUrl) {
        sentinel.hidden = true;
      }
    } catch (err) {
      console.error("Failed to load next log page:", err);
      if (status) status.textContent = "加载失败，滚动后重试";
      failed = true;
    } finally {
      loading = false;
    }

    if (!failed && sentinel.dataset.nextUrl) {
      requestAnimationFrame(() => {
        if (isNearBottom()) loadNextPage();
      });
    }
  }

  observer.observe(sentinel);
}

function updateLogCount(table) {
  const count = document.getElementById("logs-count");
  if (count) {
    count.textContent = String(getLogRows(table).length);
  }
}

function updateLogSelectionUI(table) {
  const rows = getLogRows(table);
  const selected = rows.filter((row) => row.querySelector(".log-select-checkbox")?.checked);

  const selectAll = document.getElementById("logs-select-all");
  if (selectAll) {
    selectAll.checked = rows.length > 0 && selected.length === rows.length;
    selectAll.indeterminate = selected.length > 0 && selected.length < rows.length;
    selectAll.disabled = rows.length === 0;
  }

  const button = document.getElementById("batch-delete-btn");
  if (button) {
    button.disabled = selected.length === 0;
    button.textContent = selected.length > 0 ? `批量删除 (${selected.length})` : "批量删除";
  }
}

function initColumnResizing(table) {
  const headers = Array.from(table.querySelectorAll("thead th"));
  if (headers.length === 0 || table.querySelector("colgroup[data-column-resize]")) return;

  const colgroup = document.createElement("colgroup");
  colgroup.dataset.columnResize = "true";

  const widths = headers.map((header, index) => {
    const width = Math.round(header.getBoundingClientRect().width);
    const column = document.createElement("col");
    column.style.width = `${width}px`;
    colgroup.appendChild(column);

    const handle = document.createElement("span");
    handle.className = "column-resizer";
    handle.dataset.columnIndex = String(index);
    handle.setAttribute("aria-hidden", "true");
    header.appendChild(handle);
    return width;
  });

  table.insertBefore(colgroup, table.firstChild);

  const syncTableWidth = () => {
    const totalWidth = widths.reduce((sum, width) => sum + width, 0);
    table.style.width = `${totalWidth}px`;
    table.style.minWidth = "100%";
  };
  syncTableWidth();

  table.querySelector("thead")?.addEventListener("pointerdown", (event) => {
    const handle = event.target.closest(".column-resizer");
    if (!handle) return;

    event.preventDefault();
    const index = Number(handle.dataset.columnIndex);
    const startX = event.clientX;
    const startWidth = widths[index];
    const minWidth = index === 0 ? 44 : 56;

    handle.setPointerCapture(event.pointerId);
    document.body.classList.add("is-column-resizing");

    const onPointerMove = (moveEvent) => {
      const width = Math.max(minWidth, Math.round(startWidth + moveEvent.clientX - startX));
      widths[index] = width;
      colgroup.children[index].style.width = `${width}px`;
      syncTableWidth();
    };

    const stopResizing = () => {
      handle.removeEventListener("pointermove", onPointerMove);
      handle.removeEventListener("pointerup", stopResizing);
      handle.removeEventListener("pointercancel", stopResizing);
      document.body.classList.remove("is-column-resizing");
    };

    handle.addEventListener("pointermove", onPointerMove);
    handle.addEventListener("pointerup", stopResizing);
    handle.addEventListener("pointercancel", stopResizing);
  });
}

function initLogDeleteActions(table) {
  const form = document.getElementById("log-filter-form");
  const filterButton = document.getElementById("delete-filter-btn");
  const batchButton = document.getElementById("batch-delete-btn");

  filterButton?.addEventListener("click", async () => {
    if (!form) return;
    const body = new URLSearchParams(new FormData(form));
    const hasFilters = [
      "start_time",
      "end_time",
      "project_key",
      "node_key",
      "level",
      "error_scene",
      "member",
      "session_id",
      "request_ip",
    ].some((key) => body.get(key));
    const message = hasFilters
      ? "确认删除当前筛选条件下的所有日志？已锁定的日志不会被删除，需先解锁。此操作不可恢复。"
      : "当前没有筛选条件，将删除全部日志。已锁定的日志不会被删除，需先解锁。确认继续？";
    if (!window.confirm(message)) return;

    try {
      const data = await postAdminAction(
        "/admin/logs/delete-filter",
        body.toString(),
        "application/x-www-form-urlencoded",
      );
      reloadAfterDelete(data.deleted, data.skipped_locked);
    } catch (err) {
      window.alert(err.message || "删除失败");
    }
  });

  batchButton?.addEventListener("click", async () => {
    const rows = getLogRows(table)
      .filter((row) => row.querySelector(".log-select-checkbox")?.checked);
    await deleteSelectedLogRows(table, rows);
  });

  initLogContextMenu(table);
}

function getSelectedLogRows(table) {
  return getLogRows(table)
    .filter((row) => row.querySelector(".log-select-checkbox")?.checked);
}

async function deleteSelectedLogRows(table, rows) {
  if (rows.length === 0) return;
  const lockedCount = rows.filter(isLogRowLocked).length;
  const lockedHint = lockedCount > 0
    ? `其中 ${lockedCount} 条已锁定记录会被跳过。`
    : "";
  if (!window.confirm(
    `确认删除选中的 ${rows.length} 条日志？${lockedHint}此操作不可恢复。`,
  )) return;

  try {
    const data = await postAdminAction(
      "/admin/logs/delete-selected",
      JSON.stringify({ items: rows.map(logRowRef) }),
      "application/json",
    );
    reloadAfterDelete(data.deleted, data.skipped_locked);
  } catch (err) {
    window.alert(err.message || "删除失败");
  }
}

function initLogContextMenu(table) {
  const tableBody = table.querySelector("tbody");
  const container = table.closest(".logs-table-container");
  const menu = document.getElementById("log-context-menu");
  const deleteButton = document.getElementById("log-context-delete");
  const lockButton = document.getElementById("log-context-lock");
  if (!tableBody || !container || !menu || !deleteButton || !lockButton) return;

  let targetRows = [];
  const hideMenu = () => {
    menu.hidden = true;
    targetRows = [];
  };

  tableBody.addEventListener("contextmenu", (event) => {
    const row = event.target.closest(".log-row");
    if (!row) return;

    event.preventDefault();
    const selected = getSelectedLogRows(table);
    targetRows = selected.includes(row) ? selected : [row];

    const allLocked = targetRows.every(isLogRowLocked);
    deleteButton.textContent = `删除 ${targetRows.length} 条日志`;
    lockButton.textContent = allLocked
      ? `解锁 ${targetRows.length} 条记录`
      : `锁定 ${targetRows.length} 条记录`;
    lockButton.dataset.locked = String(!allLocked);

    menu.hidden = false;
    const left = Math.min(event.clientX, window.innerWidth - menu.offsetWidth - 8);
    const top = Math.min(event.clientY, window.innerHeight - menu.offsetHeight - 8);
    menu.style.left = `${Math.max(8, left)}px`;
    menu.style.top = `${Math.max(8, top)}px`;
  });

  document.addEventListener("click", (event) => {
    if (!menu.contains(event.target)) hideMenu();
  });
  document.addEventListener("keydown", (event) => {
    if (event.key === "Escape") hideMenu();
  });
  container.addEventListener("scroll", hideMenu);
  window.addEventListener("blur", hideMenu);

  deleteButton.addEventListener("click", async () => {
    const rows = targetRows.slice();
    hideMenu();
    await deleteSelectedLogRows(table, rows);
  });

  lockButton.addEventListener("click", async () => {
    const rows = targetRows.slice();
    const locked = lockButton.dataset.locked === "true";
    hideMenu();
    await setLogRowsLocked(table, rows, locked);
  });
}

async function setLogRowsLocked(table, rows, locked) {
  if (rows.length === 0) return;

  try {
    await postAdminAction(
      "/admin/logs/lock-selected",
      JSON.stringify({ items: rows.map(logRowRef), locked }),
      "application/json",
    );
    rows.forEach((row) => applyLogRowLocked(row, locked));
    updateLogSelectionUI(table);
  } catch (err) {
    window.alert(err.message || "更新锁定状态失败");
  }
}

function applyLogRowLocked(row, locked) {
  row.dataset.locked = String(locked);
  row.classList.toggle("is-locked", locked);

  const button = row.querySelector(".log-lock-btn");
  if (button) {
    const label = locked ? "解锁记录" : "锁定记录";
    button.title = label;
    button.setAttribute("aria-label", label);
    button.setAttribute("aria-pressed", String(locked));
  }
}

async function postAdminAction(url, body, contentType) {
  const form = document.getElementById("log-filter-form");
  const response = await fetch(url, {
    method: "POST",
    headers: {
      Accept: "application/json",
      "Content-Type": contentType,
      "X-CSRF-Token": form?.dataset.csrf || "",
    },
    body,
  });

  let data = {};
  try {
    data = await response.json();
  } catch {
    // Keep the HTTP status fallback below.
  }
  if (!response.ok) {
    throw new Error(data.error?.message || `HTTP ${response.status}`);
  }
  return data;
}

function reloadAfterDelete(count, skippedLocked) {
  const url = new URL(window.location.href);
  url.searchParams.delete("cursor");
  url.searchParams.delete("trail");
  url.searchParams.set("deleted", String(count || 0));
  if (skippedLocked) {
    url.searchParams.set("skipped_locked", String(skippedLocked));
  } else {
    url.searchParams.delete("skipped_locked");
  }
  window.location.assign(url.toString());
}

function selectLogRow(row) {
  const allRows = document.querySelectorAll("#logs-table tbody .log-row");
  allRows.forEach((r) => r.classList.remove("is-selected"));
  row.classList.add("is-selected");

  const eventId = row.dataset.eventId;
  const level = row.dataset.level;
  const eventTime = row.dataset.eventTime;
  const detailUrl = row.dataset.detailUrl;

  const newWindowBtn = document.getElementById("card-open-new-window");
  if (newWindowBtn) {
    newWindowBtn.href = detailUrl || `/admin/logs/${eventId}?level=${encodeURIComponent(level)}&event_time=${encodeURIComponent(eventTime)}`;
  }

  fetchLogDetail(eventId, level, eventTime);
}

function clearLogDetail() {
  if (activeFetchController) {
    activeFetchController.abort();
    activeFetchController = null;
  }

  const loadingElem = document.getElementById("card-loading-state");
  if (loadingElem) loadingElem.style.display = "none";

  const newWindowBtn = document.getElementById("card-open-new-window");
  if (newWindowBtn) newWindowBtn.href = "#";

  const badge = document.getElementById("card-level-badge");
  if (badge) {
    badge.textContent = "-";
    badge.className = "level";
  }

  [
    "card-event-id",
    "card-received-at",
    "card-scene",
    "card-member",
    "card-session",
    "card-url",
    "card-message",
    "card-location",
    "card-file",
    "card-line",
    "card-error-stack",
    "card-request-params",
    "card-request-headers",
  ].forEach((id) => setFieldText(id, "-"));
}

let activeFetchController = null;

async function fetchLogDetail(eventId, level, eventTime) {
  const loadingElem = document.getElementById("card-loading-state");

  if (activeFetchController) {
    activeFetchController.abort();
  }
  activeFetchController = new AbortController();

  if (loadingElem) loadingElem.style.display = "flex";

  try {
    const query = new URLSearchParams({
      level: level,
      event_time: eventTime,
    });
    const res = await fetch(`/api/v1/logs/${encodeURIComponent(eventId)}?${query.toString()}`, {
      headers: { Accept: "application/json" },
      signal: activeFetchController.signal,
    });

    if (!res.ok) throw new Error(`HTTP ${res.status}`);

    const data = await res.json();
    populateDetail(data);

    if (loadingElem) loadingElem.style.display = "none";
  } catch (err) {
    if (err.name === "AbortError") return;
    console.error("Failed to load detail:", err);
    if (loadingElem) loadingElem.style.display = "none";
  }
}

function populateDetail(item) {
  setFieldText("card-event-id", item.event_id || "-");

  const badge = document.getElementById("card-level-badge");
  if (badge) {
    badge.textContent = item.level || "-";
    badge.className = `level level-${item.level || "INFO"}`;
  }

  setFieldText("card-received-at", formatDateTime(item.received_at));
  setFieldText("card-scene", item.error_scene || "-");
  setFieldText("card-member", item.member || "-");
  setFieldText("card-session", item.session_id || "-");
  setFieldText("card-url", (item.request_method ? item.request_method + " " : "") + (item.request_url || "-"));
  setFieldText("card-message", item.error_message || "-");

  const location = item.error_file ? (item.error_file + (item.error_line != null ? ":" + item.error_line : "")) : "-";
  setFieldText("card-location", location);
  setFieldText("card-file", item.error_file || "-");
  setFieldText("card-line", item.error_line != null ? String(item.error_line) : "-");

  setFieldText("card-error-stack", item.error_stack || "（无错误堆栈）");
  setFieldText("card-request-params", formatJSON(item.request_params));
  setFieldText("card-request-headers", formatJSON(item.request_headers));
}

function setFieldText(id, text) {
  const el = document.getElementById(id);
  if (el) el.textContent = text;
}

function formatJSON(val) {
  if (!val) return "-";
  if (typeof val === "string") {
    try {
      const parsed = JSON.parse(val);
      return JSON.stringify(parsed, null, 2);
    } catch {
      return val;
    }
  }
  return JSON.stringify(val, null, 2);
}

function formatDateTime(val) {
  if (!val) return "-";
  try {
    const d = new Date(val);
    if (isNaN(d.getTime())) return val;
    const pad = (n) => String(n).padStart(2, "0");
    const pad3 = (n) => String(n).padStart(3, "0");
    return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}.${pad3(d.getMilliseconds())}`;
  } catch {
    return val;
  }
}
