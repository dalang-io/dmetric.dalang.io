(() => {
  "use strict";

  const $ = (id) => document.getElementById(id);
  const KIND_LABEL = { added: "Baru", changed: "Perubahan", fixed: "Perbaikan", security: "Keamanan" };
  const MONTHS = ["Jan", "Feb", "Mar", "Apr", "Mei", "Jun", "Jul", "Agu", "Sep", "Okt", "Nov", "Des"];

  function el(tag, className, text) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined && text !== null) node.textContent = text;
    return node;
  }

  // Release tags are vYYYY.M.P; the running version is reported without "v".
  const tagOf = (version) => `v${String(version || "").replace(/^v/i, "")}`;

  function fmtDate(value) {
    const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(value || "");
    if (!m) return "";
    return `${Number(m[3])} ${MONTHS[Number(m[2]) - 1]} ${m[1]}`;
  }

  async function getJSON(url) {
    const res = await fetch(url, { cache: "no-store" });
    if (!res.ok) throw new Error(`${url}: ${res.status}`);
    return res.json();
  }

  function renderRelease(rel, running) {
    const card = el("article", "card releaseCard");
    card.id = tagOf(rel.version);
    if (running) card.classList.add("current");

    const head = el("div", "releaseHead");
    head.appendChild(el("h2", "releaseTag", tagOf(rel.version)));
    if (running) head.appendChild(el("span", "badge active", "Terpasang"));
    if (rel.released === false) head.appendChild(el("span", "badge warning", "Belum dirilis"));
    const date = fmtDate(rel.date);
    if (date) head.appendChild(el("span", "mutedText releaseDate", date));
    card.appendChild(head);
    if (rel.title) card.appendChild(el("p", "releaseTitle", rel.title));

    for (const group of rel.groups || []) {
      if (!group.items || !group.items.length) continue;
      const block = el("section", `notesGroup ${group.kind || ""}`);
      block.appendChild(el("h3", "", group.title || KIND_LABEL[group.kind] || group.kind));
      const list = el("ul");
      for (const item of group.items) list.appendChild(el("li", "", item));
      block.appendChild(list);
      card.appendChild(block);
    }
    return card;
  }

  async function load() {
    let version = "";
    try {
      version = (await getJSON("/api/health")).version || "";
    } catch {
      // The page still lists notes without a running version.
    }
    const tag = version ? tagOf(version) : "";
    $("nowTag").textContent = tag || "—";
    $("footTag").textContent = tag;
    $("verLine").textContent = tag ? `Anda memakai ${tag}` : "Versi tidak diketahui";

    try {
      const data = await getJSON("/releases.json");
      const releases = data.releases || [];
      const frag = document.createDocumentFragment();
      for (const rel of releases) {
        frag.appendChild(renderRelease(rel, tagOf(rel.version) === tag));
      }
      $("releaseList").replaceChildren(frag);
      const known = releases.some((rel) => tagOf(rel.version) === tag);
      $("nowSub").textContent = known
        ? ""
        : "Versi ini tidak tercantum di catatan rilis bawaan; lihat rilis di GitHub.";
    } catch (err) {
      $("releaseError").hidden = false;
      $("releaseError").textContent = `Catatan rilis tidak dapat dimuat (${err.message}).`;
    }

    // Latest published release (needs internet on the server; optional).
    try {
      const info = await getJSON("/api/update");
      if (info && info.tag) {
        $("updateBox").hidden = false;
        $("ghTag").textContent = info.tag;
        const parts = [];
        if (info.update_available) {
          const badge = $("nowBadge");
          badge.hidden = false;
          badge.className = "badge warning";
          badge.textContent = `Update tersedia: ${info.tag}`;
          parts.push("Versi yang lebih baru tersedia.");
        } else {
          const badge = $("nowBadge");
          badge.hidden = false;
          badge.className = "badge ok";
          badge.textContent = "Terbaru";
        }
        if (info.published_at) parts.push(`Rilis ${info.published_at}`);
        $("ghMeta").textContent = parts.join(" · ");
        $("ghNotes").textContent = info.notes || "";
        if (info.release_url) {
          $("ghLink").hidden = false;
          $("ghLink").href = info.release_url;
        }
      }
    } catch {
      // Offline server: bundled notes above are enough.
    }

    if (location.hash) {
      const target = document.getElementById(decodeURIComponent(location.hash.slice(1)));
      if (target) target.scrollIntoView();
    }
  }

  load();
})();
