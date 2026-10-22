var ExternalArchive = {
  label: function (english, chinese) {
    return locale === "zh" ? chinese : english;
  },
  facet: function (record, name) {
    return ((record.facets || {})[name] || [])[0] || "";
  },
  isExternal: function (record) {
    return record.source_kind === "external-local-history" ||
      record.source_format === "external-native-event" ||
      this.facet(record, "source.kind") === "external-local-history" ||
      (record.thread_sources || []).indexOf("external-local-history") >= 0;
  },
  badge: function (record) {
    if (!this.isExternal(record)) return "";
    return '<span class="kind-badge external-import">' +
      esc(this.label("External import", "外部导入")) + "</span>";
  },
  provenance: function (record) {
    var tool = record.source_tool || this.facet(record, "source.tool"),
      host = record.source_host || this.facet(record, "source.host"),
      account = record.source_account || this.facet(record, "source.account");
    return [tool, [account, host].filter(Boolean).join("@")].filter(Boolean).join(" · ");
  },
  status: function (record) {
    return [record.outcome,
      this.isExternal(record) ? "" : record.status_code,
      this.recordedTime(record.started_at)].filter(Boolean).join(" · ");
  },
  recordedTime: function (value) {
    return !value || String(value).startsWith("0001-") ?
      this.label("Time not recorded", "未记录时间") : localTime(value);
  },
  renderEntry: function (entry) {
    var identity = [entry.event_id, entry.parent_id ? "← " + entry.parent_id : "",
      entry.timestamp && this.recordedTime(entry.timestamp)].filter(Boolean).join(" · ");
    var label = [entry.label, entry.call_id].filter(Boolean).join(" · ");
    return '<section class="native-event"><div class="native-event-meta muted">' +
      esc(identity) + "</div>" +
      (entry.text ? renderMessage(entry.role || "event", entry.text, label) :
        '<div class="message-role">' + esc([entry.role || "event", label].filter(Boolean).join(" · ")) + "</div>") +
      "</section>";
  },
  nativeDiagnostics: function () {
    return '<details class="technical-meta"><summary>' +
      esc(this.label("Event details", "事件详情")) + '</summary><p class="muted">' +
      esc(this.label("This is a native session event. Original files are available in the session’s External import panel.",
        "这是原生会话事件。原始文件可在会话的外部导入面板下载。")) +
      '</p><button onclick="openRaw(\'request\')">' +
      esc(this.label("View event projection", "查看事件投影")) + "</button></details>";
  },
  nativeMetadata: function (view) {
    var nativeSession = (view.metadata || {}).external_session || {};
    var fields = [
      [this.label("Original session", "原始会话"), nativeSession.id || this.facet(view, "source.session_id")],
      [this.label("Parent session", "父会话"), nativeSession.parent_id],
      [this.label("Project", "项目"), nativeSession.project_path],
      [this.label("Session title", "会话标题"), nativeSession.title],
    ].filter(function (field) { return !!field[1]; });
    if (!fields.length) return "";
    return '<details class="technical-meta"><summary>' +
      esc(this.label("Original session details", "原始会话详情")) + "</summary><dl>" +
      fields.map(function (field) { return "<dt>" + esc(field[0]) + "</dt><dd>" + esc(field[1]) + "</dd>"; }).join("") +
      "</dl></details>";
  },
  loadSources: async function (sessionID) {
    var sources = await api("/external-sources?session_id=" + encodeURIComponent(sessionID));
    if (detailSessionID !== sessionID) return;
    var panel = document.getElementById("externalSources");
    panel.innerHTML = sources.length ? this.renderSources(sources, false) : "";
  },
  toggleSources: async function (button) {
    var panel = document.getElementById("importedSources");
    if (!panel.hidden) {
      panel.hidden = true;
      return;
    }
    button.disabled = true;
    try {
      var sources = await api("/external-sources");
      panel.innerHTML = this.renderSources(sources, true);
      panel.hidden = false;
    } catch (error) {
      alert(error.message || error);
    } finally {
      button.disabled = false;
    }
  },
  filterSources: function (input) {
    var query = input.value.toLocaleLowerCase();
    input.closest(".external-sources").querySelectorAll(".external-source").forEach(function (source) {
      source.hidden = !source.textContent.toLocaleLowerCase().includes(query);
    });
  },
  renderSources: function (sources, all) {
    var self = this;
    var provenance = sources.map(function (item) {
      return [item.source.tool, item.source.account + "@" + item.source.host].filter(Boolean).join(" · ");
    }).filter(function (value, index, all) { return all.indexOf(value) === index; });
    return '<details class="panel external-sources"' + (all ? " open" : "") + '><summary><b>' +
      esc(this.label("External import", "外部导入")) + " · " +
      esc(this.label("Original files", "原始文件")) + " (" + sources.length + ")</b></summary>" +
      '<p class="muted">' + esc(provenance.join(" · ")) + "</p>" +
      '<p class="muted">' + esc(this.label("Local session import. Each source revision has a SHA-256 checksum. T3 sources are history-table exports; authentication tables are excluded.",
        "本地会话导入。每个版本均有 SHA-256 校验和。T3 为历史表导出，不含身份验证表。")) + "</p>" +
      '<p class="muted">' + esc(this.label("Searchable session views depend on the source format. Original files remain available even when no session view exists.",
        "可搜索会话视图取决于原始格式。即使没有会话视图，原始文件仍可下载。")) + "</p>" +
      (all ? '<input class="source-search" aria-label="' + esc(this.label("Find imported source", "查找导入文件")) +
        '" placeholder="' + esc(this.label("Find path, tool or account…", "按路径、工具或账户查找…")) +
        '" oninput="ExternalArchive.filterSources(this)" />' : "") +
      sources.map(function (item) {
        return '<details class="external-source"><summary><b>' + esc(item.source.path) +
          '</b> · ' + esc(item.source.tool + " · " + fmt(item.bytes) + " bytes") +
          '</summary><dl><dt>' + esc(self.label("Tool / format", "工具 / 格式")) + "</dt><dd>" +
          esc(item.source.tool + " / " + item.source.format) + "</dd><dt>" +
          esc(self.label("Size", "大小")) + "</dt><dd>" + esc(fmt(item.bytes) + " bytes") +
          "</dd><dt>SHA-256</dt><dd><code>" + esc(item.sha256) + "</code></dd><dt>" +
          esc(self.label("Imported", "导入时间")) + "</dt><dd>" + esc(localTime(item.imported_at)) +
          "</dd><dt>" + esc(self.label("Source modified", "原始修改时间")) + "</dt><dd>" +
          esc(self.recordedTime(item.source.modified_at)) + '</dd></dl><button data-source-id="' +
          esc(item.id) + '" onclick="ExternalArchive.downloadSource(this)">' +
          esc(item.source.format === "t3-history-export-v1" ? self.label("Download history export", "下载历史导出") : self.label("Download original file", "下载原始文件")) + "</button></details>";
      }).join("") + "</details>";
  },
  downloadSource: async function (button) {
    button.disabled = true;
    try {
      var ticket = await api("/external-source?source_id=" + encodeURIComponent(button.dataset.sourceId));
      var link = document.createElement("a");
      link.href = new URL(ticket.url, session.base).href;
      document.body.appendChild(link);
      link.click();
      link.remove();
    } catch (error) {
      alert(error.message || error);
    } finally {
      button.disabled = false;
    }
  },
};
