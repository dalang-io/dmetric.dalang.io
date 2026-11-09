(() => {
  "use strict";

  const $ = (id) => document.getElementById(id);
  const nf1 = new Intl.NumberFormat("id-ID", { maximumFractionDigits: 1 });
  const nf2 = new Intl.NumberFormat("id-ID", { minimumFractionDigits: 2, maximumFractionDigits: 2 });
  const nf0 = new Intl.NumberFormat("id-ID", { maximumFractionDigits: 0 });
  const UNITS = ["B", "KB", "MB", "GB", "TB", "PB"];

  const state = {
    snap: null,
    points: [],
    minutes: 15,
    settings: null,
    alerts: [],
    activeSig: "",
    lastMsg: 0,
    settingsBusy: false,
    uiRTT: null,
    candidates: null,
    updateData: null,
    updateBusy: false,
    openDisks: new Set(),
  };

  // Release tags are vYYYY.M.P while the binary reports the bare version.
  function releaseTag(version) {
    const bare = String(version || "").replace(/^v/i, "");
    return bare ? `v${bare}` : "—";
  }

  function fmtBytes(value) {
    let v = Number(value);
    if (!isFinite(v) || v < 0) v = 0;
    let i = 0;
    while (v >= 1024 && i < UNITS.length - 1) {
      v /= 1024;
      i++;
    }
    return i === 0 ? `${nf0.format(v)} B` : `${nf1.format(v)} ${UNITS[i]}`;
  }

  function fmtRate(value) {
    return `${fmtBytes(value)}/s`;
  }

  function fmtPct(value) {
    return nf1.format(Number(value) || 0);
  }

  function fmtMs(value) {
    if (value == null || !isFinite(value)) return "—";
    return `${nf1.format(value)} ms`;
  }

  function latencyClass(ms) {
    if (ms >= 50) return "latCrit";
    if (ms >= 20) return "latWarn";
    return "";
  }

  const APP_PRESETS = {
    "chromeleon, sqlservr": ["chromeleon", "sqlservr"],
    "sqlservr": ["sqlservr"],
    "nginx, apache, httpd, node, php-fpm": ["nginx", "apache", "httpd", "node", "php-fpm"],
    "docker, containerd, wsl": ["docker", "containerd", "wsl"],
    "": [],
  };

  function listOf(field) {
    return splitList($("settingsForm").elements.namedItem(field).value);
  }

  function setList(field, values) {
    const clean = [];
    const seen = new Set();
    for (const raw of values) {
      const value = String(raw || "").trim();
      if (!value) continue;
      const key = value.toLowerCase();
      if (seen.has(key)) continue;
      seen.add(key);
      clean.push(value);
    }
    $("settingsForm").elements.namedItem(field).value = clean.join(", ");
    renderComboChips(field);
    if (field === "watch_apps") syncPreset();
  }

  function renderComboChips(field) {
    const box = field === "watch_apps" ? $("watchChips") : $("probeChips");
    box.replaceChildren();
    for (const value of listOf(field)) {
      const chip = el("span", "chip");
      chip.appendChild(el("span", "", value));
      const remove = el("button", "", "×");
      remove.type = "button";
      remove.title = `Hapus ${value}`;
      remove.addEventListener("click", () => {
        setList(field, listOf(field).filter((item) => item.toLowerCase() !== value.toLowerCase()));
      });
      chip.appendChild(remove);
      box.appendChild(chip);
    }
  }

  function hideComboMenus() {
    $("watchMenu").hidden = true;
    $("probeMenu").hidden = true;
  }

  function addValue(field, value) {
    value = String(value || "").trim();
    if (!value) return;
    const list = listOf(field);
    if (list.length >= 8) {
      toast(field === "watch_apps" ? "Aplikasi fokus maksimal 8 pola" : "Probe jaringan maksimal 8 target", true);
      return;
    }
    if (list.some((item) => item.toLowerCase() === value.toLowerCase())) {
      hideComboMenus();
      return;
    }
    list.push(value);
    setList(field, list);
    const search = field === "watch_apps" ? $("watchSearch") : $("probeSearch");
    search.value = "";
    hideComboMenus();
  }

  function syncPreset() {
    const current = listOf("watch_apps").map((item) => item.toLowerCase()).join(", ");
    const preset = Object.keys(APP_PRESETS).find((key) =>
      APP_PRESETS[key].map((item) => item.toLowerCase()).join(", ") === current);
    $("appPreset").value = preset === undefined ? "" : preset;
  }

  function looksLikeHostPort(value) {
    return /^(\[[0-9a-fA-F:]+\]|[A-Za-z0-9._-]+):\d{1,5}$/.test(value);
  }

  function renderWatchMenu() {
    const menu = $("watchMenu");
    const rawQuery = $("watchSearch").value.trim();
    const query = rawQuery.toLowerCase();
    const selected = new Set(listOf("watch_apps").map((item) => item.toLowerCase()));
    const candidates = (state.candidates && state.candidates.processes) || [];
    let exact = false;
    const procs = candidates.filter((proc) => {
      const pattern = proc.name.replace(/\.exe$/i, "").trim();
      if (pattern.toLowerCase() === query) exact = true;
      if (selected.has(pattern.toLowerCase())) return false;
      return !query || proc.name.toLowerCase().includes(query);
    }).slice(0, 30);

    menu.replaceChildren();
    for (const proc of procs) {
      const option = el("button", "comboOption", `${proc.name} — ${proc.count}× · CPU ${fmtPct(proc.cpu_percent)}%`);
      option.type = "button";
      option.addEventListener("click", () => addValue("watch_apps", proc.name.replace(/\.exe$/i, "").trim()));
      menu.appendChild(option);
    }
    if (rawQuery && !selected.has(query) && !exact) {
      const custom = el("button", "comboOption", `+ Tambah pola "${rawQuery}"`);
      custom.type = "button";
      custom.addEventListener("click", () => addValue("watch_apps", rawQuery));
      menu.appendChild(custom);
    }
    if (!menu.childElementCount) {
      menu.appendChild(el("div", "comboEmpty", query ? "Tidak ada proses/pola yang cocok." : "Tidak ada proses terbaca."));
    }
    menu.hidden = false;
  }

  function renderProbeMenu() {
    const menu = $("probeMenu");
    const query = $("probeSearch").value.trim();
    const lowered = query.toLowerCase();
    const selected = new Set(listOf("network_probes").map((item) => item.toLowerCase()));
    const candidates = (state.candidates && state.candidates.targets) || [];
    const targets = candidates.filter((target) => {
      if (selected.has(target.target.toLowerCase())) return false;
      return !lowered || target.target.toLowerCase().includes(lowered) || target.label.toLowerCase().includes(lowered);
    }).slice(0, 30);

    menu.replaceChildren();
    const valid = looksLikeHostPort(query);
    if (query && valid && !selected.has(lowered)) {
      const custom = el("button", "comboOption", `+ Tambah target "${query}"`);
      custom.type = "button";
      custom.addEventListener("click", () => addValue("network_probes", query));
      menu.appendChild(custom);
    }
    for (const target of targets) {
      const option = el("button", "comboOption", `${target.label} → ${target.target}`);
      option.type = "button";
      option.addEventListener("click", () => addValue("network_probes", target.target));
      menu.appendChild(option);
    }
    if (!menu.childElementCount) {
      menu.appendChild(el("div", "comboEmpty", query && !valid
        ? "Ketik host:port yang valid, mis. 10.0.0.20:1433."
        : "Tidak ada port lokal; ketik host:port untuk menambah."));
    }
    menu.hidden = false;
  }

  function wireCombo(inputID, menuID, render) {
    const input = $(inputID);
    input.addEventListener("focus", render);
    input.addEventListener("input", render);
    input.addEventListener("keydown", (event) => {
      if (event.key === "Escape") {
        $(menuID).hidden = true;
        input.blur();
        return;
      }
      if (event.key === "ArrowDown" || event.key === "ArrowUp") {
        const options = [...$(menuID).querySelectorAll(".comboOption")];
        if (!options.length) return;
        event.preventDefault();
        options[event.key === "ArrowDown" ? 0 : options.length - 1].focus();
        return;
      }
      if (event.key === "Enter") {
        event.preventDefault();
        const first = $(menuID).querySelector(".comboOption");
        if (first) first.click();
      }
    });
  }

  function wireMenuNav(menuID, inputID) {
    $(menuID).addEventListener("keydown", (event) => {
      const options = [...$(menuID).querySelectorAll(".comboOption")];
      const i = options.indexOf(document.activeElement);
      if (i < 0) return;
      if (event.key === "ArrowDown") {
        event.preventDefault();
        options[Math.min(options.length - 1, i + 1)].focus();
      } else if (event.key === "ArrowUp") {
        event.preventDefault();
        if (i === 0) $(inputID).focus();
        else options[i - 1].focus();
      } else if (event.key === "Escape") {
        $(menuID).hidden = true;
        $(inputID).focus();
      }
    });
  }

  function splitList(value) {
    return String(value || "")
      .split(",")
      .map((item) => item.trim())
      .filter(Boolean);
  }

  function fmtUptime(seconds) {
    let s = Math.max(0, Math.floor(Number(seconds) || 0));
    const d = Math.floor(s / 86400);
    s -= d * 86400;
    const h = Math.floor(s / 3600);
    s -= h * 3600;
    const m = Math.floor(s / 60);
    const parts = [];
    if (d) parts.push(`${d}h`);
    if (h) parts.push(`${h}j`);
    parts.push(`${m}m`);
    return parts.join(" ");
  }

  function fmtClock(ms) {
    const d = new Date(ms);
    if (Number.isNaN(d.getTime())) return "—";
    const today = new Date();
    const sameDay = d.toDateString() === today.toDateString();
    return d.toLocaleString("id-ID", sameDay
      ? { hour: "2-digit", minute: "2-digit", second: "2-digit" }
      : { day: "2-digit", month: "short", hour: "2-digit", minute: "2-digit" });
  }

  function platformLabel(os) {
    switch ((os || "").toLowerCase()) {
      case "darwin": return "macOS";
      case "windows": return "Windows";
      case "linux": return "Linux";
      default: return os || "—";
    }
  }

  function el(tag, className, text) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined && text !== null) node.textContent = text;
    return node;
  }

  // ---------- charts ----------

  function setSeries(svg, seriesList, fixedMax) {
    const vb = svg.viewBox.baseVal;
    const w = vb && vb.width ? vb.width : 300;
    const h = vb && vb.height ? vb.height : 54;
    const lines = svg.querySelectorAll("path.line");
    const areas = svg.querySelectorAll("path.area");
    const maxData = Math.max(0, ...seriesList.flatMap((s) => s.data));
    const max = fixedMax || Math.max(maxData * 1.15, 1);
    const plot = h - 2;

    seriesList.forEach((series, index) => {
      const line = lines[index];
      const area = areas[index];
      if (!line || !area) return;
      const data = series.data;
      if (!data || data.length < 2) {
        line.setAttribute("d", "");
        area.setAttribute("d", "");
        return;
      }
      const step = w / Math.max(1, data.length - 1);
      let d = "";
      data.forEach((value, i) => {
        const ratio = Math.min(1, Math.max(0, (Number(value) || 0) / max));
        const x = i * step;
        const y = 1 + (1 - ratio) * plot;
        d += `${i === 0 ? "M" : "L"}${x.toFixed(2)} ${y.toFixed(2)}`;
      });
      line.setAttribute("d", d);
      const lastX = (data.length - 1) * step;
      area.setAttribute("d", `${d}L${lastX.toFixed(2)} ${h}L0 ${h}Z`);
    });
  }

  function tail(list, n) {
    return list.length > n ? list.slice(list.length - n) : list;
  }

  function peakOf(list) {
    let max = 0;
    for (const v of list) if (v > max) max = v;
    return max;
  }

  function lastOf(list) {
    return list.length ? list[list.length - 1] : 0;
  }

  function renderTrendMeta(pts) {
    const set = (id, text) => { $(id).textContent = text; };
    if (!pts.length) {
      for (const id of ["metaCPU", "metaRAM", "metaNet", "metaIO", "metaLat"]) set(id, "—");
      return;
    }
    const cpu = pts.map((p) => p.cpu);
    const ram = pts.map((p) => p.ram);
    set("metaCPU", `${fmtPct(lastOf(cpu))}% · puncak ${fmtPct(peakOf(cpu))}%`);
    set("metaRAM", `${fmtPct(lastOf(ram))}% · puncak ${fmtPct(peakOf(ram))}%`);
    set("metaNet", `puncak ↓${fmtRate(peakOf(pts.map((p) => p.net_rx)))} ↑${fmtRate(peakOf(pts.map((p) => p.net_tx)))}`);
    set("metaIO", `puncak R ${fmtRate(peakOf(pts.map((p) => p.disk_read)))} · W ${fmtRate(peakOf(pts.map((p) => p.disk_write)))}`);
    set("metaLat", `puncak ${fmtMs(Math.max(peakOf(pts.map((p) => p.disk_read_latency || 0)), peakOf(pts.map((p) => p.disk_write_latency || 0))))}`);
  }

  function renderCharts() {
    const pts = state.points;
    const spark = tail(pts, 150);
    setSeries($("cpuSpark"), [{ data: spark.map((p) => p.cpu) }], 100);
    setSeries($("ramSpark"), [{ data: spark.map((p) => p.ram) }], 100);
    setSeries($("netSpark"), [
      { data: spark.map((p) => p.net_rx) },
      { data: spark.map((p) => p.net_tx) },
    ]);
    setSeries($("ioSpark"), [
      { data: spark.map((p) => p.disk_read) },
      { data: spark.map((p) => p.disk_write) },
    ]);

    setSeries($("trendCPU"), [{ data: pts.map((p) => p.cpu) }], 100);
    setSeries($("trendRAM"), [{ data: pts.map((p) => p.ram) }], 100);
    setSeries($("trendNet"), [
      { data: pts.map((p) => p.net_rx) },
      { data: pts.map((p) => p.net_tx) },
    ]);
    setSeries($("trendIO"), [
      { data: pts.map((p) => p.disk_read) },
      { data: pts.map((p) => p.disk_write) },
    ]);
    setSeries($("trendLat"), [
      { data: pts.map((p) => p.disk_read_latency || 0) },
      { data: pts.map((p) => p.disk_write_latency || 0) },
    ]);
    renderTrendMeta(pts);
  }

  // ---------- rendering ----------

  // ok < 80% of alert threshold <= warning < threshold <= critical
  function levelFor(value, limit) {
    if (!limit || !isFinite(value)) return "ok";
    if (value >= limit) return "critical";
    if (value >= limit * 0.8) return "warning";
    return "ok";
  }

  function applyLevels(snap) {
    const t = (state.settings && state.settings.settings && state.settings.settings.thresholds) || {};
    $("cardCPU").dataset.level = levelFor(snap.cpu.percent, t.cpu_percent);
    $("cardRAM").dataset.level = levelFor(snap.memory.percent, t.memory_percent);
    $("cardStorage").dataset.level = levelFor(snap.storage.percent, t.storage_percent);
  }

  function render(snap) {
    if (!snap || !snap.host) return;
    state.snap = snap;
    const host = snap.host;
    $("hostName").textContent = host.hostname || "—";
    $("hostMeta").textContent = `${platformLabel(host.os)} · ${host.arch || "—"}`;
    $("hostMeta").title = [host.platform, host.platform_version, host.cpu_model].filter(Boolean).join(" · ");
    $("uptime").textContent = fmtUptime(host.uptime_seconds);

    $("cpuValue").textContent = fmtPct(snap.cpu.percent);
    $("cpuTag").textContent = `${snap.cpu.cores} core`;
    $("cpuLoad").textContent = snap.cpu.load_supported
      ? `load ${nf2.format(snap.cpu.load1 || 0)} · ${nf2.format(snap.cpu.load5 || 0)} · ${nf2.format(snap.cpu.load15 || 0)}`
      : "load rata-rata tidak tersedia di sistem ini";

    const m = snap.memory;
    $("ramValue").textContent = fmtPct(m.percent);
    $("ramTag").textContent = `${fmtBytes(m.total)} total`;
    const swap = m.swap_total > 0
      ? ` · swap ${fmtBytes(m.swap_used)} / ${fmtBytes(m.swap_total)}`
      : "";
    $("ramSub").textContent = `${fmtBytes(m.used)} / ${fmtBytes(m.total)}${swap}`;

    const st = snap.storage;
    $("storageValue").textContent = fmtPct(st.percent);
    $("storageTag").textContent = st.mount || "/";
    $("storageSub").textContent = `${fmtBytes(st.used)} / ${fmtBytes(st.total)} · bebas ${fmtBytes(st.free)}`;
    $("storageBar").style.width = `${Math.min(100, Math.max(0, st.percent))}%`;
    $("storageBarWrap").setAttribute("aria-valuenow", String(Math.round(st.percent)));

    const net = snap.network;
    $("rxValue").textContent = fmtRate(net.rx_bytes_per_sec);
    $("txValue").textContent = fmtRate(net.tx_bytes_per_sec);
    $("netTag").textContent = `↓ ${fmtBytes(net.rx_bytes_total)}`;
    $("netTotals").textContent = `Total RX ${fmtBytes(net.rx_bytes_total)} · TX ${fmtBytes(net.tx_bytes_total)}`;
    renderNetLatency(net);
    renderNetInterfaces(net);

    const io = snap.disk;
    $("readValue").textContent = fmtRate(io.read_bytes_per_sec);
    $("writeValue").textContent = fmtRate(io.write_bytes_per_sec);
    $("ioTag").textContent = `R ${fmtBytes(io.read_bytes_total)}`;
    $("ioTotals").textContent = `Total baca ${fmtBytes(io.read_bytes_total)} · tulis ${fmtBytes(io.write_bytes_total)}`;
    $("ioLatency").textContent = `Latensi baca ${fmtMs(io.read_latency_ms)} · tulis ${fmtMs(io.write_latency_ms)} · antrean ${io.queue_supported ? nf1.format(io.queue || 0) : "—"}`;

    applyLevels(snap);
    renderDiagnosis(snap.diagnosis);
    renderHardware(snap.hardware);
    renderVolumes(snap.storage && snap.storage.volumes);
    renderApps(snap.apps);
    renderProbes(net);
    renderPorts(snap.ports);
    renderProcesses(snap.processes, snap.host.cores);
    renderActiveAlerts(snap.alerts || []);
    renderWarnings(snap.warnings || []);
    maybeRefreshAlerts(snap.alerts || []);
    renderCharts();
  }

  function hwLine(label, value, title) {
    const row = el("div", "hwLine");
    row.appendChild(el("span", "", label));
    const strong = el("strong", "", value == null || value === "" ? "—" : String(value));
    if (title) strong.title = title;
    row.appendChild(strong);
    return row;
  }

  function hwBadge(status, text) {
    const level = status === "critical" ? "critical" : status === "warning" ? "warning" : status === "ok" ? "ok" : "ended";
    return el("span", `badge ${level}`, text || (status === "critical" ? "Kritis" : status === "warning" ? "Waspada" : status === "ok" ? "Normal" : "Tidak diketahui"));
  }

  function renderHardware(hw) {
    if (!hw) return;
    const status = hw.status || "unknown";
    const badge = $("hwStatus");
    badge.className = `badge ${status === "critical" ? "critical" : status === "warning" ? "warning" : status === "ok" ? "ok" : "ended"}`;
    badge.textContent = status === "critical" ? "Kritis" : status === "warning" ? "Waspada" : status === "ok" ? "Normal" : "Tidak diketahui";
    const source = $("hwSource");
    source.textContent = hw.source || "—";
    source.title = hw.collected_at ? `Diperbarui ${fmtClock(hw.collected_at)}` : "";

    const system = hw.system || {};
    const systemBody = $("hwSystem");
    systemBody.replaceChildren(
      hwLine("Vendor / model", [system.vendor, system.product].filter(Boolean).join(" ") || "—"),
      hwLine("Serial", system.serial),
      hwLine("BIOS", [system.bios_version, system.bios_date].filter(Boolean).join(" · ")),
      hwLine("WHEA (7 hr)", system.whea_supported ? (system.whea_errors ? `${nf0.format(system.whea_errors)} error` : "0") : "—"),
    );

    const cpu = hw.cpu || {};
    const cpuBody = $("hwCPU");
    cpuBody.replaceChildren(
      hwLine("Model", cpu.model),
      hwLine("Serial / ID", cpu.serial || "—"),
      hwLine("Core / thread", `${cpu.cores || 0} / ${cpu.threads || 0}`),
      hwLine("Clock max", cpu.max_mhz ? `${nf0.format(cpu.max_mhz)} MHz` : "—"),
      hwLine("Suhu", cpu.temperature_c == null ? "—" : `${nf1.format(cpu.temperature_c)} °C`, cpu.temperature_source || ""),
    );

    const memory = hw.memory || {};
    const memBody = $("hwMemory");
    memBody.replaceChildren(
      hwLine("Total", fmtBytes(memory.total)),
      hwLine("Modul", `${(memory.modules || []).length} DIMM`),
      hwLine("ECC correctable", memory.ecc_supported ? nf0.format(memory.ecc_correctable || 0) : "—"),
      hwLine("ECC uncorrectable", memory.ecc_supported ? nf0.format(memory.ecc_uncorrectable || 0) : "—"),
    );

    const modules = memory.modules || [];
    const moduleBody = $("hwModulesBody");
    if (!modules.length) {
      const tr = document.createElement("tr");
      const td = el("td", "empty", "Detail modul RAM tidak tersedia (butuh root/dmidecode atau CIM).");
      td.colSpan = 6;
      tr.appendChild(td);
      moduleBody.replaceChildren(tr);
    } else {
      const frag = document.createDocumentFragment();
      for (const module of modules) {
        const tr = document.createElement("tr");
        tr.append(
          el("td", "portAddr", module.locator || module.bank || "—"),
          el("td", "", module.manufacturer || "—"),
          el("td", "portAddr", module.serial || "—"),
          el("td", "portAddr", module.part_number || "—"),
          el("td", "num", module.size ? fmtBytes(module.size) : "—"),
          el("td", "num", module.speed_mhz ? `${nf0.format(module.speed_mhz)} MT/s` : "—"),
        );
        frag.appendChild(tr);
      }
      moduleBody.replaceChildren(frag);
    }

    const accelerators = hw.accelerators || [];
    $("hwGpuSection").hidden = !accelerators.length;
    $("hwGpuBlock").hidden = !accelerators.length;
    const gpuBody = $("hwGpuBody");
    if (accelerators.length) {
      const frag = document.createDocumentFragment();
      for (const accelerator of accelerators) {
        const tr = document.createElement("tr");
        const status = el("td", "");
        status.appendChild(hwBadge(accelerator.status, accelerator.status));
        tr.append(
          el("td", "procName", accelerator.name || "—"),
          el("td", "mutedText", accelerator.type || "—"),
          el("td", "", accelerator.vendor || "—"),
          el("td", "num", accelerator.memory ? fmtBytes(accelerator.memory) : "—"),
          el("td", "portAddr", accelerator.driver || "—"),
          status,
        );
        frag.appendChild(tr);
      }
      gpuBody.replaceChildren(frag);
    }

    const fans = hw.fans || [];
    $("hwFanSection").hidden = !fans.length;
    $("hwFanBlock").hidden = !fans.length;
    const fanBody = $("hwFanBody");
    if (fans.length) {
      const frag = document.createDocumentFragment();
      for (const fan of fans) {
        const tr = document.createElement("tr");
        const status = el("td", "");
        status.appendChild(hwBadge(fan.status, fan.status));
        tr.append(
          el("td", "procName", fan.name || "—"),
          el("td", "num", fan.rpm == null ? "—" : `${nf0.format(fan.rpm)}`),
          el("td", "num", fan.min_rpm == null ? "—" : `${nf0.format(fan.min_rpm)}`),
          status,
          el("td", "mutedText", fan.source || "—"),
          el("td", "mutedText", fan.detail || "—"),
        );
        if (fan.issues && fan.issues.length) tr.title = fan.issues.join("; ");
        frag.appendChild(tr);
      }
      fanBody.replaceChildren(frag);
    }

    const psus = hw.psus || [];
    $("hwPsuSection").hidden = !psus.length;
    $("hwPsuBlock").hidden = !psus.length;
    const psuBody = $("hwPsuBody");
    if (psus.length) {
      const frag = document.createDocumentFragment();
      for (const psu of psus) {
        const tr = document.createElement("tr");
        const status = el("td", "");
        status.appendChild(hwBadge(psu.status, psu.status));
        tr.append(
          el("td", "procName", psu.name || "—"),
          status,
          el("td", "mutedText", psu.detail || "—"),
          el("td", "mutedText", psu.source || "—"),
        );
        if (psu.issues && psu.issues.length) tr.title = psu.issues.join("; ");
        frag.appendChild(tr);
      }
      psuBody.replaceChildren(frag);
    }

    const storage = hw.storage || [];
    const body = $("hwStorageBody");
    if (!storage.length) {
      const tr = document.createElement("tr");
      const td = el("td", "empty", status === "unknown" ? "Menunggu data hardware…" : "Tidak ada disk fisik terdeteksi");
      td.colSpan = 9;
      tr.appendChild(td);
      body.replaceChildren(tr);
    } else {
      const frag = document.createDocumentFragment();
      for (const disk of storage) {
        const tr = document.createElement("tr");
        // Kernel names (sda/sdb) change across reboots; identify by WWN/by-id.
        const diskKey = disk.id || disk.name || "";
        const smart = disk.smart || [];
        const name = el("td", "procName diskId");
        if (smart.length) {
          const open = state.openDisks.has(diskKey);
          const toggle = el("button", "expandBtn");
          toggle.type = "button";
          toggle.setAttribute("aria-expanded", String(open));
          toggle.append(el("span", "chev", open ? "▾" : "▸"), el("span", "", diskKey || "—"));
          toggle.title = open ? "Sembunyikan detail SMART" : "Tampilkan detail SMART";
          toggle.addEventListener("click", () => {
            if (state.openDisks.has(diskKey)) state.openDisks.delete(diskKey);
            else state.openDisks.add(diskKey);
            if (state.snap) renderHardware(state.snap.hardware);
          });
          name.appendChild(toggle);
        } else {
          name.textContent = diskKey || "—";
        }
        name.title = [disk.id, disk.model, disk.serial, disk.name ? `kernel: ${disk.name} (berubah tiap boot)` : ""]
          .filter(Boolean).join(" · ");
        const media = el("td", "mutedText", [disk.media, disk.bus].filter(Boolean).join(" · ") || "—");
        const health = el("td", "");
        health.appendChild(hwBadge(disk.status, disk.health || undefined));
        health.title = disk.health_note || disk.operational_status || "";
        if (!disk.health && disk.health_note) {
          health.appendChild(el("span", "volMeta", disk.health_note));
        }
        const errors = (disk.read_errors || 0) + (disk.write_errors || 0) + (disk.media_errors || 0);
        const serial = el("td", "portAddr", disk.serial || "—");
        serial.title = disk.serial || "";
        tr.append(
          name,
          serial,
          media,
          el("td", "num", disk.size ? fmtBytes(disk.size) : "—"),
          health,
          el("td", "num", disk.temperature_c == null ? "—" : `${nf1.format(disk.temperature_c)} °C`),
          el("td", "num", disk.wear_percent == null ? "—" : `${nf1.format(disk.wear_percent)}%`),
          el("td", "num", disk.power_on_hours == null ? "—" : `${nf0.format(disk.power_on_hours)} j`),
          el("td", "num", errors ? nf0.format(errors) : "0"),
        );
        if (disk.issues && disk.issues.length) tr.title = disk.issues.join("; ");
        frag.appendChild(tr);
        if (smart.length && state.openDisks.has(diskKey)) frag.appendChild(smartDetailRow(disk, smart));
      }
      body.replaceChildren(frag);
    }

    const issues = hw.issues || [];
    const list = $("hwIssues");
    if (!issues.length) {
      const li = el("li", status === "ok" ? "ok" : "", status === "ok"
        ? "Tidak ada masalah hardware terdeteksi."
        : status === "unknown" ? "Pemeriksaan hardware belum selesai/belum tersedia." : "Tidak ada rincian masalah.");
      list.replaceChildren(li);
    } else {
      const frag = document.createDocumentFragment();
      for (const issue of issues) {
        const li = el("li", status === "critical" ? "critical" : "", issue);
        frag.appendChild(li);
      }
      list.replaceChildren(frag);
    }
    const notes = hw.notes || [];
    $("hwNotes").textContent = notes.join(" · ");
  }

  // Full SMART readout of one disk: every attribute the drive reports.
  function smartDetailRow(disk, smart) {
    const tr = el("tr", "smartRow");
    const td = el("td", "smartCell");
    td.colSpan = 9;
    const ata = smart.some((m) => m.normalized != null);
    const table = el("table", "table compact smartTable");
    table.setAttribute("aria-label", `Detail SMART ${disk.id || disk.name}`);
    const head = el("tr");
    const cols = ata ? ["ID", "Atribut", "Nilai", "Worst", "Ambang", "Mentah", "Status"] : ["Metrik", "Nilai", "Status"];
    cols.forEach((label, i) => {
      const th = el("th", ata ? (i >= 2 && i <= 4 ? "num" : "") : (i === 1 ? "num" : ""), label);
      head.appendChild(th);
    });
    const thead = el("thead");
    thead.appendChild(head);
    const tbody = el("tbody");
    for (const m of smart) {
      const row = el("tr", m.status === "critical" ? "smartCrit" : m.status === "warning" ? "smartWarn" : "");
      const status = el("td", "");
      status.appendChild(hwBadge(m.status || "ok", m.status === "critical" ? "Kritis" : m.status === "warning" ? "Waspada" : "Normal"));
      if (ata) {
        row.append(
          el("td", "mutedText", m.id || ""),
          el("td", "procName", m.name),
          el("td", "num", m.normalized == null ? "—" : String(m.normalized)),
          el("td", "num", m.worst == null ? "—" : String(m.worst)),
          el("td", "num", m.threshold == null ? "—" : String(m.threshold)),
          el("td", "num", m.value),
          status,
        );
      } else {
        row.append(el("td", "procName", m.name), el("td", "num", m.value), status);
      }
      tbody.appendChild(row);
    }
    table.append(thead, tbody);
    td.appendChild(table);
    tr.appendChild(td);
    return tr;
  }

  function renderDiagnosis(diag) {
    if (!diag) return;
    const findings = (diag.findings || []).slice();
    if (state.uiRTT != null && state.uiRTT >= 100) {
      findings.unshift({
        severity: "warning",
        title: `Latensi browser→server tinggi (${fmtMs(state.uiRTT)})`,
        detail: "Dashboard ini dibuka dari mesin lain. Angka ini mengukur jalur jaringan dari browser ke server ditambah waktu respons server.",
        action: "Cek LAN/Wi-Fi/VPN/switch dari PC client ke server, lalu bandingkan dengan latensi gateway di kartu jaringan.",
      });
    }
    let level = diag.level || "ok";
    if (findings.some((f) => f.severity === "critical")) level = "critical";
    else if (findings.some((f) => f.severity === "warning")) level = "warning";

    let headline = diag.headline || "Diagnosis";
    if (diag.level === "ok" && state.uiRTT != null && state.uiRTT >= 100) {
      headline = `Jalur browser→server lambat (${fmtMs(state.uiRTT)})`;
    }
    $("diagHeadline").textContent = headline;
    $("diagnosisCard").dataset.level = level;
    const badge = $("diagLevel");
    badge.className = `badge ${level === "critical" ? "critical" : level === "warning" ? "warning" : "ok"}`;
    badge.textContent = level === "critical" ? "Kritis" : level === "warning" ? "Waspada" : "Normal";

    const list = $("findings");
    const frag = document.createDocumentFragment();
    for (const finding of findings) {
      const item = el("li", `finding ${finding.severity || "info"}`);
      const body = el("div", "findingBody");
      body.appendChild(el("strong", "", finding.title || ""));
      if (finding.detail) body.appendChild(el("p", "", finding.detail));
      if (finding.action) body.appendChild(el("p", "fAction", `→ ${finding.action}`));
      item.appendChild(body);
      frag.appendChild(item);
    }
    list.replaceChildren(frag);

    const client = $("diagClient");
    client.className = "";
    if (state.uiRTT == null) {
      client.textContent = "Latensi browser→server: —";
    } else {
      client.textContent = `Latensi browser→server: ${fmtMs(state.uiRTT)}`;
      if (state.uiRTT >= 100) client.classList.add("crit");
      else if (state.uiRTT >= 50) client.classList.add("warn");
    }
  }

  function renderNetLatency(net) {
    const parts = [];
    if (net && net.gateway) {
      const gateway = (net.probes || []).find((p) => p.kind === "gateway");
      parts.push(gateway
        ? `Gateway ${net.gateway} ${fmtMs(gateway.rtt_ms)}${gateway.loss_percent > 0 ? ` · loss ${nf1.format(gateway.loss_percent)}%` : ""}`
        : `Gateway ${net.gateway}`);
    }
    const tcp = ((net && net.probes) || []).filter((p) => p.kind === "tcp");
    if (tcp.length) {
      let worst = tcp[0];
      for (const probe of tcp) {
        if ((probe.rtt_ms || 0) > (worst.rtt_ms || 0)) worst = probe;
      }
      parts.push(`${worst.target} ${fmtMs(worst.rtt_ms)}${worst.loss_percent > 0 ? ` · loss ${nf1.format(worst.loss_percent)}%` : ""}`);
    }
    if (state.uiRTT != null) parts.push(`Browser ${fmtMs(state.uiRTT)}`);
    $("netLatency").textContent = parts.length ? parts.join(" · ") : "Latensi — (probe belum dikonfigurasi)";
  }

  function renderNetInterfaces(net) {
    const ifaces = (net && net.interfaces) || [];
    const chosen = ifaces.filter((iface) => iface.selected);
    if (!ifaces.length) {
      $("netIfaces").textContent = "Interface —";
      return;
    }
    const ordered = chosen.slice().sort((a, b) => {
      const totalA = (a.rx_bytes_total || 0) + (a.tx_bytes_total || 0);
      const totalB = (b.rx_bytes_total || 0) + (b.tx_bytes_total || 0);
      if (totalA !== totalB) return totalB - totalA;
      return a.name.localeCompare(b.name);
    });
    const active = ordered.filter((iface) =>
      (iface.rx_bytes_per_sec || 0) + (iface.tx_bytes_per_sec || 0) > 0);
    const shown = (active.length ? active : ordered).slice(0, 4);
    const parts = shown.map((iface) => `${iface.name} ↓${fmtRate(iface.rx_bytes_per_sec)} ↑${fmtRate(iface.tx_bytes_per_sec)}`);
    $("netIfaces").textContent = `${chosen.length}/${ifaces.length} NIC: ${parts.join(" · ")}${shown.length < chosen.length ? " …" : ""}`;
    $("netIfaces").title = ifaces
      .slice()
      .sort((a, b) => ((b.rx_bytes_total || 0) + (b.tx_bytes_total || 0)) - ((a.rx_bytes_total || 0) + (a.tx_bytes_total || 0)))
      .map((iface) => `${iface.name}${iface.selected ? " ✓" : ""} ↓${fmtRate(iface.rx_bytes_per_sec)} ↑${fmtRate(iface.tx_bytes_per_sec)}`)
      .join(" · ");
  }

  function renderVolumes(volumes) {
    const list = volumes || [];
    $("volumeTag").textContent = `${list.length} volume`;
    const body = $("volumesBody");
    if (!list.length) {
      const tr = document.createElement("tr");
      const td = el("td", "empty", "Volume tidak terbaca");
      td.colSpan = 6;
      tr.appendChild(td);
      body.replaceChildren(tr);
      return;
    }
    const frag = document.createDocumentFragment();
    for (const volume of list) {
      const tr = document.createElement("tr");
      const name = el("td", "");
      name.appendChild(el("span", "volName", volume.mount));
      // Show the stable disk id (or the device path) instead of sda1/sdb2.
        const backing = volume.disk_id || volume.device || "";
        const meta = el("span", "volMeta", `${volume.fstype || "—"}${backing ? ` · ${backing}` : ""}`);
        meta.title = [volume.device, volume.disk_id].filter(Boolean).join(" → ");
        name.appendChild(meta);
      const usage = el("td", "");
      usage.appendChild(el("span", "volLine", `${nf1.format(volume.percent || 0)}%`));
      usage.appendChild(el("span", "volMeta", `${fmtBytes(volume.free)} bebas`));
      const read = el("td", "num", volume.has_io ? fmtRate(volume.read_bytes_per_sec) : "—");
      const write = el("td", "num", volume.has_io ? fmtRate(volume.write_bytes_per_sec) : "—");
      const latency = el("td", "num");
      if (volume.has_io) {
        const worst = Math.max(volume.read_latency_ms || 0, volume.write_latency_ms || 0);
        latency.textContent = `${fmtMs(volume.read_latency_ms)} / ${fmtMs(volume.write_latency_ms)}`;
        latency.classList.add(latencyClass(worst));
        latency.title = `Baca ${fmtMs(volume.read_latency_ms)}, tulis ${fmtMs(volume.write_latency_ms)}`;
      } else {
        latency.textContent = "—";
      }
      const queue = el("td", "num", volume.has_io && volume.queue_supported ? nf1.format(volume.queue || 0) : "—");
      tr.append(name, usage, read, write, latency, queue);
      frag.appendChild(tr);
    }
    body.replaceChildren(frag);
  }

  function renderApps(apps) {
    const list = apps || [];
    $("appsTag").textContent = `${list.length} pola`;
    const body = $("appsBody");
    if (!list.length) {
      const tr = document.createElement("tr");
      const td = el("td", "empty", "Tambahkan pola aplikasi di Pengaturan");
      td.colSpan = 5;
      tr.appendChild(td);
      body.replaceChildren(tr);
      return;
    }
    const frag = document.createDocumentFragment();
    for (const app of list) {
      const tr = document.createElement("tr");
      const name = el("td", "procName", app.pattern);
      name.title = app.top_process ? `Proses utama: ${app.top_process}` : "Tidak ada proses yang cocok";
      const ioRate = (app.disk_read_bytes_per_sec || 0) + (app.disk_write_bytes_per_sec || 0);
      tr.append(
        name,
        el("td", "num", app.processes ? String(app.processes) : "0"),
        el("td", "num", app.processes ? `${fmtPct(app.cpu_percent)}%` : "—"),
        el("td", "num", app.processes ? fmtBytes(app.memory_rss) : "—"),
        el("td", "num", app.processes && app.top_process ? fmtRate(ioRate) : "—"),
      );
      if (!app.processes) tr.classList.add("dimRow");
      frag.appendChild(tr);
    }
    body.replaceChildren(frag);
  }

  function renderProbes(net) {
    const probes = (net && net.probes) || [];
    const tcpCount = probes.filter((p) => p.kind === "tcp").length;
    $("probesTag").textContent = tcpCount ? `gateway + ${tcpCount} TCP` : "gateway saja";
    const body = $("probesBody");
    if (!probes.length) {
      const tr = document.createElement("tr");
      const td = el("td", "empty", "Belum ada target. Tambahkan host:port SQL/Chromeleon di Pengaturan.");
      td.colSpan = 5;
      tr.appendChild(td);
      body.replaceChildren(tr);
      return;
    }
    const frag = document.createDocumentFragment();
    for (const probe of probes) {
      const tr = document.createElement("tr");
      const status = probe.ok
        ? el("span", "badge ended", "OK")
        : probe.reachable
          ? el("span", "badge warning", "port tertutup")
          : el("span", "badge warning", probe.error || "gagal");
      const target = el("td", "portAddr", probe.target);
      target.title = probe.target;
      const statusCell = el("td", "");
      statusCell.appendChild(status);
      tr.append(
        target,
        el("td", "mutedText", probe.kind === "gateway" ? "ICMP" : "TCP"),
        el("td", `num ${latencyClass(probe.rtt_ms || 0)}`, fmtMs(probe.rtt_ms)),
        (() => {
          const td = el("td", "num", `${nf1.format(probe.loss_percent || 0)}%`);
          td.title = `${probe.attempts || 0} percobaan`;
          return td;
        })(),
        statusCell,
      );
      frag.appendChild(tr);
    }
    body.replaceChildren(frag);
  }

  function renderPorts(ports) {
    const items = (ports && ports.items) || [];
    const count = (ports && ports.count) || 0;
    $("portsValue").textContent = nf0.format(count);
    $("portsTag").textContent = `${items.length} baris`;
    let tcp = 0;
    let udp = 0;
    for (const p of items) {
      if (p.proto === "tcp") tcp++;
      else if (p.proto === "udp") udp++;
    }
    const shown = items.slice(0, 80);
    $("portsSub").textContent = `${tcp} TCP · ${udp} UDP` +
      (items.length > shown.length ? ` · menampilkan ${shown.length} dari ${items.length}` : "");

    const body = $("portsBody");
    if (!shown.length) {
      const tr = document.createElement("tr");
      const td = el("td", "empty", "Tidak ada port terbuka");
      td.colSpan = 4;
      tr.appendChild(td);
      body.replaceChildren(tr);
      return;
    }
    const frag = document.createDocumentFragment();
    for (const p of shown) {
      const tr = document.createElement("tr");
      tr.append(
        el("td", "mutedText", (p.proto || "").toUpperCase()),
        el("td", "num", String(p.port)),
        el("td", "portAddr", p.address || "*"),
        el("td", "procName", p.process || `pid ${p.pid}`),
      );
      tr.lastElementChild.title = p.process || "";
      frag.appendChild(tr);
    }
    body.replaceChildren(frag);
  }

  function renderProcesses(summary, cores) {
    const items = (summary && summary.items) || [];
    $("procsTag").textContent = `${nf0.format((summary && summary.count) || 0)} proses`;
    const body = $("procsBody");
    if (!items.length) {
      const tr = document.createElement("tr");
      const td = el("td", "empty", "Menunggu data…");
      td.colSpan = 6;
      tr.appendChild(td);
      body.replaceChildren(tr);
      return;
    }
    const frag = document.createDocumentFragment();
    for (const p of items) {
      const tr = document.createElement("tr");
      const name = el("td", "procName", p.name || `pid ${p.pid}`);
      name.title = `${p.name || ""} (PID ${p.pid})`;
      const cpu = p.cpu_percent >= 100 * (cores || 1) * 0.98
        ? `${nf0.format(p.cpu_percent)}%`
        : `${fmtPct(p.cpu_percent)}%`;
      tr.append(
        name,
        el("td", "num", String(p.pid)),
        el("td", "num", cpu),
        el("td", "num", fmtBytes(p.memory_rss)),
        el("td", "num", p.disk_read_bytes_per_sec == null ? "—" : fmtRate(p.disk_read_bytes_per_sec)),
        el("td", "num", p.disk_write_bytes_per_sec == null ? "—" : fmtRate(p.disk_write_bytes_per_sec)),
      );
      if (p.disk_read_bytes_per_sec == null) {
        tr.lastElementChild.title = "I/O per proses tidak tersedia untuk proses ini";
      }
      frag.appendChild(tr);
    }
    body.replaceChildren(frag);
  }

  function renderActiveAlerts(alerts) {
    const bar = $("alertBar");
    bar.replaceChildren();
    if (!alerts.length) {
      bar.appendChild(el("span", "alertNone", "Semua metrik dalam batas normal"));
      return;
    }
    for (const a of alerts) {
      const chip = el("span", `alertChip ${a.severity || "warning"}${a.acked ? " acked" : ""}`);
      chip.appendChild(el("span", "dot"));
      const label = el("span", "alertMsg", a.message || a.kind || "Alert");
      label.title = a.message || "";
      chip.appendChild(label);
      if (!a.acked) {
        const ack = el("button", "ackBtn", "Tandai");
        ack.type = "button";
        ack.addEventListener("click", () => ackAlert(a.id, ack));
        chip.appendChild(ack);
      }
      bar.appendChild(chip);
    }
  }

  function renderWarnings(warnings) {
    const box = $("warnings");
    if (!warnings.length) {
      box.hidden = true;
      box.textContent = "";
      return;
    }
    box.hidden = false;
    box.textContent = `Sebagian metrik tidak tersedia: ${warnings.join(" · ")}`;
  }

  function maybeRefreshAlerts(alerts) {
    const sig = alerts
      .map((a) => `${a.id}:${a.ended_at || 0}:${a.acked ? 1 : 0}:${a.updated_at || 0}`)
      .sort()
      .join(",");
    if (sig !== state.activeSig) {
      state.activeSig = sig;
      refreshAlerts();
    }
  }

  function renderAlertHistory() {
    const body = $("alertsBody");
    const alerts = state.alerts || [];
    const active = alerts.filter((a) => !a.ended_at).length;
    $("alertTag").textContent = alerts.length
      ? `${active} aktif · ${alerts.length} tercatat`
      : "Belum ada";
    if (!alerts.length) {
      const tr = document.createElement("tr");
      const td = el("td", "empty", "Belum ada alert. Semua metrik dalam batas normal.");
      td.colSpan = 4;
      tr.appendChild(td);
      body.replaceChildren(tr);
      return;
    }
    const frag = document.createDocumentFragment();
    for (const a of alerts.slice(0, 60)) {
      const tr = document.createElement("tr");
      const time = el("td", "mutedText", fmtClock(a.started_at));
      const level = el("td", "");
      const badge = el("span", `badge ${a.severity || "warning"}`, a.severity === "critical" ? "Kritis" : "Waspada");
      level.appendChild(badge);
      const message = el("td", "", a.message || "");
      message.title = a.message || "";
      const status = el("td", "");
      if (a.ended_at) {
        status.appendChild(el("span", "badge ended", a.acked ? "Dibaca" : "Selesai"));
      } else {
        status.appendChild(el("span", "badge active", a.acked ? "Dibaca" : "Aktif"));
        if (!a.acked) {
          const ack = el("button", "linkBtn", "Tandai");
          ack.type = "button";
          ack.addEventListener("click", () => ackAlert(a.id, ack));
          status.appendChild(ack);
        }
      }
      tr.append(time, level, message, status);
      frag.appendChild(tr);
    }
    body.replaceChildren(frag);
  }

  function renderFooter() {
    const view = state.settings;
    if (!view) return;
    $("footLeft").textContent = `DMetric ${releaseTag(view.version)}`;
    const chip = $("verChip");
    chip.textContent = releaseTag(view.version);
    chip.hidden = false;
    $("footRight").textContent = `${view.host}:${view.effective_port} · data ${view.data_dir}`;
  }

  // ---------- data ----------

  function pointFromSnapshot(snap) {
    let readLat = snap.disk.read_latency_ms || 0;
    let writeLat = snap.disk.write_latency_ms || 0;
    let queue = snap.disk.queue || 0;
    for (const volume of (snap.storage && snap.storage.volumes) || []) {
      if (!volume.has_io) continue;
      readLat = Math.max(readLat, volume.read_latency_ms || 0);
      writeLat = Math.max(writeLat, volume.write_latency_ms || 0);
      queue = Math.max(queue, volume.queue || 0);
    }
    return {
      ts: snap.ts,
      cpu: snap.cpu.percent,
      ram: snap.memory.percent,
      disk_read: snap.disk.read_bytes_per_sec,
      disk_write: snap.disk.write_bytes_per_sec,
      net_rx: snap.network.rx_bytes_per_sec,
      net_tx: snap.network.tx_bytes_per_sec,
      load1: snap.cpu.load1,
      ports: snap.ports.count,
      procs: snap.processes.count,
      disk_read_latency: readLat,
      disk_write_latency: writeLat,
      disk_queue: queue,
    };
  }

  function onSnapshot(snap) {
    state.lastMsg = Date.now();
    setLive(true);
    const p = pointFromSnapshot(snap);
    const points = state.points;
    if (points.length && p.ts <= points[points.length - 1].ts) {
      points[points.length - 1] = p;
    } else {
      points.push(p);
      if (points.length > 1200) points.splice(0, points.length - 1200);
    }
    render(snap);
  }

  async function fetchJSON(url, options) {
    const res = await fetch(url, options);
    let data = null;
    try {
      data = await res.json();
    } catch {
      data = null;
    }
    if (!res.ok) {
      throw new Error((data && data.error) || `Permintaan gagal (${res.status})`);
    }
    return data;
  }

  async function fetchSettings() {
    try {
      state.settings = await fetchJSON("/api/settings");
      renderFooter();
      $("settingsMeta").textContent =
        `v${state.settings.version} · ${state.settings.host}:${state.settings.effective_port} · ${state.settings.data_dir}`;
      $("portNote").textContent = state.settings.port_locked
        ? "Ditetapkan oleh flag atau variabel lingkungan."
        : state.settings.effective_port !== state.settings.settings.port
          ? "Berlaku setelah restart."
          : "Aktif sekarang.";
      $("intervalNote").textContent = state.settings.interval_locked
        ? "Ditetapkan oleh flag atau variabel lingkungan."
        : state.settings.effective_interval !== state.settings.settings.interval_seconds
          ? "Berlaku setelah restart."
          : "Aktif sekarang.";
    } catch (err) {
      toast(err.message, true);
    }
  }

  async function fetchCandidates() {
    try {
      state.candidates = await fetchJSON("/api/candidates");
    } catch {
      state.candidates = null;
    }
  }

  async function fetchHistory() {
    try {
      const data = await fetchJSON(`/api/history?minutes=${state.minutes}`);
      state.points = data.points || [];
      renderCharts();
    } catch (err) {
      toast(err.message, true);
    }
  }

  async function refreshAlerts() {
    try {
      const data = await fetchJSON("/api/alerts?limit=100");
      state.alerts = data.alerts || [];
      renderAlertHistory();
    } catch {
      // The live snapshot still shows active alerts; history can wait.
    }
  }

  async function ackAlert(id, button) {
    if (!id) return;
    if (button) button.disabled = true;
    try {
      await fetchJSON(`/api/alerts/${id}/ack`, { method: "POST" });
      toast("Alert ditandai sudah dibaca");
      state.activeSig = "";
      await refreshAlerts();
      try {
        const snap = await fetchJSON("/api/snapshot");
        render(snap);
      } catch {
        // The next SSE sample will update the chips.
      }
    } catch (err) {
      if (button) button.disabled = false;
      toast(err.message, true);
    }
  }

  // ---------- live ----------

  function setLive(ok) {
    const badge = $("liveBadge");
    badge.classList.toggle("offline", !ok);
    if (ok) {
      $("liveText").textContent = state.lastMsg
        ? `Live · ${fmtClock(state.lastMsg)}`
        : "Live";
    } else {
      $("liveText").textContent = "Menyambung ulang…";
    }
  }

  function connect() {
    const es = new EventSource("/api/stream");
    es.onopen = () => {
      state.lastMsg = Date.now();
      setLive(true);
    };
    es.onmessage = (event) => {
      try {
        onSnapshot(JSON.parse(event.data));
      } catch {
        // Ignore a malformed frame; the next sample arrives shortly.
      }
    };
    es.onerror = () => setLive(false);
  }

  async function measureUI() {
    const started = performance.now();
    try {
      await fetchJSON("/api/health", { cache: "no-store" });
      state.uiRTT = performance.now() - started;
    } catch {
      state.uiRTT = null;
    }
    if (state.snap) {
      renderNetLatency(state.snap.network);
      renderDiagnosis(state.snap.diagnosis);
    }
  }

  function captureIncident() {
    if (!state.snap) {
      toast("Belum ada data untuk dilaporkan", true);
      return;
    }
    const diag = state.snap.diagnosis || {};
    const summary = [
      `DMetric insiden — ${new Date().toLocaleString("id-ID")}`,
      `Host: ${state.snap.host.hostname} (${state.snap.host.os} ${state.snap.host.arch})`,
      `Diagnosis: ${diag.headline || "—"}`,
      ...(diag.findings || []).map((f) => `- [${f.severity}] ${f.title}: ${f.detail} Aksi: ${f.action}`),
      `CPU ${fmtPct(state.snap.cpu.percent)}%, RAM ${fmtPct(state.snap.memory.percent)}%, disk R/W ${fmtRate(state.snap.disk.read_bytes_per_sec)} / ${fmtRate(state.snap.disk.write_bytes_per_sec)}, latensi disk ${fmtMs(state.snap.disk.read_latency_ms)} / ${fmtMs(state.snap.disk.write_latency_ms)}, antrean ${nf1.format(state.snap.disk.queue || 0)}`,
      `Probe: ${((state.snap.network && state.snap.network.probes) || []).map((p) => `${p.target} ${fmtMs(p.rtt_ms)} loss ${nf1.format(p.loss_percent || 0)}%`).join("; ") || "belum ada"}`,
      `Latensi browser→server: ${fmtMs(state.uiRTT)}`,
    ].join("\n");
    const report = {
      generated_at: new Date().toISOString(),
      summary,
      ui_rtt_ms: state.uiRTT,
      snapshot: state.snap,
      history: state.points,
      alerts: state.alerts,
      settings: state.settings,
    };
    const blob = new Blob([JSON.stringify(report, null, 2)], { type: "application/json" });
    const url = URL.createObjectURL(blob);
    const link = document.createElement("a");
    const stamp = new Date().toISOString().replace(/[:.]/g, "-").slice(0, 19);
    link.href = url;
    link.download = `dmetric-insiden-${stamp}.json`;
    document.body.appendChild(link);
    link.click();
    link.remove();
    setTimeout(() => URL.revokeObjectURL(url), 2000);
    toast("Laporan insiden diunduh (JSON)");
  }

  // ---------- update ----------

  function renderUpdate() {
    const info = state.updateData || {};
    const current = info.current_version || (state.settings && state.settings.version) || "—";
    const latest = info.latest_version || "—";
    $("upCurrent").textContent = releaseTag(current);
    $("upLatest").textContent = info.tag || (latest === "—" ? latest : releaseTag(latest));
    $("updateDot").hidden = !info.update_available;

    const status = $("upStatus");
    status.className = "updateStatus";
    let message = "";
    if (info.error) {
      message = info.error;
      status.classList.add("error");
    } else if (info.update_available) {
      message = `Update tersedia${info.tag ? `: ${info.tag}` : ""}.`;
      status.classList.add("ok");
    } else {
      message = "Aplikasi sudah memakai versi terbaru.";
    }
    if (info.update_available && info.install_allowed === false) {
      message += " Update harus dipicu dari server (localhost).";
    }
    status.textContent = message;

    const asset = [];
    if (info.asset_name) asset.push(info.asset_name);
    if (info.asset_size) asset.push(fmtBytes(info.asset_size));
    if (info.checksum) asset.push("SHA256 tersedia");
    $("upAsset").textContent = asset.join(" · ");

    $("upNotes").textContent = info.notes || "";
    const release = $("upRelease");
    release.hidden = !info.release_url;
    if (info.release_url) release.href = info.release_url;
    $("upMeta").textContent = info.published_at ? `Rilis ${info.published_at}` : "";

    const button = $("upInstall");
    button.disabled = state.updateBusy || !info.update_available || info.install_allowed === false;
    button.textContent = state.updateBusy ? "Memproses…" : "Update & restart";
  }

  async function checkUpdate(force, silent) {
    const status = $("upStatus");
    if (!silent) {
      status.className = "updateStatus";
      status.textContent = "Memeriksa rilis…";
    }
    try {
      state.updateData = await fetchJSON(`/api/update${force ? "?refresh=1" : ""}`);
    } catch (err) {
      state.updateData = {
        current_version: (state.settings && state.settings.version) || "—",
        error: err.message,
        install_allowed: false,
      };
    }
    renderUpdate();
    if (!silent && state.updateData && state.updateData.error) {
      toast(state.updateData.error, true);
    }
  }

  async function openUpdate() {
    renderUpdate();
    $("updateDialog").showModal();
    if (!state.updateData) await checkUpdate(false, false);
  }

  async function installUpdate() {
    const info = state.updateData;
    if (!info || !info.update_available || info.install_allowed === false || state.updateBusy) return;
    state.updateBusy = true;
    renderUpdate();
    const status = $("upStatus");
    status.className = "updateStatus";
    status.textContent = "Mengunduh, memverifikasi, dan memasang update…";
    try {
      const result = await fetchJSON("/api/update/install", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ version: info.latest_version }),
      });
      status.textContent = result.message || "Update terpasang. Menunggu restart…";
      const target = info.latest_version;
      for (let attempt = 0; attempt < 90; attempt++) {
        await new Promise((resolve) => setTimeout(resolve, 1000));
        try {
          const health = await fetchJSON("/api/health", { cache: "no-store" });
          if (health.version === target) {
            location.reload();
            return;
          }
        } catch {
          // The server is restarting.
        }
      }
      status.className = "updateStatus error";
      status.textContent = "Server belum kembali dengan versi baru; muat ulang halaman beberapa saat lagi.";
    } catch (err) {
      status.className = "updateStatus error";
      status.textContent = err.message;
    } finally {
      state.updateBusy = false;
      renderUpdate();
    }
  }

  // ---------- settings ----------

  const THRESHOLD_FIELDS = [
    "cpu_percent",
    "memory_percent",
    "storage_percent",
    "disk_read_mbps",
    "disk_write_mbps",
    "net_rx_mbps",
    "net_tx_mbps",
    "load_per_core",
    "process_cpu_percent",
    "process_memory_mb",
    "sustain_seconds",
    "recovery_seconds",
  ];

  function fillSettings() {
    const view = state.settings;
    const form = $("settingsForm");
    if (!view) return;
    const s = view.settings;
    form.elements.namedItem("port").value = s.port;
    form.elements.namedItem("interval_seconds").value = s.interval_seconds;
    form.elements.namedItem("watch_apps").value = (s.watch_apps || []).join(", ");
    form.elements.namedItem("network_probes").value = (s.network_probes || []).join(", ");
    renderComboChips("watch_apps");
    renderComboChips("network_probes");
    syncPreset();
    $("probeNote").textContent = "Berlaku langsung. Pilih port lokal atau ketik host:port SQL/PC client.";
    fillCandidates(s);
    for (const field of THRESHOLD_FIELDS) {
      form.elements.namedItem(field).value = s.thresholds[field];
    }
    $("portNote").textContent = view.port_locked
      ? "Ditetapkan oleh flag atau variabel lingkungan."
      : (view.effective_port !== s.port ? "Berlaku setelah restart." : "Aktif sekarang.");
    $("intervalNote").textContent = view.interval_locked
      ? "Ditetapkan oleh flag atau variabel lingkungan."
      : (view.effective_interval !== s.interval_seconds ? "Berlaku setelah restart." : "Aktif sekarang.");
  }

  function fillCandidates(settings) {
    const candidates = state.candidates || { processes: [], targets: [], interfaces: [], gateway: "" };
    const targets = candidates.targets || [];
    $("probeHint").textContent = candidates.gateway
      ? `Gateway ${candidates.gateway} diuji otomatis via ICMP · ${targets.length} port lokal tersedia.`
      : "Gateway tidak terdeteksi; probe TCP tetap bisa ditambahkan.";

    const list = $("ifaceList");
    const ifaceStats = candidates.interfaces || [];
    const chosen = new Set((settings.network_interfaces || []).map((name) => name.toLowerCase()));
    const pseudo = ["awdl", "llw", "anpi", "ap1", "gif", "stf", "nan", "bridge", "utun", "veth", "docker", "br-", "virbr"];
    let visible = ifaceStats.filter((iface) => {
      const name = iface.name.toLowerCase();
      if (chosen.has(name)) return true;
      if ((iface.rx_bytes_total || 0) + (iface.tx_bytes_total || 0) > 0) return true;
      return !pseudo.some((prefix) => name.startsWith(prefix));
    });
    if (!visible.length) visible = ifaceStats;
    const known = new Set(visible.map((iface) => iface.name.toLowerCase()));
    for (const name of settings.network_interfaces || []) {
      if (!known.has(name.toLowerCase())) {
        visible.push({ name, rx_bytes_total: 0, tx_bytes_total: 0, rx_bytes_per_sec: 0, tx_bytes_per_sec: 0 });
        known.add(name.toLowerCase());
      }
    }
    if (!visible.length) {
      list.replaceChildren(el("span", "sub", "Interface belum terdeteksi."));
      return;
    }
    const frag = document.createDocumentFragment();
    for (const iface of visible) {
      const label = el("label", "ifaceOption");
      const input = document.createElement("input");
      input.type = "checkbox";
      input.name = "iface";
      input.value = iface.name;
      input.checked = chosen.has(iface.name.toLowerCase());
      label.appendChild(input);
      label.appendChild(el("span", "", iface.name));
      label.appendChild(el("span", "ifaceMeta", `↓${fmtRate(iface.rx_bytes_per_sec)} ↑${fmtRate(iface.tx_bytes_per_sec)}`));
      frag.appendChild(label);
    }
    list.replaceChildren(frag);
  }

  async function openSettings() {
    if (!state.settings) {
      await fetchSettings();
    }
    await fetchCandidates();
    if (!state.settings) {
      toast("Pengaturan belum tersedia", true);
      return;
    }
    fillSettings();
    hideComboMenus();
    $("settingsDialog").showModal();
  }

  async function saveSettings(event) {
    event.preventDefault();
    if (state.settingsBusy) return;
    const form = $("settingsForm");
    if (!form.reportValidity()) return;
    const num = (name) => Number(form.elements.namedItem(name).value);
    const thresholds = {};
    for (const field of THRESHOLD_FIELDS) thresholds[field] = num(field);
    if (splitList(form.elements.namedItem("watch_apps").value).length > 8) {
      toast("Aplikasi fokus maksimal 8 pola", true);
      return;
    }
    if (splitList(form.elements.namedItem("network_probes").value).length > 8) {
      toast("Probe jaringan maksimal 8 target", true);
      return;
    }
    const body = {
      port: Math.round(num("port")),
      interval_seconds: Math.round(num("interval_seconds")),
      thresholds,
      watch_apps: splitList(form.elements.namedItem("watch_apps").value),
      network_probes: splitList(form.elements.namedItem("network_probes").value),
      network_interfaces: [...form.querySelectorAll('input[name="iface"]:checked')].map((input) => input.value),
    };
    state.settingsBusy = true;
    const submit = form.querySelector('button[type="submit"]');
    if (submit) submit.disabled = true;
    try {
      const view = await fetchJSON("/api/settings", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      state.settings = view;
      renderFooter();
      fillSettings();
      $("settingsDialog").close();
      toast(view.restart_required
        ? "Tersimpan. Port/interval berlaku setelah restart; aplikasi, probe, dan interface langsung aktif."
        : "Pengaturan tersimpan dan langsung aktif.");
    } catch (err) {
      toast(err.message, true);
    } finally {
      state.settingsBusy = false;
      if (submit) submit.disabled = false;
    }
  }

  // ---------- toast ----------

  let toastTimer = 0;
  function toast(message, isError) {
    const node = $("toast");
    node.textContent = message;
    node.classList.toggle("error", Boolean(isError));
    node.classList.add("show");
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => node.classList.remove("show"), 3600);
  }

  // ---------- wiring ----------

  function wire() {
    $("settingsBtn").addEventListener("click", openSettings);
    $("captureBtn").addEventListener("click", captureIncident);
    $("updateBtn").addEventListener("click", openUpdate);
    $("closeUpdate").addEventListener("click", () => $("updateDialog").close());
    $("upCheck").addEventListener("click", () => checkUpdate(true, false));
    $("upInstall").addEventListener("click", installUpdate);
    $("appPreset").addEventListener("change", () => {
      setList("watch_apps", splitList($("appPreset").value));
    });
    wireCombo("watchSearch", "watchMenu", renderWatchMenu);
    wireCombo("probeSearch", "probeMenu", renderProbeMenu);
    wireMenuNav("watchMenu", "watchSearch");
    wireMenuNav("probeMenu", "probeSearch");
    for (const id of ["settingsDialog", "updateDialog"]) {
      // Clicking the backdrop (the dialog element itself) closes it.
      $(id).addEventListener("click", (event) => {
        if (event.target === $(id)) $(id).close();
      });
    }
    document.addEventListener("click", (event) => {
      if (!event.target.closest(".combo")) hideComboMenus();
    });
    $("closeSettings").addEventListener("click", () => $("settingsDialog").close());
    $("cancelSettings").addEventListener("click", () => $("settingsDialog").close());
    $("settingsForm").addEventListener("submit", saveSettings);

    document.querySelectorAll(".rangeBtn").forEach((button) => {
      button.addEventListener("click", async () => {
        document.querySelectorAll(".rangeBtn").forEach((b) => {
          b.classList.remove("active");
          b.setAttribute("aria-pressed", "false");
        });
        button.classList.add("active");
        button.setAttribute("aria-pressed", "true");
        const minutes = Number(button.dataset.minutes) || 15;
        if (minutes === state.minutes) return;
        state.minutes = minutes;
        await fetchHistory();
      });
    });

    setInterval(measureUI, 6000);

    setInterval(() => {
      if (Date.now() - state.lastMsg > 7000) {
        fetchJSON("/api/snapshot")
          .then(onSnapshot)
          .catch(() => setLive(false));
      }
    }, 6000);
  }

  async function init() {
    wire();
    await Promise.all([fetchSettings(), fetchHistory(), refreshAlerts()]);
    try {
      const snap = await fetchJSON("/api/snapshot");
      onSnapshot(snap);
    } catch (err) {
      toast(err.message, true);
    }
    await measureUI();
    connect();
    checkUpdate(false, true);
    setInterval(() => checkUpdate(false, true), 6 * 60 * 60 * 1000);
  }

  init();
})();
