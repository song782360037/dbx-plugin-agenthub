(() => {
  "use strict";

  const $ = (id) => document.getElementById(id);
  const dbx = window.dbxPlugin;
  const views = ["overview", "library", "mcp", "install", "settings"];
  const viewTitles = { overview: "概览", library: "技能库", mcp: "MCP 服务", install: "安装", settings: "设置" };
  const statusMeta = {
    synced: { label: "已同步", className: "good" },
    partial: { label: "部分同步", className: "warn" },
    update: { label: "有更新", className: "warn" },
    modified: { label: "本地已修改", className: "warn" },
    conflict: { label: "冲突", className: "bad" },
    untracked: { label: "未纳管", className: "neutral" },
    hub_only: { label: "仅中心库", className: "neutral" },
    missing: { label: "中心库缺失", className: "bad" }
  };

  const state = {
    overview: null,
    mcpOverview: null,
    view: "overview",
    loading: false,
    overviewRequest: 0,
    query: "",
    statusFilter: "all",
    managedOnly: false,
    mcpQuery: "",
    mcpStatusFilter: "all",
    mcpDetail: null,
    mcpMode: "edit",
    mcpSelectedTargets: new Set(),
    mcpImportServers: [],
    mcpImportIndex: 0,
    mcpLoading: false,
    selectedSkill: null,
    detail: null,
    detailName: "",
    detailTab: "readme",
    detailRaw: false,
    fileView: null,
    sourceType: "github",
    zipBase64: "",
    zipName: "",
    zipSize: 0,
    preview: null,
    previewRequest: 0,
    selectedCandidates: new Set(),
    selectedTargets: new Set(),
    previewDetail: "",
    installMode: "auto",
    installOverwrite: false,
    installAck: false,
    settingsLoaded: false,
    settingsDirty: false,
    settingsRevision: 0,
    drawerRequest: 0,
    fileRequest: 0,
    importRequest: 0,
    mcpSourceAgentId: "",
    overlayStack: [],
    busy: false,
    modalResolve: null,
    toastTimer: null
  };

  function invoke(method, params) {
    if (!dbx || typeof dbx.invoke !== "function") {
      return Promise.reject(new Error("未检测到 DBX 插件宿主"));
    }
    return dbx.invoke(method, params || {}, { timeoutMs: 120000 });
  }

  function h(tag, props, ...children) {
    const node = document.createElement(tag);
    if (props) {
      for (const [key, value] of Object.entries(props)) {
        if (value === null || value === undefined || value === false) continue;
        if (key === "class") node.className = value;
        else if (key === "text") node.textContent = value;
        else if (key === "value") node.value = value;
        else if (key === "checked" || key === "disabled" || key === "selected" || key === "hidden") node[key] = Boolean(value);
        else if (key === "dataset") Object.assign(node.dataset, value);
        else if (key === "style") Object.assign(node.style, value);
        else if (key.startsWith("on") && typeof value === "function") node.addEventListener(key.slice(2).toLowerCase(), value);
        else node.setAttribute(key, String(value));
      }
    }
    for (const child of children.flat(4)) {
      if (child === null || child === undefined || child === false) continue;
      node.append(child instanceof Node ? child : document.createTextNode(String(child)));
    }
    if (props?.onclick && /^(compact-row|skill-row|file-row|candidate)(\s|$)/.test(props.class || "")) {
      node.tabIndex = 0;
      node.setAttribute("role", /^(compact-row|file-row)$/.test(props.class) ? "button" : "group");
      node.addEventListener("keydown", (event) => {
        if (event.target !== node || !["Enter", " "].includes(event.key)) return;
        event.preventDefault();
        node.click();
      });
    }
    return node;
  }

  function clear(node) {
    if (node) node.replaceChildren();
  }

  function overlayFocusables(node) {
    return [...node.querySelectorAll('button, input, select, textarea, a[href], summary, [tabindex]')]
      .filter((item) => !item.disabled && item.tabIndex >= 0 && !item.closest("[inert]") && item.getClientRects().length);
  }

  function syncOverlays() {
    const top = state.overlayStack[state.overlayStack.length - 1];
    document.querySelector(".app-shell").inert = Boolean(top);
    for (const id of ["detail-drawer", "mcp-drawer", "modal", "mcp-import-modal"]) {
      $(id).inert = Boolean(top && id !== top.id);
    }
    document.body.style.overflow = top ? "hidden" : "";
  }

  function openOverlay(id) {
    const existing = state.overlayStack.find((item) => item.id === id);
    $(id).hidden = false;
    if (!existing) state.overlayStack.push({ id, returnFocus: document.activeElement });
    syncOverlays();
    if (!existing) (overlayFocusables($(id))[0] || $(id)).focus();
  }

  function closeOverlay(id) {
    const index = state.overlayStack.findIndex((item) => item.id === id);
    const wasTop = index === state.overlayStack.length - 1;
    const entry = index < 0 ? null : state.overlayStack.splice(index, 1)[0];
    $(id).hidden = true;
    syncOverlays();
    if (!entry || !wasTop) return;
    const top = state.overlayStack[state.overlayStack.length - 1];
    const previous = entry.returnFocus;
    if (previous?.isConnected && !previous.closest("[inert]") && previous.getClientRects().length && !previous.disabled) previous.focus();
    else if (top) (overlayFocusables($(top.id))[0] || $(top.id)).focus();
    else document.querySelector('.section-menu [aria-selected="true"]')?.focus();
  }

  function errorText(error) {
    if (error instanceof Error) return error.message;
    if (error && typeof error === "object" && error.message) return String(error.message);
    return String(error || "未知错误");
  }

  function formatDate(value) {
    if (!value) return "—";
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return "—";
    return date.toLocaleString("zh-CN", { year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", hour12: false });
  }

  function formatRelative(value) {
    if (!value) return "—";
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return "—";
    const seconds = Math.round((date.getTime() - Date.now()) / 1000);
    const magnitude = Math.abs(seconds);
    let amount;
    if (magnitude < 60) amount = "不到 1 分钟";
    else if (magnitude < 3600) amount = `${Math.round(magnitude / 60)} 分钟`;
    else if (magnitude < 86400) amount = `${Math.round(magnitude / 3600)} 小时`;
    else amount = `${Math.round(magnitude / 86400)} 天`;
    return seconds >= 0 ? `${amount}后` : `${amount}前`;
  }

  function formatBytes(value) {
    const bytes = Number(value) || 0;
    if (bytes < 1024) return `${bytes} B`;
    if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KiB`;
    return `${(bytes / 1024 / 1024).toFixed(1)} MiB`;
  }

  function shortHash(value) {
    return value ? String(value).slice(0, 10) : "—";
  }

  function agentName(id) {
    const agent = state.overview?.agents?.find((item) => item.id === id);
    return agent?.name || id;
  }

  function statusBadge(status, textOverride) {
    const meta = statusMeta[status] || { label: textOverride || status || "未知", className: "neutral" };
    return h("span", { class: `badge ${meta.className}` }, textOverride || meta.label);
  }

  function statusPriority(status) {
    return { update: 0, conflict: 1, modified: 2, missing: 3, partial: 4, hub_only: 5, untracked: 6, synced: 7 }[status] ?? 8;
  }

  function sourceLabel(view) {
    const source = view.source;
    if (!source) return "来源未知";
    switch (source.type) {
      case "github":
        return `${source.repo || "GitHub"}${source.ref || source.resolvedRef ? `@${source.ref || source.resolvedRef}` : ""}`;
      case "local":
        return `本地 · ${source.localPath || source.label || ""}`;
      case "zip":
        return `压缩包 · ${source.archiveName || source.label || ""}`;
      case "url":
        try { return `URL · ${new URL(source.url).host}`; } catch { return "URL"; }
      case "adopted":
        return `纳入管理 · ${source.label || ""}`;
      default:
        return source.label || source.type;
    }
  }

  function sourceTitle(view) {
    const source = view.source;
    if (!source) return "";
    if (source.type === "github") return [source.repo, source.ref || source.resolvedRef, source.path].filter(Boolean).join(" · ");
    return source.localPath || source.url || source.archiveName || source.label || "";
  }

  function targetChips(targets, limit = 6) {
    const wrap = h("div", { class: "target-chips" });
    const list = Array.isArray(targets) ? targets : [];
    for (const target of list.slice(0, limit)) {
      const classes = ["chip"];
      if (target.managed) classes.push("managed");
      if (target.modified) classes.push("modified");
      if (!target.managed) classes.push("untracked");
      wrap.append(h("span", {
        class: classes.join(" "),
        title: `${target.path || ""}${target.mode ? ` · ${target.mode}` : ""}${target.exists === false ? " · 未安装" : ""}`
      }, target.agentName || agentName(target.agentId)));
    }
    if (list.length > limit) wrap.append(h("span", { class: "chip" }, `+${list.length - limit}`));
    if (!list.length) wrap.append(h("span", { class: "subtle" }, "未安装"));
    return wrap;
  }

  function showToast(message, isError = false) {
    const toast = $("toast");
    toast.textContent = message || "";
    toast.className = `toast${isError ? " error" : ""}`;
    toast.hidden = !message;
    clearTimeout(state.toastTimer);
    if (message) state.toastTimer = setTimeout(() => { toast.hidden = true; }, isError ? 8000 : 4500);
  }

  function showNotice(message, kind = "") {
    const notice = $("notice");
    notice.textContent = message || "";
    notice.className = `notice ${kind}`.trim();
    notice.hidden = !message;
  }

  function showWarning(message) {
    const banner = $("warning-banner");
    banner.textContent = message || "";
    banner.hidden = !message;
  }

  function showMCPWarning(message) {
    const banner = $("mcp-warning");
    banner.textContent = message || "";
    banner.hidden = !message;
  }

  function setStatus(id, value) {
    const node = $(id);
    if (node) node.textContent = value || "";
  }

  function setBusy(button, busy, label) {
    state.busy = busy;
    if (button) {
      button.disabled = busy;
      if (busy) {
        button.dataset.label = button.textContent;
        button.textContent = label || "处理中…";
      } else if (button.dataset.label) {
        button.textContent = button.dataset.label;
        delete button.dataset.label;
      }
    }
  }

  async function loadOverview(options = {}) {
    if (state.loading && !options.replacePending) return false;
    const request = ++state.overviewRequest;
    state.loading = true;
    if (!options.silent) setStatus("sidebar-summary", "正在扫描…");
    try {
      const data = await invoke("skills/getOverview");
      if (request !== state.overviewRequest) return false;
      state.overview = data;
      if (!state.settingsLoaded) {
        state.installMode = data.settings?.defaultMode || "auto";
        state.settingsLoaded = true;
      }
      const agentIds = new Set((data.agents || []).map((agent) => agent.id));
      for (const id of [...state.selectedTargets]) {
        if (!agentIds.has(id)) state.selectedTargets.delete(id);
      }
      renderAll();
      if (data.warnings?.length) showWarning(data.warnings.join("；"));
      else showWarning("");
      return true;
    } catch (error) {
      if (request !== state.overviewRequest) return false;
      showToast(`扫描失败：${errorText(error)}`, true);
      setStatus("sidebar-summary", "扫描失败");
      return false;
    } finally {
      if (request === state.overviewRequest) state.loading = false;
    }
  }

  async function loadMCP(options = {}) {
    if (state.mcpLoading) return;
    state.mcpLoading = true;
    try {
      const data = await invoke("mcp/getOverview");
      state.mcpOverview = data;
      renderMCP();
      renderStats();
      renderNav();
      renderOverviewMCP();
      showMCPWarning(data.warnings?.length ? data.warnings.join("；") : "");
    } catch (error) {
      if (!options.silent) showToast(`MCP 扫描失败：${errorText(error)}`, true);
    } finally {
      state.mcpLoading = false;
    }
  }

  async function refreshAll(options = {}) {
    await Promise.allSettled([loadOverview(options), loadMCP(options)]);
  }

  function renderAll() {
    if (!state.overview) return;
    renderStats();
    renderClients();
    renderAttention();
    renderRecent();
    renderHub();
    renderLibrary();
    renderNav();
    renderOverviewMCP();
    renderMCP();
    if (state.view === "settings") renderSettings();
    if (state.view === "install") renderPreview();
  }

  function renderNav() {
    const skills = state.overview?.skills || [];
    $("nav-skill-count").textContent = String(skills.length);
    $("nav-skill-count").hidden = skills.length === 0;
    const mcpCount = state.mcpOverview?.servers?.length || 0;
    $("nav-mcp-count").textContent = String(mcpCount);
    $("nav-mcp-count").hidden = mcpCount === 0;
    const stats = state.overview?.stats || {};
    const mcpStats = state.mcpOverview?.stats || {};
    setStatus("sidebar-summary", `${stats.detectedAgents || 0} 客户端 · ${stats.managed || 0} 技能 · ${mcpStats.managed || 0} MCP`);
  }

  function renderStats() {
    if (!state.overview) return;
    const stats = state.overview.stats || {};
    const mcpStats = state.mcpOverview?.stats || {};
    const cells = [
      { label: "已纳管技能", value: stats.managed || 0, note: `${state.overview.skills.filter((skill) => skill.managed).length} 个可从中心库同步` },
      { label: "已同步", value: stats.synced || 0, note: "所有目标内容一致" },
      { label: "需要处理", value: stats.needsAttention || 0, note: "更新、部分同步或冲突" },
      { label: "未纳管", value: stats.untracked || 0, note: "只在客户端目录中发现" },
      { label: "MCP 服务", value: mcpStats.managed || 0, note: `${mcpStats.synced || 0} 已同步 · ${mcpStats.untracked || 0} 未纳管` }
    ];
    const wrap = $("overview-stats");
    clear(wrap);
    for (const cell of cells) {
      wrap.append(h("div", { class: "stat" },
        h("span", { class: "stat-label" }, cell.label),
        h("strong", {}, String(cell.value)),
        h("span", { class: "stat-note" }, cell.note)
      ));
    }
  }

  function renderClients() {
    const wrap = $("overview-clients");
    clear(wrap);
    const agents = state.overview.agents || [];
    setStatus("overview-client-summary", `已检测 ${agents.filter((agent) => agent.detected).length} / ${agents.length} 个客户端`);
    for (const agent of agents) {
      const status = agent.detected ? "已检测" : "未检测";
      wrap.append(h("div", { class: "client-row" },
        h("div", { class: "client-name" }, h("i", { class: `led${agent.detected ? " on" : ""}` }), agent.name),
        h("div", { class: "client-path", title: agent.path }, agent.path),
        h("div", { class: `client-status${agent.detected ? " on" : ""}` }, agent.overridden ? `${status} · 自定义路径` : status),
        agent.note ? h("div", { class: "client-note" }, agent.note) : null
      ));
    }
  }

  function compactRow(skill) {
    return h("div", { class: "compact-row", onclick: () => openDetail(skill.name) },
      h("div", { class: "compact-main" },
        h("div", { class: "compact-name" }, skill.name),
        h("div", { class: "compact-desc" }, skill.description || sourceLabel(skill))
      ),
      statusBadge(skill.status)
    );
  }

  function renderAttention() {
    const wrap = $("overview-attention");
    clear(wrap);
    const attention = (state.overview.skills || [])
      .filter((skill) => ["update", "partial", "modified", "conflict", "missing", "hub_only"].includes(skill.status))
      .sort((left, right) => statusPriority(left.status) - statusPriority(right.status) || left.name.localeCompare(right.name, "zh-CN"));
    if (!attention.length) {
      wrap.append(h("p", { class: "subtle" }, "没有需要处理的技能。"));
      return;
    }
    for (const skill of attention.slice(0, 8)) wrap.append(compactRow(skill));
  }

  function renderRecent() {
    const wrap = $("overview-recent");
    clear(wrap);
    const skills = [...(state.overview.skills || [])].sort((left, right) => {
      const leftTime = Date.parse(left.updatedAt || 0) || 0;
      const rightTime = Date.parse(right.updatedAt || 0) || 0;
      return rightTime - leftTime || left.name.localeCompare(right.name, "zh-CN");
    });
    if (!skills.length) {
      wrap.append(h("p", { class: "subtle" }, "还没有发现技能。"));
      return;
    }
    for (const skill of skills.slice(0, 6)) wrap.append(compactRow(skill));
  }

  function renderHub() {
    const wrap = $("overview-hub");
    clear(wrap);
    const settings = state.overview.settings || {};
    const rows = [
      ["目录", state.overview.hubPath || "—"],
      ["纳管技能", `${state.overview.stats?.managed || 0} 个`],
      ["默认同步", { auto: "自动（链接优先）", link: "链接", copy: "副本" }[settings.defaultMode] || settings.defaultMode || "—"],
      ["GitHub Token", settings.hasToken ? "已配置（不回显）" : "未配置"],
      ["风险扫描", settings.scanLevel === "off" ? "已关闭" : "基础规则"],
      ["最近扫描", formatDate(state.overview.scannedAt)]
    ];
    for (const [label, value] of rows) {
      wrap.append(h("dt", {}, label), h("dd", {}, value));
    }
    const led = $("hub-led");
    led.className = `led${state.overview.stats?.managed ? " on" : ""}`;
    $("sidebar-hub").textContent = state.overview.hubPath ? "中心库已连接" : "中心库不可用";
  }

  function filteredSkills() {
    const query = state.query.trim().toLocaleLowerCase();
    return (state.overview?.skills || []).filter((skill) => {
      if (state.managedOnly && !skill.managed) return false;
      if (state.statusFilter === "attention") {
        if (!["update", "partial", "modified", "conflict", "missing", "hub_only"].includes(skill.status)) return false;
      } else if (state.statusFilter !== "all" && skill.status !== state.statusFilter) {
        return false;
      }
      if (!query) return true;
      const haystack = `${skill.name} ${skill.description || ""} ${sourceLabel(skill)} ${(skill.targets || []).map((target) => target.agentName || target.agentId).join(" ")}`.toLocaleLowerCase();
      return haystack.includes(query);
    }).sort((left, right) => {
      if (left.managed !== right.managed) return left.managed ? -1 : 1;
      const priority = statusPriority(left.status) - statusPriority(right.status);
      if (priority) return priority;
      return left.name.localeCompare(right.name, "zh-CN");
    });
  }

  function renderLibrary() {
    const list = $("library-list");
    clear(list);
    const skills = filteredSkills();
    const all = state.overview?.skills || [];
    const attention = all.filter((skill) => ["update", "partial", "modified", "conflict", "missing"].includes(skill.status)).length;
    $("library-summary").textContent = `共 ${all.length} 个技能 · ${all.filter((skill) => !skill.managed).length} 个未纳管 · ${attention} 个需要处理`;
    const empty = $("library-empty");
    empty.hidden = skills.length > 0;
    if (!skills.length) {
      $("library-empty-title").textContent = all.length ? "没有匹配的技能" : "还没有技能";
      $("library-empty-text").textContent = all.length ? "调整搜索或状态筛选条件。" : "从 GitHub 或压缩包导入第一个技能。";
    }
    for (const skill of skills) {
      const actions = h("div", { class: "row-actions" });
      if (skill.managed && skill.status === "update") {
        actions.append(h("button", { type: "button", class: "primary", onclick: (event) => { event.stopPropagation(); updateSkill(skill.name); } }, "更新"));
      }
      if (skill.managed && (skill.status === "partial" || skill.status === "hub_only")) {
        actions.append(h("button", { type: "button", onclick: (event) => { event.stopPropagation(); syncMissing(skill); } }, "补齐"));
      }
      if (!skill.managed) {
        actions.append(h("button", { type: "button", onclick: (event) => { event.stopPropagation(); adoptSkill(skill); } }, "纳入管理"));
      }
      actions.append(h("button", { type: "button", class: "subtle-button", onclick: (event) => { event.stopPropagation(); openDetail(skill.name); } }, "详情"));
      const source = h("div", { class: "skill-source", title: sourceTitle(skill) },
        h("span", {}, sourceLabel(skill)),
        skill.source?.path ? h("span", { class: "mono" }, skill.source.path) : null
      );
      list.append(h("div", {
        class: "skill-row",
        onclick: (event) => {
          if (event.target.closest("input, button, a, label")) return;
          openDetail(skill.name);
        }
      },
        h("div", { class: "skill-main" },
          h("div", { class: "skill-title-line" },
            h("span", { class: "skill-name" }, skill.name),
            !skill.managed ? h("span", { class: "chip untracked" }, "未纳管") : null,
            skill.available?.commit ? h("span", { class: "chip modified" }, "新版本") : null
          ),
          h("div", { class: "skill-desc" }, skill.description || "没有 description")
        ),
        source,
        targetChips(skill.targets),
        statusBadge(skill.status, skill.statusText),
        actions
      ));
    }
  }

  function mcpEndpoint(view) {
    if (!view) return "";
    if (view.transport === "stdio") return [view.command, ...(view.args || [])].filter(Boolean).join(" ");
    return view.url || "";
  }

  function mcpSourceLabel(view) {
    if (view?.source === "adopted") return "纳入管理";
    if (view?.source === "manual") return "手动配置";
    if (view?.source === "client") return "客户端配置";
    return view?.source || "未知";
  }

  function mcpStatusPriority(status) {
    return { conflict: 0, modified: 1, partial: 2, hub_only: 3, untracked: 4, synced: 5 }[status] ?? 6;
  }

  function renderOverviewMCP() {
    const wrap = $("overview-mcp");
    if (!wrap) return;
    clear(wrap);
    const servers = state.mcpOverview?.servers || [];
    if (!servers.length) {
      wrap.append(h("p", { class: "subtle" }, "还没有发现 MCP 服务。"));
      return;
    }
    const sorted = [...servers].sort((left, right) => mcpStatusPriority(left.status) - mcpStatusPriority(right.status) || left.name.localeCompare(right.name, "zh-CN"));
    for (const server of sorted.slice(0, 6)) {
      wrap.append(h("div", { class: "compact-row", onclick: () => openMCPDetail(server.name) },
        h("div", { class: "compact-main" },
          h("div", { class: "compact-name" }, server.name),
          h("div", { class: "compact-desc" }, `${server.transport} · ${mcpEndpoint(server) || "—"}`)
        ),
        statusBadge(server.status, server.statusText)
      ));
    }
  }

  function renderMCP() {
    if (!state.mcpOverview) return;
    renderMCPStats();
    renderMCPList();
    renderMCPAdapters();
  }

  function renderMCPStats() {
    const stats = state.mcpOverview?.stats || {};
    const cells = [
      { label: "已纳管 MCP", value: stats.managed || 0, note: "中心库中的服务配置" },
      { label: "已同步", value: stats.synced || 0, note: "所有目标配置一致" },
      { label: "需要处理", value: stats.needsAttention || 0, note: "部分同步、修改或冲突" },
      { label: "未纳管", value: stats.untracked || 0, note: "只在客户端配置中发现" }
    ];
    const wrap = $("mcp-stats");
    clear(wrap);
    for (const cell of cells) {
      wrap.append(h("div", { class: "stat" },
        h("span", { class: "stat-label" }, cell.label),
        h("strong", {}, String(cell.value)),
        h("span", { class: "stat-note" }, cell.note)
      ));
    }
  }

  function filteredMCPServers() {
    const query = state.mcpQuery.trim().toLocaleLowerCase();
    return (state.mcpOverview?.servers || []).filter((server) => {
      if (state.mcpStatusFilter === "attention") {
        if (!["partial", "modified", "conflict"].includes(server.status)) return false;
      } else if (state.mcpStatusFilter !== "all" && server.status !== state.mcpStatusFilter) {
        return false;
      }
      if (!query) return true;
      const haystack = `${server.name} ${server.command || ""} ${server.url || ""} ${(server.args || []).join(" ")} ${(server.envKeys || []).join(" ")} ${(server.headerKeys || []).join(" ")}`.toLocaleLowerCase();
      return haystack.includes(query);
    }).sort((left, right) => {
      if (left.managed !== right.managed) return left.managed ? -1 : 1;
      return mcpStatusPriority(left.status) - mcpStatusPriority(right.status) || left.name.localeCompare(right.name, "zh-CN");
    });
  }

  function renderMCPList() {
    const list = $("mcp-list");
    clear(list);
    const servers = filteredMCPServers();
    const all = state.mcpOverview?.servers || [];
    const attention = all.filter((server) => ["partial", "modified", "conflict"].includes(server.status)).length;
    $("mcp-summary").textContent = `共 ${all.length} 个 MCP 服务 · ${all.filter((server) => !server.managed).length} 个未纳管 · ${attention} 个需要处理`;
    const empty = $("mcp-empty");
    empty.hidden = servers.length > 0;
    if (!servers.length) {
      $("mcp-empty-title").textContent = all.length ? "没有匹配的 MCP 服务" : "还没有 MCP 服务";
      $("mcp-empty-text").textContent = all.length ? "调整搜索或状态筛选条件。" : "添加一个 stdio 或 HTTP MCP 服务，并选择要同步的客户端。";
    }
    for (const server of servers) {
      const actions = h("div", { class: "row-actions" });
      actions.append(h("button", { type: "button", onclick: (event) => { event.stopPropagation(); openMCPDetail(server.name); } }, server.managed ? "编辑" : "纳入管理"));
      const main = h("div", { class: "skill-main" },
        h("div", { class: "skill-title-line" },
          h("span", { class: "skill-name" }, server.name),
          h("span", { class: "chip" }, server.transport.toUpperCase()),
          !server.managed ? h("span", { class: "chip untracked" }, "未纳管") : null
        ),
        h("div", { class: "mcp-endpoint", title: mcpEndpoint(server) }, mcpEndpoint(server) || "—")
      );
      list.append(h("div", {
        class: "skill-row",
        onclick: (event) => {
          if (event.target.closest("input, button, a, label")) return;
          openMCPDetail(server.name);
        }
      },
        main,
        h("div", { class: "skill-source" }, h("span", {}, mcpSourceLabel(server)), server.updatedAt ? h("span", {}, formatRelative(server.updatedAt)) : null),
        targetChips(server.targets),
        statusBadge(server.status, server.statusText),
        actions
      ));
    }
  }

  function renderMCPAdapters() {
    const wrap = $("mcp-adapters");
    clear(wrap);
    for (const adapter of state.mcpOverview?.adapters || []) {
      wrap.append(h("div", { class: "client-row" },
        h("div", { class: "client-name" }, h("i", { class: `led${adapter.detected ? " on" : ""}` }), adapter.name),
        h("div", { class: "client-path", title: adapter.path }, adapter.path),
        h("div", { class: `client-status${adapter.detected ? " on" : ""}` }, adapter.detected ? `${adapter.format.toUpperCase()} · 已检测` : "未检测")
      ));
    }
  }

  function openMCPAdd() {
    closeDetail();
    ++state.drawerRequest;
    state.mcpDetail = null;
    state.mcpSourceAgentId = "";
    state.mcpMode = "add";
    state.mcpSelectedTargets = new Set();
    fillMCPForm({ transport: "stdio", env: {}, headers: {} }, null);
    $("mcp-backdrop").hidden = false;
    openOverlay("mcp-drawer");
  }

  async function openMCPDetail(name, agentId = "") {
    closeDetail();
    const request = ++state.drawerRequest;
    const view = state.mcpOverview?.servers?.find((server) => server.name === name);
    const sources = (view?.targets || []).filter((target) => target.exists);
    state.mcpSourceAgentId = agentId || (!view?.managed && sources.length === 1 ? sources[0].agentId : "");
    state.mcpDetail = { view: view || { name }, config: null };
    state.mcpMode = view && !view.managed ? "untracked" : "loading";
    state.mcpSelectedTargets = new Set();
    fillMCPForm(null, state.mcpDetail.view);
    $("mcp-backdrop").hidden = false;
    openOverlay("mcp-drawer");
    if (view && !view.managed && sources.length > 1 && !state.mcpSourceAgentId) {
      setStatus("mcp-form-status", "请先选择来源客户端，再查看配置或纳入管理。选择来源不会修改客户端。");
      return;
    }
    setStatus("mcp-form-status", "正在读取配置…");
    try {
      const detail = await invoke("mcp/getServer", { name, ...(state.mcpSourceAgentId ? { agentId: state.mcpSourceAgentId } : {}) });
      if (request !== state.drawerRequest) return;
      state.mcpDetail = detail;
      state.mcpMode = detail.view.managed ? "edit" : "untracked";
      state.mcpSelectedTargets = new Set((detail.view.targets || []).filter((target) => target.managed).map((target) => target.agentId));
      fillMCPForm(detail.config, detail.view);
    } catch (error) {
      if (request !== state.drawerRequest) return;
      setStatus("mcp-form-status", "读取失败；可重新选择来源或关闭后重试。");
      showToast(`读取 MCP 配置失败：${errorText(error)}`, true);
    }
  }

  function closeMCP() {
    if (!$("mcp-drawer").hidden) ++state.drawerRequest;
    closeMCPImport();
    closeOverlay("mcp-drawer");
    $("mcp-backdrop").hidden = true;
    state.mcpDetail = null;
    state.mcpSourceAgentId = "";
  }

  function fillMCPForm(config, view) {
    const isAdd = state.mcpMode === "add";
    const isUntracked = state.mcpMode === "untracked";
    const name = view?.name || "";
    const sourceSelect = $("mcp-source");
    clear(sourceSelect);
    sourceSelect.append(h("option", { value: "" }, "请选择来源客户端"));
    for (const target of (view?.targets || []).filter((target) => target.exists)) {
      sourceSelect.append(h("option", { value: target.agentId }, target.agentName || agentName(target.agentId)));
    }
    sourceSelect.value = state.mcpSourceAgentId;
    $("mcp-source-row").hidden = !isUntracked;
    $("mcp-name").value = name;
    $("mcp-name").disabled = !isAdd;
    $("mcp-transport").value = config?.transport || "stdio";
    $("mcp-command").value = config?.command || "";
    $("mcp-args").value = JSON.stringify(config?.args || [], null, 2);
    $("mcp-url").value = config?.url || "";
    $("mcp-env").value = mapToLines(config?.env);
    $("mcp-headers").value = mapToLines(config?.headers);
    $("mcp-timeout").value = String(config?.timeoutMs || 0);
    $("mcp-overwrite").checked = false;
    $("mcp-drawer-title").textContent = isAdd ? "添加 MCP 服务" : name;
    $("mcp-drawer-breadcrumb").textContent = isAdd ? "MCP 服务 / 新建" : (isUntracked ? "MCP 服务 / 未纳管" : "MCP 服务 / 编辑");
    $("mcp-drawer-desc").textContent = isAdd ? "配置会先进入中心库，再写入所选客户端。" : (view?.statusText || "");
    const meta = $("mcp-drawer-meta");
    clear(meta);
    if (!isAdd) {
      const rows = [
        ["状态", view?.statusText || "—"],
        ["来源", mcpSourceLabel(view)],
        ["传输", (config?.transport || "").toUpperCase()],
        ["环境变量", (view?.envKeys || []).join("、") || "无"],
        ["请求头", (view?.headerKeys || []).join("、") || "无"],
        ["更新时间", formatDate(view?.updatedAt)]
      ];
      for (const [label, value] of rows) meta.append(h("div", { class: "meta-line" }, h("b", {}, label), h("span", {}, value || "—")));
      if (view?.warnings?.length) meta.append(h("div", { class: "meta-line" }, h("b", {}, "提示"), h("span", {}, view.warnings.join("；"))));
    }
    const editable = isAdd || state.mcpMode === "edit";
    for (const id of ["mcp-transport", "mcp-command", "mcp-args", "mcp-url", "mcp-env", "mcp-headers", "mcp-timeout"]) {
      $(id).disabled = !editable;
    }
    $("mcp-import-open").hidden = !editable;
    $("mcp-save").hidden = !editable;
    $("mcp-adopt").hidden = !isUntracked;
    $("mcp-adopt").disabled = !config || !state.mcpSourceAgentId;
    $("mcp-remove").hidden = !view?.managed || !editable;
    $("mcp-overwrite").disabled = !editable;
    $("mcp-overwrite").closest("label").hidden = !editable;
    $("mcp-form-status").textContent = isUntracked
      ? "该服务来自所选客户端配置；纳入管理后会进入中心库，之后可以统一编辑和同步。"
      : (isAdd ? "保存后写入所选客户端，并在写入前自动备份原配置。" : "环境变量和请求头中的敏感值显示为掩码；保持掩码会沿用原值。");
    syncMCPTransportFields();
    renderMCPTargets();
  }

  function syncMCPTransportFields() {
    const transport = $("mcp-transport").value;
    $("mcp-stdio-fields").hidden = transport !== "stdio";
    $("mcp-http-fields").hidden = transport === "stdio";
    $("mcp-env-field").hidden = transport !== "stdio";
  }

  function renderMCPTargets() {
    const wrap = $("mcp-targets");
    clear(wrap);
    const editable = state.mcpMode === "add" || state.mcpMode === "edit";
    for (const adapter of state.mcpOverview?.adapters || []) {
      const checked = state.mcpSelectedTargets.has(adapter.id);
      const disabled = (!adapter.detected && !checked) || !editable;
      wrap.append(h("label", { class: `target-option${checked ? " checked" : ""}`, title: adapter.path },
        h("input", {
          type: "checkbox", checked, disabled,
          onchange: (event) => {
            if (event.target.checked) state.mcpSelectedTargets.add(adapter.id);
            else state.mcpSelectedTargets.delete(adapter.id);
            event.target.closest("label").classList.toggle("checked", event.target.checked);
            renderMCPTargetHint();
          }
        }),
        h("span", {}, h("strong", {}, adapter.name), h("small", { title: adapter.path }, adapter.detected ? adapter.path : "未检测到"))
      ));
    }
    renderMCPTargetHint();
  }

  function renderMCPTargetHint() {
    const editable = state.mcpMode === "add" || state.mcpMode === "edit";
    const hint = $("mcp-target-hint");
    if (!editable) hint.textContent = "未纳管服务不能在这里切换目标；纳入管理后可编辑。";
    else if (!state.mcpSelectedTargets.size) hint.textContent = "不选择客户端时只保存到中心库。";
    else hint.textContent = "保存时会写入所选客户端；Pi 和 Trae 不支持 SSE 传输。";
  }

  function mapToLines(values) {
    return Object.entries(values || {}).map(([key, value]) => `${key}=${value}`).join("\n");
  }

  function parseLines(text, kind = "env") {
    const values = Object.create(null);
    const errors = [];
    const seen = new Set();
    const keyPattern = kind === "headers" ? /^[!#$%&'*+.^_`|~0-9A-Za-z-]+$/ : /^[A-Za-z_][A-Za-z0-9_]*$/;
    String(text || "").split(/\r?\n/).forEach((line, index) => {
      if (!line.trim() || line.trimStart().startsWith("#")) return;
      const separator = line.indexOf("=");
      const key = separator < 0 ? "" : line.slice(0, separator).trim();
      const identity = kind === "headers" ? key.toLowerCase() : key;
      if (separator <= 0 || !keyPattern.test(key)) {
        errors.push(`第 ${index + 1} 行：请使用有效的 KEY=VALUE 格式`);
      } else if (seen.has(identity)) {
        errors.push(`第 ${index + 1} 行：键名重复`);
      } else {
        seen.add(identity);
        values[key] = line.slice(separator + 1);
      }
    });
    return { values, errors };
  }

  function parseArgs(text) {
    const value = String(text ?? "");
    if (!value) return [];
    if (value.trimStart().startsWith("[")) {
      let args;
      try { args = JSON.parse(value); } catch { throw new Error("Args：JSON 格式无效，请输入字符串数组"); }
      if (!Array.isArray(args) || args.some((arg) => typeof arg !== "string")) throw new Error("Args：数组中的每一项都必须是字符串");
      return args;
    }
    return value.split(/\r?\n/);
  }

  function collectMCPForm() {
    const transport = $("mcp-transport").value;
    const isStdio = transport === "stdio";
    const fields = parseLines($(isStdio ? "mcp-env" : "mcp-headers").value, isStdio ? "env" : "headers");
    if (fields.errors.length) throw new Error(`${isStdio ? "环境变量" : "Headers"}：${fields.errors.join("；")}`);
    const args = isStdio ? parseArgs($("mcp-args").value) : [];
    const timeoutMs = Number($("mcp-timeout").value || 0);
    if (!Number.isInteger(timeoutMs) || timeoutMs < 0 || timeoutMs > 600000) throw new Error("超时必须是 0–600000 之间的整数");
    return {
      originalName: state.mcpDetail?.view?.managed ? state.mcpDetail.view.name : "",
      name: $("mcp-name").value.trim(),
      transport,
      command: isStdio ? $("mcp-command").value.trim() : "",
      args,
      env: isStdio ? fields.values : {},
      url: isStdio ? "" : $("mcp-url").value.trim(),
      headers: isStdio ? {} : fields.values,
      timeoutMs,
      targets: [...state.mcpSelectedTargets],
      overwrite: $("mcp-overwrite").checked
    };
  }

  function targetChangeSummary(before, after, nameForId = agentName) {
    const previous = new Set(before);
    const next = new Set(after);
    const names = (ids) => ids.map(nameForId).join("、") || "无";
    return [
      `新增目标：${names([...next].filter((id) => !previous.has(id)))}`,
      `更新目标：${names([...next].filter((id) => previous.has(id)))}`,
      `移除目标：${names([...previous].filter((id) => !next.has(id)))}`
    ].join("\n");
  }

  function mcpAgentName(id) {
    return state.mcpOverview?.adapters?.find((adapter) => adapter.id === id)?.name
      || state.mcpDetail?.view?.targets?.find((target) => target.agentId === id)?.agentName || agentName(id);
  }

  async function saveMCPForm() {
    if (!["add", "edit"].includes(state.mcpMode) || $("mcp-save").disabled) return;
    let input;
    try {
      input = collectMCPForm();
      if (!input.name) throw new Error("请填写 MCP 服务名");
      if (input.transport === "stdio" && !input.command) throw new Error("请填写 Command");
      if (input.transport !== "stdio" && !input.url) throw new Error("请填写 URL");
    } catch (error) {
      setStatus("mcp-form-status", errorText(error));
      showToast(errorText(error), true);
      return;
    }
    const request = state.drawerRequest;
    const existing = state.mcpDetail?.view?.managed ? state.mcpDetail.view : state.mcpOverview?.servers?.find((server) => server.name === input.name && server.managed);
    const previous = (existing?.targets || []).filter((target) => target.managed).map((target) => target.agentId);
    const answer = await confirmAction({
      title: "确认保存 MCP",
      message: `服务：${input.name}\n${targetChangeSummary(previous, input.targets, mcpAgentName)}\n\n${input.targets.length ? "写入所选客户端，并备份原配置。" : "仅保留中心库配置，不写入新客户端。"}${previous.some((id) => !input.targets.includes(id)) ? "取消勾选的纳管目标将被移除并备份。" : ""}\n${input.overwrite ? "已允许覆盖同名或本地修改的配置。" : "不允许覆盖同名或本地修改的配置；发生冲突时中止。"}`,
      okText: "确认保存",
      okClass: "primary"
    });
    if (!answer.ok || request !== state.drawerRequest) return;
    const button = $("mcp-save");
    setBusy(button, true, "保存中…");
    try {
      const result = await invoke("mcp/save", { ...input, confirmed: true });
      showToast(result.message || "MCP 服务已保存");
      if (request === state.drawerRequest) closeMCP();
      await loadMCP({ silent: true });
    } catch (error) {
      showToast(`保存 MCP 失败：${errorText(error)}`, true);
    } finally {
      setBusy(button, false);
    }
  }

  async function removeMCPForm() {
    const name = state.mcpDetail?.view?.name;
    if (!name) return;
    const request = state.drawerRequest;
    const answer = await confirmAction({
      title: "移除 MCP 服务",
      message: `将从中心库和所有纳管客户端移除 ${name}，并备份原配置文件。未纳管的同名配置不会被删除。`,
      okText: "移除"
    });
    if (!answer.ok || request !== state.drawerRequest) return;
    try {
      await invoke("mcp/remove", { name, confirmed: true });
      showToast("MCP 服务已移除");
      if (request === state.drawerRequest) closeMCP();
      await loadMCP({ silent: true });
    } catch (error) {
      showToast(`移除失败：${errorText(error)}`, true);
    }
  }

  async function adoptMCPForm() {
    const name = state.mcpDetail?.view?.name;
    const target = state.mcpDetail?.view?.targets?.find((item) => item.exists && item.agentId === state.mcpSourceAgentId);
    if (!name || !target || !state.mcpDetail?.config || $("mcp-adopt").disabled) return;
    const request = state.drawerRequest;
    const answer = await confirmAction({
      title: "将 MCP 纳入管理",
      message: `来源客户端：${target.agentName || agentName(target.agentId)}\n服务：${name}\n将所选来源配置纳入中心库。客户端配置不会被修改，其他同名配置不会自动纳入管理。`,
      okText: "纳入管理",
      okClass: "primary"
    });
    if (!answer.ok || request !== state.drawerRequest) return;
    const button = $("mcp-adopt");
    setBusy(button, true);
    try {
      const result = await invoke("mcp/adopt", { agentId: target.agentId, name });
      showToast(result.message || "MCP 服务已纳入管理");
      await loadMCP({ silent: true });
      if (request === state.drawerRequest) await openMCPDetail(name);
    } catch (error) {
      showToast(`纳入管理失败：${errorText(error)}`, true);
    } finally {
      setBusy(button, false);
    }
  }

  function openMCPImport() {
    if ($("mcp-drawer").hidden) openMCPAdd();
    ++state.importRequest;
    state.mcpImportServers = [];
    $("mcp-import-status").textContent = "";
    $("mcp-import-select-row").hidden = true;
    $("mcp-import-apply").hidden = true;
    openOverlay("mcp-import-modal");
  }

  function closeMCPImport() {
    ++state.importRequest;
    state.mcpImportServers = [];
    closeOverlay("mcp-import-modal");
  }

  async function parseMCPImport() {
    const text = $("mcp-import-text").value;
    const request = ++state.importRequest;
    const drawerRequest = state.drawerRequest;
    const button = $("mcp-import-parse");
    setBusy(button, true, "解析中…");
    try {
      const result = await invoke("mcp/importJSON", { text });
      if (request !== state.importRequest || drawerRequest !== state.drawerRequest) return;
      state.mcpImportServers = result.servers || [];
      state.mcpImportIndex = 0;
      const select = $("mcp-import-select");
      clear(select);
      state.mcpImportServers.forEach((server, index) => select.append(h("option", { value: String(index) }, server.name)));
      $("mcp-import-select-row").hidden = state.mcpImportServers.length <= 1;
      $("mcp-import-apply").hidden = false;
      $("mcp-import-status").textContent = `发现 ${state.mcpImportServers.length} 个服务${result.warnings?.length ? "；" + result.warnings.join("；") : ""}`;
      if (state.mcpImportServers.length === 1) applyMCPImport();
    } catch (error) {
      if (request !== state.importRequest || drawerRequest !== state.drawerRequest) return;
      state.mcpImportServers = [];
      $("mcp-import-apply").hidden = true;
      $("mcp-import-status").textContent = errorText(error);
    } finally {
      setBusy(button, false);
    }
  }

  function applyMCPImport() {
    const index = Number($("mcp-import-select").value || 0);
    const server = state.mcpImportServers[index];
    if (!server) return;
    ++state.drawerRequest;
    state.mcpSourceAgentId = "";
    if (state.mcpMode !== "add") {
      state.mcpDetail = null;
      state.mcpMode = "add";
      state.mcpSelectedTargets = new Set();
    }
    fillMCPForm(server.config, { name: server.name, managed: false, statusText: "待保存", targets: [] });
    $("mcp-name").value = server.name;
    $("mcp-form-status").textContent = "已从 JSON 填入；请确认目标客户端后保存。";
    $("mcp-drawer").hidden = false;
    $("mcp-backdrop").hidden = false;
    closeMCPImport();
  }

  function renderMarkdown(text) {
    const source = String(text || "");
    if (!source) return h("p", { class: "subtle" }, "没有可显示的说明。");
    const lines = source.replace(/\r\n?/g, "\n").split("\n");
    const root = h("div", { class: "markdown" });
    let inCode = false;
    let codeLines = [];
    let listType = "";
    let listNode = null;
    const closeList = () => {
      if (listNode) root.append(listNode);
      listNode = null;
      listType = "";
    };
    const inline = (value) => {
      let output = escapeHtml(value);
      output = output.replace(/`([^`]+)`/g, "<code>$1</code>");
      output = output.replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>");
      output = output.replace(/\[([^\]]+)\]\((https?:\/\/[^\s)]+)\)/g, '<a href="$2" target="_blank" rel="noopener noreferrer">$1</a>');
      return output;
    };
    for (const rawLine of lines) {
      const line = rawLine.replace(/\s+$/, "");
      if (/^```/.test(line)) {
        if (inCode) {
          root.append(h("pre", {}, h("code", { text: codeLines.join("\n") })));
          codeLines = [];
          inCode = false;
        } else {
          closeList();
          inCode = true;
        }
        continue;
      }
      if (inCode) {
        codeLines.push(rawLine);
        continue;
      }
      if (!line.trim()) {
        closeList();
        continue;
      }
      const heading = line.match(/^(#{1,6})\s+(.*)$/);
      if (heading) {
        closeList();
        const level = Math.min(6, heading[1].length);
        const node = document.createElement(`h${level}`);
        node.innerHTML = inline(heading[2]);
        root.append(node);
        continue;
      }
      const unordered = line.match(/^\s*[-*+]\s+(.*)$/);
      const ordered = line.match(/^\s*\d+\.\s+(.*)$/);
      if (unordered || ordered) {
        const type = unordered ? "ul" : "ol";
        if (!listNode || listType !== type) {
          closeList();
          listType = type;
          listNode = document.createElement(type);
        }
        const item = document.createElement("li");
        item.innerHTML = inline((unordered || ordered)[1]);
        listNode.append(item);
        continue;
      }
      if (/^>\s?/.test(line)) {
        closeList();
        const quote = document.createElement("blockquote");
        quote.innerHTML = inline(line.replace(/^>\s?/, ""));
        root.append(quote);
        continue;
      }
      closeList();
      const paragraph = document.createElement("p");
      paragraph.innerHTML = inline(line);
      root.append(paragraph);
    }
    if (inCode && codeLines.length) root.append(h("pre", {}, h("code", { text: codeLines.join("\n") })));
    closeList();
    return root;
  }

  function escapeHtml(value) {
    return String(value).replace(/[&<>"']/g, (character) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[character]));
  }

  async function openDetail(name) {
    if (!name) return;
    closeMCP();
    const request = ++state.drawerRequest;
    ++state.fileRequest;
    state.detail = null;
    state.detailName = name;
    state.fileView = null;
    state.detailTab = "readme";
    state.detailRaw = false;
    setStatus("detail-name", name);
    setStatus("detail-desc", "正在读取详情…");
    for (const id of ["detail-meta", "detail-body", "detail-actions"]) clear($(id));
    $("drawer-backdrop").hidden = false;
    openOverlay("detail-drawer");
    try {
      const detail = await invoke("skills/getSkill", { name });
      if (request !== state.drawerRequest) return;
      state.detail = detail;
      renderDetail();
    } catch (error) {
      if (request !== state.drawerRequest) return;
      setStatus("detail-desc", "读取失败，请关闭后重试。");
      showToast(`读取详情失败：${errorText(error)}`, true);
    }
  }

  function closeDetail() {
    if (!$("detail-drawer").hidden) ++state.drawerRequest;
    ++state.fileRequest;
    state.detail = null;
    state.detailName = "";
    state.fileView = null;
    closeOverlay("detail-drawer");
    $("drawer-backdrop").hidden = true;
  }

  async function refreshDetail() {
    if (!state.detailName) return;
    const name = state.detailName;
    const request = state.drawerRequest;
    const fileRequest = ++state.fileRequest;
    try {
      const detail = await invoke("skills/getSkill", { name });
      if (request !== state.drawerRequest || fileRequest !== state.fileRequest) return;
      state.detail = detail;
      state.fileView = null;
      renderDetail();
    } catch {
      if (request === state.drawerRequest && fileRequest === state.fileRequest) closeDetail();
    }
  }

  function severityLabel(severity) {
    return { critical: "高危", high: "高风险", medium: "中风险", low: "低风险" }[severity] || severity || "提示";
  }

  function riskList(findings, limit = 10) {
    const risks = Array.isArray(findings) ? findings : [];
    if (!risks.length) return null;
    const box = h("div", { class: "risk-box" }, h("h3", {}, "风险项"));
    const list = h("ul", { class: "risk-list" });
    for (const risk of risks.slice(0, limit)) {
      list.append(h("li", {},
        h("strong", {}, `[${severityLabel(risk.severity)}] ${risk.title}`),
        risk.file ? h("span", { class: "subtle" }, ` · ${risk.file}:${risk.line || 1}`) : null,
        risk.snippet ? h("span", { class: "snippet" }, risk.snippet) : null
      ));
    }
    box.append(list);
    if (risks.length > limit) box.append(h("p", { class: "hint" }, `仅显示前 ${limit} 项风险，共 ${risks.length} 项。`));
    return box;
  }

  function renderDetail() {
    const detail = state.detail;
    if (!detail) return;
    const skill = detail.skill;
    $("detail-name").textContent = skill.name;
    $("detail-desc").textContent = skill.description || "没有 description";
    const meta = $("detail-meta");
    clear(meta);
    const source = skill.source ? sourceLabel(skill) : "—";
    const rows = [
      ["状态", skill.statusText || statusMeta[skill.status]?.label || skill.status],
      ["来源", source],
      ["来源详情", sourceTitle(skill) || "—"],
      ["内容哈希", shortHash(skill.contentHash)],
      ["更新时间", formatDate(skill.updatedAt)],
      ["文件", `${skill.files || 0} 个 · ${formatBytes(skill.size)}`]
    ];
    if (skill.available?.commit) rows.push(["新版本", `${shortHash(skill.available.commit)} · ${formatDate(skill.available.checkedAt)}`]);
    if (skill.available?.error) rows.push(["检查结果", skill.available.error]);
    if (skill.warnings?.length) rows.push(["提示", skill.warnings.join("；")]);
    for (const [label, value] of rows) {
      meta.append(h("div", { class: "meta-line" }, h("b", {}, label), h("span", {}, value || "—")));
    }
    for (const button of document.querySelectorAll("#detail-tabs button")) {
      button.setAttribute("aria-selected", String(button.dataset.tab === state.detailTab));
    }
    const body = $("detail-body");
    clear(body);
    if (state.fileView) {
      body.append(h("button", { type: "button", class: "subtle-button", onclick: () => { state.fileView = null; renderDetail(); } }, "← 返回文件列表"));
      body.append(h("p", { class: "subtle" }, state.fileView.path));
      if (state.fileView.binary) body.append(h("p", { class: "subtle" }, "二进制文件，不提供文本预览。"));
      else body.append(h("pre", { class: "code-view" }, state.fileView.content || ""));
    } else if (state.detailTab === "readme") {
      const toolbar = h("div", { class: "form-actions", style: { marginTop: "0", marginBottom: "10px" } },
        h("button", { type: "button", class: "subtle-button", onclick: () => { state.detailRaw = !state.detailRaw; renderDetail(); } }, state.detailRaw ? "渲染预览" : "查看原文")
      );
      body.append(toolbar);
      if (state.detailRaw) body.append(h("pre", { class: "code-view" }, detail.content || ""));
      else body.append(renderMarkdown(detail.content || ""));
      const risks = riskList(skill.risk, 10);
      if (risks) body.append(risks);
    } else if (state.detailTab === "files") {
      const list = h("div", { class: "file-list" });
      const files = detail.files || [];
      if (!files.length) list.append(h("p", { class: "subtle" }, "没有文件列表。"));
      for (const file of files) {
        list.append(h("div", {
          class: "file-row",
          onclick: async () => {
            const request = state.drawerRequest;
            const fileRequest = ++state.fileRequest;
            try {
              const result = await invoke("skills/readFile", { name: skill.name, path: file.path });
              if (request !== state.drawerRequest || fileRequest !== state.fileRequest) return;
              state.fileView = result;
              renderDetail();
            } catch (error) {
              if (request === state.drawerRequest && fileRequest === state.fileRequest) showToast(`读取文件失败：${errorText(error)}`, true);
            }
          }
        },
          h("span", { class: "file-path", title: file.path }, file.path),
          h("span", { class: "file-size" }, formatBytes(file.size)),
          file.executable ? h("span", { class: "chip modified" }, "可执行") : null
        ));
      }
      body.append(list);
    } else {
      renderDetailTargets(body, skill);
    }
    const actions = $("detail-actions");
    clear(actions);
    if (skill.managed) {
      actions.append(h("button", { type: "button", onclick: () => checkOneUpdate(skill.name) }, "检查更新"));
      if (skill.status === "update" || skill.available?.commit) actions.append(h("button", { type: "button", class: "primary", onclick: () => updateSkill(skill.name) }, "更新"));
      if (skill.status === "partial" || skill.status === "hub_only") actions.append(h("button", { type: "button", onclick: () => syncMissing(skill) }, "补齐客户端"));
      actions.append(h("button", { type: "button", onclick: () => copyText(sourceTitle(skill) || sourceLabel(skill)) }, "复制来源"));
      actions.append(h("button", { type: "button", class: "danger", onclick: () => removeSkill(skill) }, "移除"));
    } else {
      actions.append(h("button", { type: "button", class: "primary", onclick: () => adoptSkill(skill) }, "纳入管理"));
      actions.append(h("button", { type: "button", onclick: () => copyText(sourceTitle(skill) || sourceLabel(skill)) }, "复制来源"));
    }
  }

  function renderDetailTargets(body, skill) {
    const targets = skill.targets || [];
    if (targets.length) {
      body.append(h("h3", { style: { marginBottom: "6px" } }, "当前目标"));
      const list = h("div", { class: "target-list" });
      for (const target of targets) {
        list.append(h("div", { class: "target-row" },
          h("div", { class: "target-info" },
            h("div", { class: "target-title" },
              target.agentName || agentName(target.agentId),
              target.managed ? h("span", { class: "chip managed" }, target.mode || "managed") : target.tracked ? null : h("span", { class: "chip untracked" }, "未纳管"),
              target.modified ? h("span", { class: "chip modified" }, "已修改") : null,
              target.tracked && !target.enabled ? h("span", { class: "chip untracked" }, "已停用") : null,
              target.enabled && !target.exists ? h("span", { class: "chip untracked" }, "缺失") : null
            ),
            h("div", { class: "target-path" }, target.path || "")
          )
        ));
      }
      body.append(list);
    }
    if (skill.managed) {
      body.append(renderDispatchPanel(skill));
      body.append(h("p", { class: "hint", style: { marginTop: "10px" } }, "链接模式指向中心库，更新后自动生效；副本模式需要在更新后重新复制。移除会把客户端目录移入插件回收站，中心库保留。"));
    } else {
      body.append(h("p", { class: "hint", style: { marginTop: "10px" } }, "未纳管技能来自客户端现有目录。纳入管理时会复制所选来源到中心库，再将该来源目录备份并替换为链接。其他客户端不会被修改。"));
    }
  }

  function renderDispatchPanel(skill) {
    const agents = (state.overview?.agents || []).filter((agent) => agent.detected || agent.custom);
    const targets = skill.targets || [];
    const current = new Set(targets.filter((target) => target.tracked && target.enabled).map((target) => target.agentId));
    const selected = new Set(current);
    const byAgent = new Map(targets.map((target) => [target.agentId, target]));
    const panel = h("section", { class: "dispatch" },
      h("div", { class: "card-heading", style: { marginBottom: "6px" } },
        h("div", {},
          h("h3", {}, "下发范围"),
          h("p", { class: "subtle" }, "勾选要同步到的客户端；取消勾选会把该客户端的目录移入回收站。")
        )
      )
    );
    if (!agents.length) {
      panel.append(h("p", { class: "subtle" }, "没有检测到可下发的客户端。"));
      return panel;
    }
    const rows = h("div", { class: "dispatch-rows" });
    for (const agent of agents) {
      const target = byAgent.get(agent.id);
      let status = "未下发";
      let statusClass = "untracked";
      if (target?.tracked && target.enabled) {
        status = target.exists ? "已下发" : "缺失";
        statusClass = target.exists ? "managed" : "modified";
      } else if (target?.tracked) {
        status = "已停用";
      } else if (target?.exists) {
        status = "同名未纳管";
      }
      let row;
      const input = h("input", {
        type: "checkbox",
        checked: selected.has(agent.id),
        "aria-label": `${agent.name}：下发 ${skill.name}`,
        onchange: (event) => {
          if (event.target.checked) selected.add(agent.id);
          else selected.delete(agent.id);
          if (row) row.className = `dispatch-row${event.target.checked ? " checked" : ""}`;
        }
      });
      row = h("label", { class: `dispatch-row${selected.has(agent.id) ? " checked" : ""}`, title: agent.path },
        input,
        h("span", { class: "dispatch-name" }, h("strong", {}, agent.name), h("small", {}, agent.path)),
        h("span", { class: `chip ${statusClass}` }, status)
      );
      rows.append(row);
    }
    const overwrite = h("input", { type: "checkbox" });
    const apply = h("button", { type: "button", class: "primary" }, "应用下发范围");
    apply.addEventListener("click", () => applyDispatch(skill, selected, overwrite.checked, apply));
    panel.append(rows,
      h("label", { class: "check-line" }, overwrite, h("span", {}, "允许覆盖新增客户端中的同名或本地修改内容（先备份）")),
      h("div", { class: "form-actions" }, apply)
    );
    return panel;
  }

  async function applyDispatch(skill, selected, overwrite, button) {
    const targets = skill.targets || [];
    const current = new Set(targets.filter((target) => target.tracked && target.enabled).map((target) => target.agentId));
    const tracked = new Set(targets.filter((target) => target.tracked).map((target) => target.agentId));
    const agents = state.overview?.agents || [];
    const nameOf = (id) => agents.find((agent) => agent.id === id)?.name || agentName(id);
    const added = [...selected].filter((id) => !current.has(id));
    const removed = [...current].filter((id) => !selected.has(id));
    if (!added.length && !removed.length) {
      showToast("下发范围没有变化");
      return;
    }
    const lines = [`技能：${skill.name}`];
    if (added.length) lines.push(`新增：${added.map(nameOf).join("、")}`);
    if (removed.length) lines.push(`移除：${removed.map(nameOf).join("、")}`);
    if (removed.length) lines.push("移除会把客户端目录移入插件回收站，中心库保留。");
    if (added.length) lines.push("若新增目标已有同名或本地修改内容，只有勾选覆盖确认才允许替换。");
    const answer = await confirmAction({
      title: "应用下发范围",
      message: lines.join("\n"),
      checkLabel: added.length ? "允许覆盖新增客户端中的同名或本地修改内容（先备份）" : "",
      okText: "应用"
    });
    if (!answer.ok) return;
    setBusy(button, true, "同步中…");
    const failures = [];
    for (const id of added) {
      try {
        await invoke("skills/setTarget", { name: skill.name, agentId: id, enabled: true, mode: tracked.has(id) ? "" : "auto", confirmed: Boolean(answer.checked) });
      } catch (error) {
        failures.push(`${nameOf(id)}：${errorText(error)}`);
      }
    }
    for (const id of removed) {
      try {
        await invoke("skills/setTarget", { name: skill.name, agentId: id, enabled: false, confirmed: true });
      } catch (error) {
        failures.push(`${nameOf(id)}：${errorText(error)}`);
      }
    }
    setBusy(button, false);
    if (failures.length) showToast(`下发未完成：${failures.join("；")}`, true);
    else showToast(`下发范围已更新：新增 ${added.length} 个、移除 ${removed.length} 个`);
    await loadOverview({ silent: true });
    await refreshDetail();
  }

  async function syncMissing(skill) {
    if (!skill.managed) return;
    const existing = new Set((skill.targets || []).filter((target) => target.exists).map((target) => target.agentId));
    const missing = (state.overview.agents || []).filter((agent) => agent.detected && !existing.has(agent.id));
    if (!missing.length) {
      showToast("没有需要补齐的客户端");
      return;
    }
    const answer = await confirmAction({
      title: "补齐客户端",
      message: `技能：${skill.name}\n${targetChangeSummary([], missing.map((agent) => agent.id))}\n只写入列出的客户端。若目录出现同名或本地修改内容，需勾选覆盖确认才允许替换。`,
      checkLabel: "允许覆盖所列客户端中的同名或本地修改内容（先备份）",
      okText: "开始同步"
    });
    if (!answer.ok) return;
    const failures = [];
    for (const agent of missing) {
      try {
        await invoke("skills/setTarget", { name: skill.name, agentId: agent.id, enabled: true, mode: "auto", confirmed: Boolean(answer.checked) });
      } catch (error) {
        failures.push(`${agent.name}：${errorText(error)}`);
      }
    }
    if (failures.length) showToast(`部分客户端同步失败：${failures.join("；")}`, true);
    else showToast(`已补齐 ${missing.length} 个客户端`);
    await loadOverview({ silent: true });
    await refreshDetail();
  }

  async function adoptSkill(skill) {
    const targets = (skill.targets || []).filter((target) => target.exists);
    if (!targets.length) {
      showToast("没有可纳入管理的客户端目录", true);
      return;
    }
    const messageForTarget = (target) => `来源客户端：${target.agentName || agentName(target.agentId)}\n技能：${skill.name}\n将所选来源复制到中心库，并将该客户端原目录备份到插件回收站后替换为链接。其他同名目录不会被修改。`;
    const answer = await confirmAction({
      title: "将技能纳入管理",
      message: targets.length === 1 ? messageForTarget(targets[0]) : `技能：${skill.name}\n多个客户端存在同名技能。请选择来源；只处理所选客户端，其他同名目录不会被修改。`,
      sources: targets.length > 1 ? targets.map((target) => ({ value: target.agentId, label: target.agentName || agentName(target.agentId), message: messageForTarget(target) })) : [],
      okText: "纳入管理",
      okClass: "primary"
    });
    if (!answer.ok) return;
    const target = targets.length === 1 ? targets[0] : targets.find((item) => item.agentId === answer.source);
    if (!target) return;
    try {
      const result = await invoke("skills/adopt", { agentId: target.agentId, name: skill.name, link: true });
      showToast(result.message || "技能已纳入管理");
      await loadOverview({ silent: true });
      await refreshDetail();
    } catch (error) {
      showToast(`纳入管理失败：${errorText(error)}`, true);
    }
  }

  async function removeSkill(skill) {
    const request = state.drawerRequest;
    const answer = await confirmAction({
      title: "移除技能",
      message: `将移除 ${skill.name} 的所有纳管目标，并把中心库移入插件回收站。未纳管的客户端副本不会被删除。`,
      okText: "移除"
    });
    if (!answer.ok) return;
    try {
      await invoke("skills/removeSkill", { name: skill.name, confirmed: true });
      if (request === state.drawerRequest) closeDetail();
      showToast("技能已移除");
      await loadOverview({ silent: true });
    } catch (error) {
      showToast(`移除失败：${errorText(error)}`, true);
    }
  }

  async function updateSkill(name) {
    let item;
    try {
      const result = await invoke("skills/previewUpdate", { names: [name] });
      item = (result.previews || [])[0];
    } catch (error) {
      showToast(`准备更新失败：${errorText(error)}`, true);
      return;
    }
    if (!item) {
      showToast("没有可用的更新预览", true);
      return;
    }
    const risks = item.candidate?.risk || [];
    const critical = risks.filter((risk) => risk.severity === "critical").length;
    const lines = [`已下载 ${name} 的新版本并完成风险扫描。`];
    lines.push(risks.length ? `共 ${risks.length} 项风险${critical ? `，其中 ${critical} 项高危` : ""}。` : "未发现明显风险。");
    if (item.warnings?.length) lines.push(`提示：${item.warnings.join("；")}`);
    lines.push("更新会替换中心库并同步所有纳管目标，旧版本备份到插件回收站。");
    const answer = await confirmAction({
      title: "更新技能",
      message: lines.join("\n"),
      details: riskList(risks, 8),
      checkLabel: "覆盖客户端中已本地修改的副本",
      ackLabel: critical ? "我已了解上述高危风险，仍要更新" : "",
      okText: "更新"
    });
    if (!answer.ok) return;
    try {
      const result = await invoke("skills/update", {
        previewIds: [item.previewId],
        overwriteLocal: Boolean(answer.checked),
        confirmed: true,
        acknowledgeRisk: Boolean(answer.acknowledged)
      });
      showToast(result.message || "技能已更新");
      await loadOverview({ silent: true });
      await refreshDetail();
    } catch (error) {
      showToast(`更新失败：${errorText(error)}`, true);
    }
  }

  async function checkOneUpdate(name) {
    try {
      showToast("正在检查更新…");
      const result = await invoke("skills/checkUpdates", { names: [name], force: true });
      const item = result.results?.[0];
      if (!item) showToast("没有检查结果", true);
      else if (item.status === "update") showToast(item.message ? `发现新版本：${item.message}` : `发现新版本 ${shortHash(item.commit)}`);
      else if (item.status === "current") showToast("已是最新版本");
      else if (item.status === "unsupported") showToast(item.message || "该来源不支持检查更新");
      else showToast(item.error || item.message || "检查完成", Boolean(item.error));
      await loadOverview({ silent: true });
      await refreshDetail();
    } catch (error) {
      showToast(`检查更新失败：${errorText(error)}`, true);
    }
  }

  async function checkAllUpdates() {
    const names = (state.overview?.skills || []).filter((skill) => skill.managed && skill.source?.type === "github").map((skill) => skill.name);
    if (!names.length) {
      showToast("没有可检查更新的 GitHub 来源技能");
      return;
    }
    const button = $("check-updates");
    setBusy(button, true, "检查中…");
    try {
      const result = await invoke("skills/checkUpdates", { names, force: true });
      const updates = (result.results || []).filter((item) => item.status === "update").length;
      const errors = (result.results || []).filter((item) => item.status === "error").length;
      showToast(`检查完成：${updates} 个有更新${errors ? `，${errors} 个失败` : ""}`);
      await loadOverview({ silent: true });
    } catch (error) {
      showToast(`检查更新失败：${errorText(error)}`, true);
    } finally {
      setBusy(button, false);
    }
  }

  async function copyText(value) {
    if (!value) return;
    try {
      await navigator.clipboard.writeText(String(value));
      showToast("已复制");
    } catch {
      const textarea = h("textarea", { style: { position: "fixed", left: "-9999px" } }, String(value));
      document.body.append(textarea);
      textarea.select();
      try { document.execCommand("copy"); showToast("已复制"); } catch { showToast("复制失败，请手动选择文本", true); }
      textarea.remove();
    }
  }

  function confirmAction(options) {
    return new Promise((resolve) => {
      if (state.modalResolve) {
        state.modalResolve({ ok: false, checked: false, acknowledged: false, source: "" });
        state.modalResolve = null;
      }
      state.modalResolve = resolve;
      $("modal-title").textContent = options.title || "确认操作";
      $("modal-message").textContent = options.message || "";
      const extra = $("modal-extra");
      clear(extra);
      extra.hidden = !options.details;
      if (options.details) extra.append(options.details);
      const checkRow = $("modal-check-row");
      const check = $("modal-check");
      checkRow.hidden = !options.checkLabel;
      $("modal-check-label").textContent = options.checkLabel || "";
      check.checked = Boolean(options.checkDefault);
      const ackRow = $("modal-ack-row");
      const ack = $("modal-ack");
      ackRow.hidden = !options.ackLabel;
      $("modal-ack-label").textContent = options.ackLabel || "";
      ack.checked = false;
      $("modal-ok").textContent = options.okText || "确认";
      $("modal-ok").className = options.okClass || "danger primary";
      const sources = options.sources || [];
      const select = $("modal-source");
      clear(select);
      select.append(h("option", { value: "" }, "请选择来源客户端"));
      for (const source of sources) select.append(h("option", { value: source.value }, source.label));
      select.value = "";
      $("modal-source-row").hidden = !sources.length;
      const sync = () => {
        $("modal-ok").disabled = (sources.length > 0 && !select.value) || (Boolean(options.ackLabel) && !ack.checked);
      };
      sync();
      select.onchange = () => {
        const source = sources.find((item) => item.value === select.value);
        $("modal-message").textContent = source?.message || options.message || "";
        sync();
      };
      ack.onchange = sync;
      openOverlay("modal");
    });
  }

  function closeModal(ok) {
    if (ok && $("modal-ok").disabled) return;
    const resolve = state.modalResolve;
    state.modalResolve = null;
    const checked = $("modal-check").checked;
    const acknowledged = $("modal-ack").checked;
    const source = $("modal-source").value;
    closeOverlay("modal");
    if (resolve) resolve({ ok, checked, acknowledged, source });
  }

  function showView(view) {
    if (!views.includes(view)) return;
    state.view = view;
    for (const name of views) {
      $(`view-${name}`).hidden = name !== view;
    }
    for (const button of document.querySelectorAll(".section-menu [data-view]")) {
      button.setAttribute("aria-selected", String(button.dataset.view === view));
    }
    $("page-title").textContent = viewTitles[view];
    $("breadcrumb-view").textContent = viewTitles[view];
    $("global-toolbar").hidden = false;
    const skillToolbar = view === "overview" || view === "library";
    $("check-updates").hidden = !skillToolbar;
    $("open-install").hidden = view === "mcp" || view === "settings";
    $("add-mcp").hidden = view !== "mcp";
    if (view === "settings") renderSettings();
    if (view === "install") renderPreview();
    if (view === "library") renderLibrary();
    if (view === "mcp") renderMCP();
    window.scrollTo({ top: 0, behavior: "smooth" });
    $("section-menu").classList.remove("open");
    $("menu-toggle").setAttribute("aria-expanded", "false");
  }

  function renderPreview() {
    const panel = $("preview-panel");
    const options = $("install-options");
    clear(panel);
    const preview = state.preview;
    if (!preview) {
      $("install-button").disabled = true;
      options.hidden = true;
      panel.append(h("div", { class: "empty-state compact" },
        h("h3", {}, "尚未解析来源"),
        h("p", {}, "填写左侧来源后点击“解析来源”。解析只下载到临时目录，不会写入客户端。")
      ));
      return;
    }
    options.hidden = false;
    const header = h("div", { class: "card-heading", style: { marginBottom: "10px" } },
      h("div", {},
        h("h3", {}, `发现 ${preview.candidates.length} 个技能`),
        h("p", { class: "subtle" }, `${sourceLabel({ source: preview.source })}${preview.source.path ? ` · ${preview.source.path}` : ""}`)
      ),
      h("div", { class: "filters" },
        h("button", { type: "button", class: "subtle-button", onclick: () => { state.selectedCandidates = new Set(preview.candidates.map((item) => item.name)); renderPreview(); } }, "全选"),
        h("button", { type: "button", class: "subtle-button", onclick: () => { state.selectedCandidates = new Set(); renderPreview(); } }, "清空")
      )
    );
    panel.append(header);
    if (preview.warnings?.length) panel.append(h("p", { class: "hint" }, preview.warnings.join("；")));
    const list = h("div", { class: "candidate-list" });
    for (const candidate of preview.candidates) {
      const selected = state.selectedCandidates.has(candidate.name);
      const risks = candidate.risk || [];
      const critical = risks.filter((risk) => risk.severity === "critical").length;
      const high = risks.filter((risk) => risk.severity === "high").length;
      const row = h("div", {
        class: `candidate${selected ? " selected" : ""}`,
        onclick: (event) => {
          if (event.target.closest("input, button, label")) return;
          state.previewDetail = candidate.name;
          renderPreview();
        }
      },
        h("input", {
          type: "checkbox", checked: selected,
          onchange: (event) => {
            if (event.target.checked) state.selectedCandidates.add(candidate.name);
            else state.selectedCandidates.delete(candidate.name);
            renderPreview();
          }
        }),
        h("div", {},
          h("div", { class: "candidate-name" },
            candidate.name,
            candidate.plugin ? h("span", { class: "chip managed" }, `${candidate.plugin.kind} 插件`) : null
          ),
          h("div", { class: "candidate-desc" }, candidate.description || "没有 description"),
          h("div", { class: "candidate-meta" },
            h("span", {}, `${candidate.files} 个文件 · ${formatBytes(candidate.size)}`),
            candidate.warnings?.length ? h("span", {}, `${candidate.warnings.length} 个提示`) : null,
            candidate.requiredTools?.length ? h("span", {}, `工具：${candidate.requiredTools.join("、")}`) : null,
            candidate.plugin?.name ? h("span", {}, candidate.plugin.name) : null
          )
        ),
        h("div", { class: "candidate-actions" },
          critical ? h("span", { class: "risk-pill critical" }, `${critical} 高危`) : null,
          high ? h("span", { class: "risk-pill high" }, `${high} 高风险`) : null,
          risks.length ? h("span", { class: "risk-pill medium" }, `${risks.length} 项风险`) : h("span", { class: "subtle" }, "未发现明显风险")
        )
      );
      list.append(row);
      if (state.previewDetail === candidate.name) list.append(candidateDetail(candidate));
    }
    panel.append(list);
    const detailCandidate = preview.candidates.find((item) => item.name === state.previewDetail) || preview.candidates[0];
    if (!state.previewDetail && detailCandidate) {
      state.previewDetail = detailCandidate.name;
      renderPreview();
      return;
    }
    renderTargets();
    const installButton = $("install-button");
    installButton.disabled = state.selectedCandidates.size === 0;
    setStatus("install-status", `${state.selectedCandidates.size} 个技能 · ${state.selectedTargets.size ? `${state.selectedTargets.size} 个客户端` : "仅中心库"}`);
  }

  function candidateDetail(candidate) {
    const box = h("div", { class: "preview-detail" });
    box.append(h("h3", {}, candidate.name));
    if (candidate.warnings?.length) {
      box.append(h("p", { class: "hint" }, `提示：${candidate.warnings.join("；")}`));
    }
    const risks = candidate.risk || [];
    if (risks.length) {
      box.append(riskList(risks, 10));
    } else {
      box.append(h("p", { class: "hint" }, "基础规则未发现明显风险；安装前仍建议查看文件内容。"));
    }
    if (candidate.contentPreview) {
      box.append(h("div", { style: { marginTop: "10px" } }, renderMarkdown(candidate.contentPreview)));
    }
    return box;
  }

  function renderTargets() {
    const wrap = $("install-targets");
    clear(wrap);
    const agents = state.overview?.agents || [];
    for (const agent of agents) {
      const checked = state.selectedTargets.has(agent.id);
      const disabled = !agent.detected && !agent.custom;
      wrap.append(h("label", {
        class: `target-option${checked ? " checked" : ""}`,
        title: disabled ? "未检测到该客户端；可在设置中指定技能目录" : agent.path
      },
        h("input", {
          type: "checkbox", checked, disabled, dataset: { agentId: agent.id },
          onchange: (event) => {
            const restoreFocus = document.activeElement === event.target;
            if (event.target.checked) state.selectedTargets.add(agent.id);
            else state.selectedTargets.delete(agent.id);
            renderPreview();
            if (restoreFocus) [...document.querySelectorAll("#install-targets input")].find((input) => input.dataset.agentId === agent.id)?.focus();
          }
        }),
        h("span", {},
          h("strong", {}, agent.name),
          h("small", { title: agent.path }, agent.detected ? agent.path : "未检测到")
        )
      ));
    }
    const hint = $("target-hint");
    if (!state.selectedTargets.size) hint.textContent = "不选择客户端时只保存到中心库，之后可在技能详情里下发。";
    else if (state.selectedTargets.has("codex") && state.selectedTargets.has("universal")) hint.textContent = "Codex 也可能读取通用目录；同时选择两处可能出现同名技能，请确认后再安装。";
    else hint.textContent = "链接模式优先使用软链接；Windows 下自动回退到目录联接或副本。";
    for (const button of document.querySelectorAll("#install-mode button")) {
      button.setAttribute("aria-pressed", String(button.dataset.mode === state.installMode));
    }
    $("install-overwrite").checked = state.installOverwrite;
    $("install-ack").checked = state.installAck;
  }

  function sourceInput() {
    if (state.sourceType === "github") {
      return { type: "github", value: $("source-value").value.trim(), ref: $("source-ref").value.trim(), path: $("source-path").value.trim() };
    }
    if (state.sourceType === "zip") {
      if (!state.zipBase64) throw new Error("请选择 zip 或 tar.gz 压缩包");
      return { type: "zip", zipBase64: state.zipBase64, fileName: state.zipName };
    }
    return { type: "local", value: $("local-value").value.trim() };
  }

  // Invalidate reviewed content and any replies for a previous source.
  function invalidatePreview() {
    ++state.previewRequest;
    state.preview = null;
    state.selectedCandidates = new Set();
    state.previewDetail = "";
    state.installAck = false;
    setBusy($("preview-source"), false);
    setStatus("source-status", "");
    renderPreview();
  }

  async function previewSource() {
    const button = $("preview-source");
    let input;
    try {
      input = sourceInput();
    } catch (error) {
      showToast(errorText(error), true);
      return;
    }
    invalidatePreview();
    const request = state.previewRequest;
    setBusy(button, true, "解析中…");
    setStatus("source-status", "正在下载并扫描…");
    try {
      const preview = await invoke("skills/previewSource", { source: input });
      if (request !== state.previewRequest) return;
      state.preview = preview;
      state.previewDetail = preview.candidates[0]?.name || "";
      state.selectedCandidates = new Set(preview.candidates.length <= 15 ? preview.candidates.map((item) => item.name) : []);
      setStatus("source-status", `发现 ${preview.candidates.length} 个技能`);
      renderPreview();
    } catch (error) {
      if (request !== state.previewRequest) return;
      state.preview = null;
      setStatus("source-status", "");
      renderPreview();
      showToast(`解析失败：${errorText(error)}`, true);
    } finally {
      if (request === state.previewRequest) setBusy(button, false);
    }
  }

  async function installPreview() {
    if (!state.preview) return;
    if (!state.selectedCandidates.size) {
      showToast("请选择至少一个技能", true);
      return;
    }
    const preview = state.preview;
    const request = state.previewRequest;
    const input = {
      previewId: preview.id,
      names: [...state.selectedCandidates],
      targets: [...state.selectedTargets],
      mode: state.installMode,
      overwrite: state.installOverwrite,
      acknowledgeRisk: state.installAck
    };
    const chosen = preview.candidates.filter((candidate) => input.names.includes(candidate.name));
    const critical = chosen.some((candidate) => (candidate.risk || []).some((risk) => risk.severity === "critical"));
    if (critical && !input.acknowledgeRisk) {
      showToast("选中技能包含高危风险项，请先勾选风险确认", true);
      return;
    }
    const answer = await confirmAction({
      title: "确认安装",
      message: `${chosen.map((candidate) => {
        const skill = state.overview?.skills?.find((item) => item.name === candidate.name);
        const selected = input.targets;
        const existing = (skill?.targets || []).filter((target) => target.exists && selected.includes(target.agentId)).map((target) => target.agentId);
        return `技能：${candidate.name}\n${selected.length ? targetChangeSummary(existing, selected) : "仅保存到中心库；之后可在技能详情里下发。"}`;
      }).join("\n\n")}\n\n${input.targets.length ? `仅向所选目录直接同步；已有链接会随中心库内容变化。${input.overwrite ? "同名或未纳管目录会先备份到插件回收站。" : "如果存在同名或未纳管目录，安装会中止。"}` : "不写入任何客户端目录。"}`,
      checkLabel: critical ? "我已查看风险并确认安装" : "",
      checkDefault: input.acknowledgeRisk,
      okText: "开始安装"
    });
    if (!answer.ok) return;
    if (request !== state.previewRequest || state.preview !== preview) {
      showToast("来源预览已变化，请重新确认安装", true);
      return;
    }
    if (critical && !answer.checked) {
      showToast("需要勾选风险确认", true);
      return;
    }
    const button = $("install-button");
    setBusy(button, true, "安装中…");
    try {
      const result = await invoke("skills/install", {
        ...input,
        acknowledgeRisk: Boolean(answer.checked || input.acknowledgeRisk)
      });
      showToast(result.message || "安装完成");
      const current = request === state.previewRequest;
      if (current) {
        invalidatePreview();
        state.installOverwrite = false;
      }
      await loadOverview({ silent: true });
      if (current) showView("library");
    } catch (error) {
      showToast(`安装失败：${errorText(error)}`, true);
    } finally {
      setBusy(button, false);
      button.disabled = !state.preview || !state.selectedCandidates.size;
    }
  }

  function markSettingsDirty() {
    state.settingsDirty = true;
    ++state.settingsRevision;
    setStatus("settings-status", "有未保存的更改");
  }

  function renderSettings() {
    if (!state.overview || state.settingsDirty) return;
    const settings = state.overview.settings || {};
    $("settings-proxy").value = settings.proxy || "";
    $("settings-token").value = "";
    $("settings-token").placeholder = settings.hasToken ? "已配置；留空保持不变" : "可选；用于 GitHub API 和私有仓库";
    $("settings-clear-token").checked = false;
    $("settings-mode").value = settings.defaultMode || "auto";
    $("settings-scan").value = settings.scanLevel || "basic";
    $("settings-max").value = String(settings.maxDownloadMB || 50);
    const paths = $("agent-paths");
    clear(paths);
    const agents = (state.overview.agents || []).filter((agent) => agent.builtin);
    for (const agent of agents) {
      const input = h("input", { type: "text", value: settings.agentPaths?.[agent.id] || agent.defaultPath || agent.path, placeholder: agent.defaultPath || agent.path });
      input.dataset.agentId = agent.id;
      input.dataset.defaultPath = agent.defaultPath || agent.path;
      paths.append(h("div", { class: "path-row" },
        h("div", { class: "path-name" }, agent.name),
        input,
        h("div", { class: `path-state${agent.detected ? " on" : ""}` }, agent.detected ? "已检测" : "未检测")
      ));
    }
    renderCustomAgents(settings.customAgents || []);
  }

  function renderCustomAgents(customAgents) {
    const wrap = $("custom-agents");
    clear(wrap);
    if (!customAgents.length) {
      wrap.append(h("p", { class: "subtle" }, "还没有自定义客户端。"));
      return;
    }
    customAgents.forEach((agent, index) => {
      const id = h("input", { type: "text", value: agent.id || "", placeholder: "id，例如 my-agent" });
      const name = h("input", { type: "text", value: agent.name || "", placeholder: "显示名称" });
      const path = h("input", { type: "text", value: agent.path || "", placeholder: "技能目录，例如 C:\\tools\\my-agent\\skills" });
      id.dataset.customField = "id";
      name.dataset.customField = "name";
      path.dataset.customField = "path";
      const row = h("div", { class: "custom-row", dataset: { customIndex: String(index) } },
        id, name, path,
        h("button", { type: "button", class: "danger", onclick: () => { row.remove(); markSettingsDirty(); } }, "移除")
      );
      wrap.append(row);
    });
  }

  function collectCustomAgents() {
    const rows = [...document.querySelectorAll("#custom-agents .custom-row")];
    return rows.map((row) => ({
      id: row.querySelector('[data-custom-field="id"]').value.trim(),
      name: row.querySelector('[data-custom-field="name"]').value.trim(),
      path: row.querySelector('[data-custom-field="path"]').value.trim()
    })).filter((item) => item.id || item.name || item.path);
  }

  async function saveSettings() {
    if (!state.overview) return;
    const button = $("save-settings");
    const agentPaths = {};
    for (const input of document.querySelectorAll("#agent-paths input[data-agent-id]")) {
      const value = input.value.trim();
      if (value && value !== input.dataset.defaultPath) agentPaths[input.dataset.agentId] = value;
    }
    const payload = {
      proxy: $("settings-proxy").value.trim(),
      defaultMode: $("settings-mode").value,
      scanLevel: $("settings-scan").value,
      maxDownloadMB: Number($("settings-max").value || 50),
      agentPaths,
      customAgents: collectCustomAgents()
    };
    const token = $("settings-token").value.trim();
    if (token) payload.githubToken = token;
    if ($("settings-clear-token").checked) payload.clearToken = true;
    const revision = state.settingsRevision;
    state.settingsDirty = true;
    setBusy(button, true, "保存中…");
    try {
      await invoke("skills/saveSettings", payload);
      showToast("设置已保存");
      state.settingsLoaded = false;
      const refreshed = await loadOverview({ silent: true, replacePending: true });
      if (revision === state.settingsRevision && refreshed) {
        state.settingsDirty = false;
        renderSettings();
        setStatus("settings-status", "已保存");
      } else if (revision === state.settingsRevision) {
        setStatus("settings-status", "已保存；刷新失败，暂时保留表单");
      }
    } catch (error) {
      showToast(`保存失败：${errorText(error)}`, true);
    } finally {
      setBusy(button, false);
    }
  }

  async function testConnection() {
    const button = $("test-connection");
    setBusy(button, true, "测试中…");
    setStatus("connection-status", "");
    try {
      const result = await invoke("skills/testConnection");
      setStatus("connection-status", result.message || "连接成功");
      showToast(result.message || "GitHub 连接成功");
    } catch (error) {
      setStatus("connection-status", "连接失败");
      showToast(`连接失败：${errorText(error)}`, true);
    } finally {
      setBusy(button, false);
    }
  }

  function bindEvents() {
    for (const button of document.querySelectorAll(".section-menu [data-view]")) {
      button.addEventListener("click", () => showView(button.dataset.view));
    }
    $("menu-toggle").addEventListener("click", () => {
      const menu = $("section-menu");
      const open = menu.classList.toggle("open");
      $("menu-toggle").setAttribute("aria-expanded", String(open));
    });
    $("refresh").addEventListener("click", () => refreshAll());
    $("rescan").addEventListener("click", () => refreshAll());
    $("check-updates").addEventListener("click", checkAllUpdates);
    $("open-install").addEventListener("click", () => showView("install"));
    $("add-mcp").addEventListener("click", openMCPAdd);
    $("overview-mcp-more").addEventListener("click", () => showView("mcp"));
    for (const button of document.querySelectorAll('[data-action="add-mcp"]')) {
      button.addEventListener("click", openMCPAdd);
    }
    $("mcp-overview-more")?.addEventListener("click", () => showView("mcp"));
    $("mcp-search").addEventListener("input", (event) => { state.mcpQuery = event.target.value; renderMCPList(); });
    $("mcp-status").addEventListener("change", (event) => { state.mcpStatusFilter = event.target.value; renderMCPList(); });
    $("mcp-import-button").addEventListener("click", openMCPImport);
    $("mcp-close").addEventListener("click", closeMCP);
    $("mcp-backdrop").addEventListener("click", closeMCP);
    $("mcp-transport").addEventListener("change", syncMCPTransportFields);
    $("mcp-source").addEventListener("change", (event) => {
      const name = state.mcpDetail?.view?.name;
      if (name) openMCPDetail(name, event.target.value);
    });
    $("mcp-save").addEventListener("click", saveMCPForm);
    $("mcp-remove").addEventListener("click", removeMCPForm);
    $("mcp-adopt").addEventListener("click", adoptMCPForm);
    $("mcp-import-open").addEventListener("click", openMCPImport);
    $("mcp-import-parse").addEventListener("click", parseMCPImport);
    $("mcp-import-text").addEventListener("input", () => {
      ++state.importRequest;
      state.mcpImportServers = [];
      $("mcp-import-apply").hidden = true;
      $("mcp-import-select-row").hidden = true;
      setStatus("mcp-import-status", "内容已更改，请重新解析");
    });
    $("mcp-import-apply").addEventListener("click", applyMCPImport);
    $("mcp-import-close").addEventListener("click", closeMCPImport);
    $("mcp-import-select").addEventListener("change", (event) => { state.mcpImportIndex = Number(event.target.value || 0); });
    $("mcp-import-modal").addEventListener("click", (event) => { if (event.target === $("mcp-import-modal")) closeMCPImport(); });
    $("overview-more").addEventListener("click", () => showView("library"));
    for (const button of document.querySelectorAll('[data-action="go-install"]')) {
      button.addEventListener("click", () => showView("install"));
    }
    $("library-search").addEventListener("input", (event) => { state.query = event.target.value; renderLibrary(); });
    $("library-status").addEventListener("change", (event) => { state.statusFilter = event.target.value; renderLibrary(); });
    $("library-managed-only").addEventListener("change", (event) => { state.managedOnly = event.target.checked; renderLibrary(); });
    for (const button of document.querySelectorAll("#source-type button")) {
      button.addEventListener("click", () => {
        state.sourceType = button.dataset.type;
        for (const item of document.querySelectorAll("#source-type button")) item.setAttribute("aria-pressed", String(item.dataset.type === state.sourceType));
        $("source-github").hidden = state.sourceType !== "github";
        $("source-zip").hidden = state.sourceType !== "zip";
        $("source-local").hidden = state.sourceType !== "local";
        invalidatePreview();
      });
    }
    for (const id of ["source-value", "source-ref", "source-path", "local-value"]) {
      $(id).addEventListener("input", invalidatePreview);
    }
    $("zip-file").addEventListener("change", async (event) => {
      invalidatePreview();
      const file = event.target.files?.[0];
      state.zipBase64 = "";
      state.zipName = "";
      state.zipSize = 0;
      if (!file) return;
      if (file.size > 4 * 1024 * 1024) {
        showToast("压缩包超过 4 MiB；请改用本地目录或 GitHub 地址", true);
        event.target.value = "";
        return;
      }
      try {
        state.zipBase64 = await readFileBase64(file);
        state.zipName = file.name;
        state.zipSize = file.size;
        setStatus("source-status", `已选择 ${file.name} · ${formatBytes(file.size)}`);
      } catch (error) {
        showToast(`读取压缩包失败：${errorText(error)}`, true);
      }
    });
    $("preview-source").addEventListener("click", previewSource);
    $("install-button").addEventListener("click", installPreview);
    for (const button of document.querySelectorAll("#install-mode button")) {
      button.addEventListener("click", () => {
        state.installMode = button.dataset.mode;
        renderTargets();
      });
    }
    $("install-overwrite").addEventListener("change", (event) => { state.installOverwrite = event.target.checked; });
    $("install-ack").addEventListener("change", (event) => { state.installAck = event.target.checked; });
    $("view-settings").addEventListener("input", markSettingsDirty);
    $("view-settings").addEventListener("change", markSettingsDirty);
    $("save-settings").addEventListener("click", saveSettings);
    $("test-connection").addEventListener("click", testConnection);
    $("add-custom-agent").addEventListener("click", () => {
      const current = collectCustomAgents();
      current.push({ id: "", name: "", path: "" });
      renderCustomAgents(current);
      markSettingsDirty();
    });
    $("detail-close").addEventListener("click", closeDetail);
    $("drawer-backdrop").addEventListener("click", closeDetail);
    for (const button of document.querySelectorAll("#detail-tabs button")) {
      button.addEventListener("click", () => {
        ++state.fileRequest;
        state.detailTab = button.dataset.tab;
        state.fileView = null;
        renderDetail();
      });
    }
    $("modal-cancel").addEventListener("click", () => closeModal(false));
    $("modal-ok").addEventListener("click", () => closeModal(true));
    $("modal").addEventListener("click", (event) => { if (event.target === $("modal")) closeModal(false); });
    document.addEventListener("keydown", (event) => {
      const top = state.overlayStack[state.overlayStack.length - 1];
      if (!top) return;
      if (event.key === "Tab") {
        const items = overlayFocusables($(top.id));
        const first = items[0];
        const last = items[items.length - 1];
        if (!items.length) { event.preventDefault(); $(top.id).focus(); }
        else if (!items.includes(document.activeElement) || (event.shiftKey && document.activeElement === first) || (!event.shiftKey && document.activeElement === last)) {
          event.preventDefault();
          (event.shiftKey ? last : first).focus();
        }
      }
      if (event.key !== "Escape") return;
      event.preventDefault();
      if (top.id === "modal") closeModal(false);
      else if (top.id === "mcp-import-modal") closeMCPImport();
      else if (top.id === "mcp-drawer") closeMCP();
      else if (top.id === "detail-drawer") closeDetail();
    });
  }

  function readFileBase64(file) {
    return new Promise((resolve, reject) => {
      const reader = new FileReader();
      reader.onerror = () => reject(new Error("读取文件失败"));
      reader.onload = () => {
        const result = String(reader.result || "");
        const index = result.indexOf(",");
        resolve(index >= 0 ? result.slice(index + 1) : result);
      };
      reader.readAsDataURL(file);
    });
  }

  async function init() {
    bindEvents();
    showView("overview");
    if (!dbx) {
      showWarning("未检测到 DBX 插件宿主。若在浏览器中调试，请使用 .dbx-dev/mock.html。");
      return;
    }
    try {
      await dbx.ready;
      if (typeof dbx.onContext === "function") dbx.onContext(() => {});
      await refreshAll();
    } catch (error) {
      showWarning(`工作台初始化失败：${errorText(error)}`);
    }
  }

  init();
})();
