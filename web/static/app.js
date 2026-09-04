// bbs 前端交互(不依赖任何构建工具)
// 通知未读数:进入页面拉一次,之后 30s 轮询(角标只对登录用户渲染)
(function () {
  var badge = document.getElementById("notif-count");
  if (!badge) return;
  function refresh() {
    fetch("/notifications/unread", { headers: { "Accept": "application/json" } })
      .then(function (res) { return res.json(); })
      .then(function (d) {
        var n = d.unread || 0;
        if (n > 0) {
          badge.textContent = n > 99 ? "99+" : String(n);
          badge.removeAttribute("hidden");
        } else {
          badge.setAttribute("hidden", "");
        }
      })
      .catch(function () {});
  }
  refresh();
  setInterval(refresh, 30000);
  window.__bbsNotifRefresh = refresh;
})();

// 编辑器「插入图片」:上传到 /uploads,在光标处插入 Markdown 图片链接
(function () {
  function insertAtCursor(ta, text) {
    if (ta && ta.selectionStart !== undefined) {
      ta.setRangeText(text, ta.selectionStart, ta.selectionEnd, "end");
    } else if (ta) {
      ta.value += text;
    }
    if (ta) {
      ta.focus();
      ta.dispatchEvent(new Event("input", { bubbles: true }));
    }
  }

  document.querySelectorAll("[data-upload]").forEach(function (btn) {
    var row = btn.closest(".upload-row");
    if (!row) return;
    var input = row.querySelector("input[type=file]");
    var status = row.querySelector(".upload-status");
    var form = btn.closest("form");

    btn.addEventListener("click", function () { input.click(); });
    input.addEventListener("change", function () {
      if (!input.files || !input.files.length) return;
      var fd = new FormData();
      fd.append("file", input.files[0]);
      var headers = { "Accept": "application/json" };
      if (form) {
        var csrf = form.querySelector('input[name="_csrf"]');
        if (csrf) headers["X-CSRF-Token"] = csrf.value;
      }
      status.textContent = "上传中…";
      btn.disabled = true;
      fetch("/uploads", { method: "POST", body: fd, headers: headers })
        .then(function (res) {
          if (!res.ok) {
            return res.text().then(function (t) { throw new Error(t || "上传失败"); });
          }
          return res.json();
        })
        .then(function (d) {
          var ta = document.querySelector(btn.getAttribute("data-upload"));
          if (d && d.url) {
            insertAtCursor(ta, "![](" + d.url + ")");
            status.textContent = "已插入图片链接";
          }
        })
        .catch(function (e) { status.textContent = e.message || "上传失败"; })
        .then(function () { btn.disabled = false; input.value = ""; });
    });
  });
})();

// 抽屉导航(移动端):菜单按钮开合,点 scrim/Esc/链接后关闭
(function () {
  var shell = document.getElementById("drawer-shell");
  var btn = document.getElementById("menu-btn");
  if (!shell || !btn) return;
  function setOpen(open) {
    shell.classList.toggle("open", open);
    document.body.classList.toggle("drawer-open", open);
    btn.setAttribute("aria-expanded", open ? "true" : "false");
  }
  btn.addEventListener("click", function () { setOpen(!shell.classList.contains("open")); });
  shell.addEventListener("click", function (e) {
    if (e.target.closest("[data-drawer-close]") || e.target.closest("a")) setOpen(false);
  });
  document.addEventListener("keydown", function (e) { if (e.key === "Escape") setOpen(false); });
})();

// 通知中心:点未读条目先标已读,再跳转主题;失败仍放行跳转
(function () {
  var csrfInput = null;
  function token() {
    if (!csrfInput) csrfInput = document.querySelector('input[name="_csrf"]');
    return csrfInput ? csrfInput.value : "";
  }
  document.addEventListener("click", function (e) {
    var row = e.target.closest("a.notif-row.unread");
    if (!row) return;
    var nid = row.getAttribute("data-nid");
    if (!nid) return;
    e.preventDefault();
    var href = row.getAttribute("href");
    fetch("/notifications/" + nid + "/read", {
      method: "POST",
      headers: { "Accept": "application/json", "X-CSRF-Token": token() }
    })
      .then(function () {
        if (window.__bbsNotifRefresh) window.__bbsNotifRefresh();
        window.location.href = href;
      })
      .catch(function () { window.location.href = href; });
  });
})();

// 编辑资料:内置头像面板(隐藏浏览器原生文件控件,选中即上传并预览)
(function () {
  var panel = document.querySelector("[data-avatar-panel]");
  if (!panel) return;
  var pickers = panel.querySelectorAll("[data-avatar-pick]");
  var preview = pickers[0];
  var fileInput = panel.querySelector("[data-avatar-input]");
  var resetBtn = panel.querySelector("[data-avatar-reset]");
  var statusEl = panel.querySelector("[data-avatar-status]");
  var urlInput = panel.querySelector("[data-avatar-url]");
  var originalEl = panel.querySelector("[data-avatar-original]");
  var originalHTML = originalEl ? originalEl.innerHTML : "";
  var form = panel.closest("form");
  var csrf = form ? form.querySelector('input[name="_csrf"]') : null;
  var maxBytes = 5 * 1024 * 1024;
  var allowed = ["image/jpeg", "image/png", "image/gif", "image/webp"];

  function setStatus(msg, cls) {
    statusEl.textContent = msg || "";
    statusEl.className = "upload-status" + (cls ? " " + cls : "");
  }

  function pickFile() { fileInput.click(); }

  Array.prototype.forEach.call(pickers, function (el) {
    el.addEventListener("click", pickFile);
  });

  resetBtn.addEventListener("click", function () {
    preview.innerHTML = originalHTML;
    urlInput.value = "";
    resetBtn.hidden = true;
    setStatus("");
  });

  fileInput.addEventListener("change", function () {
    var f = fileInput.files && fileInput.files[0];
    if (!f) return;
    if (allowed.indexOf(f.type) === -1) {
      setStatus("仅支持 jpg/png/gif/webp 图片", "err");
      fileInput.value = "";
      return;
    }
    if (f.size > maxBytes) {
      setStatus("图片不能超过 5MB", "err");
      fileInput.value = "";
      return;
    }
    var fd = new FormData();
    fd.append("file", f);
    var headers = { "Accept": "application/json" };
    if (csrf) headers["X-CSRF-Token"] = csrf.value;
    setStatus("上传中…");
    fetch("/uploads", { method: "POST", body: fd, headers: headers })
      .then(function (res) {
        if (!res.ok) {
          return res.text().then(function (t) { throw new Error(t || "上传失败"); });
        }
        return res.json();
      })
      .then(function (d) {
        if (!d || !d.url) throw new Error("上传失败");
        var img = document.createElement("img");
        img.src = d.url;
        img.alt = "新头像预览";
        preview.innerHTML = "";
        preview.appendChild(img);
        urlInput.value = d.url;
        resetBtn.hidden = false;
        setStatus("新头像已就绪,点「保存」生效", "ok");
      })
      .catch(function (e) {
        setStatus(e.message || "上传失败", "err");
      })
      .then(function () { fileInput.value = ""; });
  });
})();

// 编辑资料:称号标签(跟随身份/自定义/隐藏)联动
(function () {
  var editor = document.querySelector(".badge-editor");
  if (!editor) return;
  var radios = editor.querySelectorAll("input[name=badge_mode]");
  var customRow = editor.querySelector("[data-badge-custom]");
  var textInput = editor.querySelector("input[name=badge_text]");
  var preview = document.getElementById("badge-preview-label");

  function sync() {
    var custom = editor.querySelector('input[name="badge_mode"]:checked') &&
      editor.querySelector('input[name="badge_mode"]:checked').value === "custom";
    customRow.classList.toggle("disabled", !custom);
    textInput.disabled = !custom;
    var pv = editor.querySelector(".badge-preview");
    if (preview) {
      var t = textInput.value.trim();
      preview.textContent = t || (custom ? "自定义称号预览" : "");
      if (pv) pv.style.display = custom ? "inline-flex" : "none";
    }
  }
  radios.forEach(function (r) { r.addEventListener("change", sync); });
  if (textInput) textInput.addEventListener("input", sync);
  sync();
})();


// 编辑器:页面任意 .composer 通用绑定(发新帖/编辑主题/编辑回复/回帖框)
// 工具栏:回形针插图、@ 提及搜索、表情、常用图标、右下角回车发送
(function () {
  var EMOJI = ["😀","😄","😁","😆","😂","🤣","😊","😇","🙂","😉","😍","😘","😚","😜","🤪","😝",
    "🤔","🤨","😐","😑","😶","😏","😒","🙄","😬","😴","🤤","😪","😷","🤒","🤕","🤢",
    "🤮","🥵","🥶","🥴","😵","🤯","😱","😨","😰","😥","😢","😭","😤","😡","🤬","😈",
    "👿","💀","👻","🤖","💩","👍","👎","👌","✌️","🤞","🤟","🤘","👏","🙌","🙏","🤝",
    "💪","🤙","❤️","🧡","💛","💚","💙","💜","🖤","🤍","💔","💯","⭐","🔥","✨","🎉",
    "🎊","🎁","🎂","🍻","☕","🚀","📌","✅","❌"];
  var ICONS = [
    ["github.svg", "GitHub"], ["twitter.svg", "Twitter"], ["rss.svg", "RSS"],
    ["mail.svg", "邮件"], ["link.svg", "链接"], ["globe.svg", "网站"],
    ["star.svg", "收藏"], ["heart.svg", "喜欢"], ["tag.svg", "标签"],
    ["clock.svg", "时钟"], ["flame.svg", "热帖"], ["play.svg", "播放"],
    ["music.svg", "音乐"], ["camera.svg", "相机"], ["eye.svg", "浏览"],
    ["pin.svg", "置顶"], ["message-circle.svg", "评论"], ["search.svg", "搜索"],
    ["lock.svg", "锁定"], ["bell.svg", "通知"]
  ];

  function attachComposer(composer) {
    var form = composer.closest("form");
    var ta = composer.querySelector("textarea");
    if (!form || !ta) return;
    var bar = composer.querySelector(".composer-bar");
    var panelsBox = composer.querySelector(".composer-panels");
    if (!bar || !panelsBox) return;
    var statusEl = bar.querySelector(".upload-status");
    var fileInput = bar.querySelector('input[type="file"]');
    var csrfInput = form.querySelector('input[name="_csrf"]');
    var csrfToken = csrfInput ? csrfInput.value : "";

    function setStatus(msg) {
      if (!statusEl) return;
      statusEl.textContent = msg || "";
      statusEl.className = "upload-status" + (msg ? " show" : "");
      if (msg) {
        setTimeout(function () { statusEl.textContent = ""; statusEl.className = "upload-status"; }, 2600);
      }
    }
    function insertAtCursor(text) {
      var s = ta.selectionStart;
      if (s === undefined || s === null) s = ta.value.length;
      ta.setRangeText(text, s, ta.selectionEnd, "end");
      ta.focus();
      ta.dispatchEvent(new Event("input", { bubbles: true }));
    }
    function panel(name) { return panelsBox.querySelector('[data-panel="' + name + '"]'); }
    function closePanels() {
      panelsBox.querySelectorAll(".cp-panel").forEach(function (p) { p.hidden = true; });
      bar.querySelectorAll("[data-panel-toggle]").forEach(function (b) {
        b.classList.remove("on"); b.setAttribute("aria-expanded", "false");
      });
      var r = panelsBox.querySelector(".cp-at-results");
      if (r) r.textContent = "";
    }
    composer.__closePanels = closePanels;

    function setPanel(name, open) {
      panelsBox.querySelectorAll(".cp-panel").forEach(function (p) {
        p.hidden = p.getAttribute("data-panel") !== name || !open;
      });
      bar.querySelectorAll("[data-panel-toggle]").forEach(function (b) {
        var on = b.getAttribute("data-panel-toggle") === name && open;
        b.classList.toggle("on", on);
        b.setAttribute("aria-expanded", on ? "true" : "false");
      });
      if (open && name === "at") {
        var inp = panel("at").querySelector(".cp-at-input");
        if (inp) inp.focus();
      }
    }

    // ---- 面板开合 ----
    bar.addEventListener("click", function (e) {
      var btn = e.target.closest("[data-panel-toggle]");
      if (!btn) return;
      var name = btn.getAttribute("data-panel-toggle");
      var isOpen = !btn.classList.contains("on");
      setPanel(name, isOpen);
    });

    // ---- 表情面板 ----
    var emojiPanel = panel("emoji");
    if (emojiPanel) {
      EMOJI.forEach(function (ch) {
        var b = document.createElement("button");
        b.type = "button";
        b.className = "cp-emoji-item";
        b.textContent = ch;
        b.title = ch;
        b.addEventListener("click", function () { insertAtCursor(ch); });
        emojiPanel.appendChild(b);
      });
    }

    // ---- 图标库面板 ----
    var iconsPanel = panel("icons");
    if (iconsPanel) {
      ICONS.forEach(function (it) {
        var b = document.createElement("button");
        b.type = "button";
        b.className = "cp-icon-item";
        b.title = it[1];
        var img = document.createElement("img");
        img.src = "/static/icons/" + it[0];
        img.alt = it[1];
        b.appendChild(img);
        b.addEventListener("click", function () {
          insertAtCursor("![" + it[1] + "](" + img.getAttribute("src") + ")");
        });
        iconsPanel.appendChild(b);
      });
    }

    // ---- @ 提及:展开小搜索框,正则匹配用户名 ----
    var atPanel = panel("at");
    if (atPanel) {
      var atInput = atPanel.querySelector(".cp-at-input");
      var atResults = atPanel.querySelector(".cp-at-results");
      var timer = null;

      function renderUsers(list) {
        atResults.textContent = "";
        if (!list || !list.length) {
          var empty = document.createElement("div");
          empty.className = "cp-at-empty";
          empty.textContent = "没有匹配的用户";
          atResults.appendChild(empty);
          return;
        }
        list.forEach(function (u) {
          var row = document.createElement("button");
          row.type = "button";
          row.className = "cp-at-row";
          var av;
          if (u.AvatarPath) {
            av = document.createElement("img");
            av.src = u.AvatarPath;
            av.alt = "";
          } else {
            av = document.createElement("span");
            av.textContent = u.Name.charAt(0);
          }
          av.className = "cp-at-av";
          var nm = document.createElement("span");
          nm.textContent = u.Name;
          row.appendChild(av);
          row.appendChild(nm);
          row.addEventListener("click", function () {
            var start = ta.selectionStart || ta.value.length;
            var before = ta.value.slice(0, start);
            var m = /@[^\s@]*$/.exec(before);
            var from = m ? start - m[0].length : start;
            ta.setRangeText("@" + u.Name + " ", from, start, "end");
            setPanel("", false);
            ta.focus();
          });
          atResults.appendChild(row);
        });
      }
      function searchUsers() {
        var q = atInput.value.trim();
        if (!q) { atResults.textContent = ""; return; }
        fetch("/api/users?q=" + encodeURIComponent(q), { headers: { "Accept": "application/json" } })
          .then(function (res) {
            if (res.status === 401) throw new Error("请先登录");
            if (!res.ok) throw new Error("搜索失败");
            return res.json();
          })
          .then(renderUsers)
          .catch(function (e) {
            atResults.textContent = "";
            var empty = document.createElement("div");
            empty.className = "cp-at-empty";
            empty.textContent = e.message || "搜索失败";
            atResults.appendChild(empty);
          });
      }
      atInput.addEventListener("input", function () {
        clearTimeout(timer);
        timer = setTimeout(searchUsers, 200);
      });
      atInput.addEventListener("keydown", function (e) {
        if (e.key === "Enter") {
          e.preventDefault();
          var first = atResults.querySelector(".cp-at-row");
          if (first) first.click();
        }
      });
    }

    // ---- 插图:回形针打开内置文件选择,上传后插图片链接 ----
    var uploadBtn = bar.querySelector('[data-compose="upload"]');
    if (uploadBtn && fileInput) {
      uploadBtn.addEventListener("click", function () { fileInput.click(); });
      fileInput.addEventListener("change", function () {
        var f = fileInput.files && fileInput.files[0];
        if (!f) return;
        var fd = new FormData();
        fd.append("file", f);
        var headers = { "Accept": "application/json" };
        if (csrfToken) headers["X-CSRF-Token"] = csrfToken;
        setStatus("上传中…");
        fetch("/uploads", { method: "POST", body: fd, headers: headers })
          .then(function (res) {
            if (!res.ok) {
              return res.text().then(function (t) { throw new Error(t || "上传失败"); });
            }
            return res.json();
          })
          .then(function (d) {
            if (!d || !d.url) throw new Error("上传失败");
            insertAtCursor("![](" + d.url + ")");
            setStatus("已插入图片");
          })
          .catch(function (e) { setStatus(e.message || "上传失败"); })
          .then(function () { fileInput.value = ""; });
      });
    }

    // ---- Ctrl/Cmd + Enter 提交 ----
    ta.addEventListener("keydown", function (e) {
      if ((e.ctrlKey || e.metaKey) && e.key === "Enter") {
        e.preventDefault();
        if (form.requestSubmit) form.requestSubmit(); else form.submit();
      }
    });

    // ---- htmx 成功提交后清空输入并收起面板 ----
    document.body.addEventListener("htmx:afterRequest", function (e) {
      if (e.detail.elt !== form || !e.detail.successful) return;
      ta.value = "";
      closePanels();
    });
  }

  document.querySelectorAll(".composer").forEach(attachComposer);

  function closeAllPanels() {
    document.querySelectorAll(".composer").forEach(function (c) {
      if (c.__closePanels) c.__closePanels();
    });
  }
  document.addEventListener("click", function (e) {
    if (e.target.closest(".composer")) return;
    closeAllPanels();
  });
  document.addEventListener("keydown", function (e) { if (e.key === "Escape") closeAllPanels(); });
})();

// 主题页:回复框默认收起,右下角浮动「回复」键展开定位;引用按钮同样唤起回复框
(function () {
  var fab = document.getElementById("reply-fab");
  var form = document.getElementById("reply-form");
  if (!fab || !form) return;
  var ta = form.querySelector("textarea");
  var repliesBox = document.getElementById("replies");

  function isOpen() { return form.classList.contains("open"); }
  function closeComposer() {
    form.classList.remove("open");
  }
  function openComposer(scroll) {
    form.classList.add("open");
    if (scroll) {
      form.scrollIntoView({ behavior: "smooth", block: "start" });
      setTimeout(function () {
        try { ta.focus({ preventScroll: true }); } catch (err) { ta.focus(); }
      }, 180);
    }
  }

  fab.addEventListener("click", function () {
    if (isOpen()) {
      closeComposer();
      var op = document.querySelector(".op-card");
      if (op) op.scrollIntoView({ behavior: "smooth", block: "start" });
    } else {
      openComposer(true);
    }
  });

  // 引用:点引用图标,把「被引用楼自己写的正文」放进独立引用条(与正文输入分隔开)
  var qBox = form.querySelector("[data-composer-quote]");
  var qText = qBox ? qBox.querySelector("[data-cq-text]") : null;
  var qClear = qBox ? qBox.querySelector("[data-cq-clear]") : null;
  var quote = null; // { md, label }

  function clearQuote() {
    quote = null;
    if (qBox) qBox.hidden = true;
    if (qText) qText.textContent = "";
  }

  document.addEventListener("click", function (e) {
    var btn = e.target.closest("[data-quote]");
    if (!btn || !ta) return;
    e.preventDefault();
    var author = btn.getAttribute("data-q-author") || "";
    var floor = btn.getAttribute("data-q-floor") || "";
    var src = (btn.getAttribute("data-q-text") || "").trim();
    var md = "> @" + author;
    if (floor) md += " 于 #" + floor;
    md += ":\n";
    if (src) md += "> " + src + "\n";
    md += "\n";
    var label = (floor ? "#" + floor + " " : "") + "@" + author;
    if (src) label += " · " + src;
    quote = { md: md, label: label };
    if (qBox) qBox.hidden = false;
    if (qText) qText.textContent = label;
    openComposer(true);
  });

  if (qClear) qClear.addEventListener("click", function (e) {
    e.preventDefault();
    clearQuote();
    if (ta.focus) ta.focus({ preventScroll: true });
  });

  // 提交时把引用条拼进正文:引用独立存储于楼层开头,不与输入框内容混在一起
  document.body.addEventListener("htmx:configRequest", function (e) {
    if (e.detail.elt !== form || !quote) return;
    var cur = ta.value.trim();
    e.detail.parameters.content = quote.md + cur;
  });

  // 回复成功:清空收起回复框,滚到新插入的回复
  document.body.addEventListener("htmx:afterRequest", function (e) {
    if (e.detail.elt !== form || !e.detail.successful) return;
    clearQuote();
    closeComposer();
    var fresh = repliesBox && repliesBox.lastElementChild;
    if (fresh && fresh.classList && fresh.classList.contains("reply")) {
      fresh.scrollIntoView({ behavior: "smooth", block: "nearest" });
    }
  });
})();

// 顶栏头像:点击展开/收起账户菜单(常用 / 功能与设置 / 退出登录)
(function () {
  var wrap = document.querySelector("[data-acc-wrap]");
  if (!wrap) return;
  var trigger = wrap.querySelector("[data-acc-toggle]");
  var pop = wrap.querySelector("[data-acc-pop]");
  if (!trigger || !pop) return;

  function setOpen(open) {
    pop.hidden = !open;
    trigger.setAttribute("aria-expanded", open ? "true" : "false");
  }
  trigger.addEventListener("click", function (e) {
    e.preventDefault();
    e.stopPropagation();
    setOpen(pop.hidden);
  });
  document.addEventListener("click", function (e) {
    if (!e.target.closest("[data-acc-wrap]")) setOpen(false);
  });
  document.addEventListener("keydown", function (e) {
    if (e.key === "Escape") setOpen(false);
  });
})();

// 夜间模式:切换 <html data-theme>,持久化到本地
(function () {
  var btns = document.querySelectorAll("[data-theme-toggle]");
  if (!btns.length) return;
  var root = document.documentElement;
  function syncAll(dark) {
    btns.forEach(function (b) {
      b.setAttribute("aria-pressed", dark ? "true" : "false");
      var moon = b.querySelector(".th-moon");
      var sun = b.querySelector(".th-sun");
      var lb = b.querySelector(".th-label");
      // SVG 元素不反射 hidden 属性,需直接增删属性
      if (moon) moon.toggleAttribute("hidden", dark);
      if (sun) sun.toggleAttribute("hidden", !dark);
      if (lb) lb.textContent = dark ? "日间模式" : "夜间模式";
    });
  }
  btns.forEach(function (b) {
    b.addEventListener("click", function () {
      var dark = root.getAttribute("data-theme") === "dark";
      root.setAttribute("data-theme", dark ? "light" : "dark");
      syncAll(!dark);
      try { localStorage.setItem("bbs-theme", dark ? "light" : "dark"); } catch (e) {}
    });
  });
  syncAll(root.getAttribute("data-theme") === "dark");
})();

// 内置确认面板(替代浏览器原生 confirm / hx-confirm)
(function () {
  var modal = document.getElementById("bbs-modal");
  if (!modal) return;
  var textEl = document.getElementById("bbs-modal-text");
  var okBtn = document.getElementById("bbs-modal-ok");
  var lastFocus = null;
  var pending = null;

  function open(text, onYes) {
    lastFocus = document.activeElement;
    textEl.textContent = text;
    pending = onYes;
    modal.hidden = false;
    okBtn.focus();
  }
  function close() {
    modal.hidden = true;
    pending = null;
    if (lastFocus && lastFocus.focus) lastFocus.focus();
  }
  function resolve() {
    var fn = pending;
    close();
    if (fn) fn();
  }
  okBtn.addEventListener("click", resolve);
  Array.prototype.forEach.call(modal.querySelectorAll("[data-modal-cancel]"), function (el) {
    el.addEventListener("click", close);
  });
  document.addEventListener("keydown", function (e) {
    if (modal.hidden) return;
    if (e.key === "Escape") close();
    if (e.key === "Enter" && e.target === okBtn) resolve();
  });
  window.bbsConfirm = open;

  // 普通表单:data-confirm="文案" → 内置确认后提交
  document.addEventListener("submit", function (e) {
    var form = e.target;
    if (!form || !form.hasAttribute || !form.hasAttribute("data-confirm")) return;
    e.preventDefault();
    open(form.getAttribute("data-confirm"), function () { form.submit(); });
  }, true);

  // 按钮:data-confirm="文案" → 内置确认后提交所属表单(封禁等)
  document.addEventListener("click", function (e) {
    var btn = e.target.closest ? e.target.closest("button[data-confirm]") : null;
    if (!btn) return;
    var form = btn.closest("form");
    if (!form) return;
    e.preventDefault();
    open(btn.getAttribute("data-confirm"), function () { form.submit(); });
  }, true);

  // htmx 对每个 hx 请求都会发 htmx:confirm;只有带 hx-confirm 文案的
  // 请求(如删回复)才需要确认,点赞/收藏/发送等直接放行
  document.body.addEventListener("htmx:confirm", function (e) {
    if (!e.detail.question) return;
    e.preventDefault();
    open(e.detail.question, function () { e.detail.issueRequest(true); });
  });
})();

// 发帖:版块选择面板(替代浏览器原生下拉)
(function () {
  var pick = document.querySelector("[data-board-pick]");
  if (!pick) return;
  var field = pick.closest(".compose-field");
  if (!field) return;
  var input = field.querySelector('input[name="category"]');
  if (!input) return;
  var opts = Array.prototype.slice.call(pick.querySelectorAll("[data-board-opt]"));
  function sync(active) {
    opts.forEach(function (o) {
      var on = o === active || o.getAttribute("data-board-opt") === input.value;
      o.classList.toggle("on", on);
      o.setAttribute("aria-checked", on ? "true" : "false");
    });
  }
  opts.forEach(function (o) {
    o.addEventListener("click", function () {
      input.value = o.getAttribute("data-board-opt");
      sync(o);
    });
  });
  sync(null);
})();

// 管理后台:选项芯片(选管辖版块/封禁天数) → 写入表单隐藏 input,未选则拦截提交
(function () {
  document.querySelectorAll("[data-pick]").forEach(function (box) {
    var form = box.closest("form");
    if (!form) return;
    var name = box.getAttribute("data-pick");
    var input = form.querySelector('input[name="' + name + '"]');
    var chips = box.querySelectorAll(".pick-chip");
    function sync() {
      var active = box.querySelector(".pick-chip.on");
      if (input && active) input.value = active.getAttribute("data-val");
      chips.forEach(function (c) {
        c.classList.toggle("on", c === active);
        c.setAttribute("aria-pressed", c === active ? "true" : "false");
      });
    }
    chips.forEach(function (c) {
      c.addEventListener("click", function () {
        box.querySelectorAll(".pick-chip").forEach(function (o) { o.classList.remove("on"); });
        c.classList.add("on");
        sync();
      });
    });
    if (input) {
      form.addEventListener("submit", function (e) {
        if (!input.value && box.getAttribute("data-optional") === null) {
          e.preventDefault();
          box.classList.add("need");
          setTimeout(function () { box.classList.remove("need"); }, 600);
        }
      });
    }
    sync();
  });
})();

// 管理后台:用户管理弹窗(列表内直接操作;不进独立页面)
(function () {
  var modal = document.getElementById("um-modal");
  if (!modal) return;
  var body = document.getElementById("um-body");
  if (!body) return;
  var loadingHTML = '<div class="um-loading">加载中…</div>';

  function umPanelURL(id) {
    // 带上当前列表的 q / page;去掉可能残留的 panel 参数
    var s = location.search.replace(/[?&]panel=[^&]*/, "");
    if (s.indexOf("&") === 0) s = "?" + s.slice(1);
    return "/admin/users/" + id + "/panel" + s;
  }
  function openModal() { modal.hidden = false; }
  function clearPanelParam() {
    // 手动关闭弹窗后清掉 ?panel=,否则刷新会再次自动弹出
    var s = location.search.replace(/[?&]panel=[^&]*/, "");
    if (s.indexOf("&") === 0) s = "?" + s.slice(1);
    if (s !== location.search) {
      try { history.replaceState(null, "", location.pathname + s + location.hash); } catch (e) {}
    }
  }
  function closeModal() {
    modal.hidden = true;
    clearPanelParam();
  }
  function loadPanel(url) {
    body.innerHTML = loadingHTML;
    openModal();
    fetch(url, { headers: { "Accept": "text/html" } })
      .then(function (r) { return r.ok ? r.text() : Promise.reject(); })
      .then(function (html) {
        body.innerHTML = html;
        bindPanel(body);
      })
      .catch(function () {
        body.innerHTML = '<p class="empty">加载失败,请刷新页面重试</p>';
      });
  }
  function bindPanel(root) {
    root.querySelectorAll("[data-pick]").forEach(function (box) {
      var form = box.closest("form");
      if (!form) return;
      var name = box.getAttribute("data-pick");
      var input = form.querySelector('input[name="' + name + '"]');
      if (!input) return;
      var chips = Array.prototype.slice.call(box.querySelectorAll(".pick-chip:not(.off)"));
      function sync() {
        var v = input.value;
        chips.forEach(function (c) {
          c.classList.toggle("on", c.getAttribute("data-val") === v);
          c.setAttribute("aria-pressed", c.getAttribute("data-val") === v ? "true" : "false");
        });
        box.classList.remove("need");
      }
      chips.forEach(function (c) {
        c.addEventListener("click", function () {
          input.value = c.getAttribute("data-val");
          sync();
          if (name === "days") input.dispatchEvent(new Event("change", { bubbles: true }));
        });
      });
      input.addEventListener("input", sync);
      input.addEventListener("change", sync);
      form.addEventListener("submit", function (e) {
        if (!input.value && box.getAttribute("data-optional") === null) {
          e.preventDefault();
          box.classList.add("need");
          setTimeout(function () { box.classList.remove("need"); }, 600);
        }
      });
      sync();
    });
    // 账号认证:分类芯片(官方/厂商红 V、作者黄 V)+ 自定义文案 + 实时预览。
    // 分类决定 V 颜色,文案自由填写;两者都空 = 无认证(管理员/版主按身份显示)。
    root.querySelectorAll("[data-verify-edit]").forEach(function (box) {
      var kindInput = box.querySelector('input[name="kind"]');
      var titleInput = box.querySelector('input[name="title"]');
      if (!kindInput) return;
      var chips = Array.prototype.slice.call(box.querySelectorAll(".pick-chip"));
      var preview = box.querySelector("[data-verify-preview]");
      var seal = box.querySelector(".vb-seal");
      var auto = box.getAttribute("data-auto") || "";
      function kindCls(k) {
        if (k === "官方" || k === "厂商") return "v-red";
        if (k === "作者") return "v-yellow";
        return "";
      }
      function sync() {
        var k = kindInput.value;
        var t = titleInput ? titleInput.value.trim() : "";
        chips.forEach(function (c) {
          var on = c.getAttribute("data-kind") === k;
          c.classList.toggle("on", on);
          c.setAttribute("aria-pressed", on ? "true" : "false");
        });
        var label = t || k || auto;
        if (preview) preview.textContent = label || "不显示 V 标";
        if (seal) {
          seal.classList.remove("v-red", "v-yellow");
          var cls = kindCls(k);
          if (cls) seal.classList.add(cls);
        }
        box.classList.toggle("off", !label);
      }
      chips.forEach(function (c) {
        c.addEventListener("click", function () {
          kindInput.value = c.getAttribute("data-kind");
          sync();
        });
      });
      if (titleInput) titleInput.addEventListener("input", sync);
      sync();
    });
    // 账号等级:LV0–LV6 / 跟随经验 单选,写入隐藏 level(auto 表示自动)
    root.querySelectorAll("[data-lv-pick]").forEach(function (box) {
      var form = box.closest("form");
      var input = form ? form.querySelector('input[name="level"]') : null;
      if (!input) return;
      var chips = Array.prototype.slice.call(box.querySelectorAll(".pick-chip"));
      function sync() {
        chips.forEach(function (c) {
          var on = c.getAttribute("data-lv") === input.value;
          c.classList.toggle("on", on);
          c.setAttribute("aria-pressed", on ? "true" : "false");
        });
      }
      chips.forEach(function (c) {
        c.addEventListener("click", function () {
          input.value = c.getAttribute("data-lv");
          sync();
        });
      });
      sync();
    });
    // 后台换头像:点击头像选图后立即上传
    root.querySelectorAll("[data-um-avatar]").forEach(function (btn) {
      var form = btn.closest("form");
      var input = form ? form.querySelector('input[type="file"][name="avatar"]') : null;
      if (!input) return;
      btn.addEventListener("click", function () { input.click(); });
      input.addEventListener("change", function () {
        if (input.files && input.files.length) form.submit();
      });
    });
    // 称号标签:选中「自定义」时启用输入框
    root.querySelectorAll("[data-um-badge]").forEach(function (box) {
      var text = box.querySelector('input[name="badge_text"]');
      function sync() {
        var custom = box.querySelector('input[name="badge_mode"]:checked');
        var on = custom && custom.value === "custom";
        if (text) text.disabled = !on;
      }
      box.addEventListener("change", sync);
      sync();
    });
    // 聚焦第一个可交互控件,方便键盘操作
    var first = root.querySelector("button:not([disabled]), input:not([type=hidden])");
    if (first) try { first.focus(); } catch (e) {}
  }

  document.addEventListener("click", function (e) {
    var btn = e.target.closest ? e.target.closest(".js-um-open") : null;
    if (!btn) return;
    e.preventDefault();
    loadPanel(umPanelURL(btn.getAttribute("data-um-id")));
  });
  Array.prototype.forEach.call(modal.querySelectorAll("[data-um-cancel]"), function (el) {
    el.addEventListener("click", closeModal);
  });
  document.addEventListener("keydown", function (e) {
    if (e.key !== "Escape" || modal.hidden) return;
    var c = document.getElementById("bbs-modal");
    if (c && !c.hidden) return;
    closeModal();
  });

  // 操作回跳后自动重开(带 data-panel-href 时)
  var holder = document.querySelector("[data-panel-href]");
  if (holder) loadPanel(holder.getAttribute("data-panel-href"));
})();

// 管理后台:内容管理行尾「⋯」菜单(置顶/锁定/删除低频操作收拢)
(function () {
  function closeMenus(except) {
    document.querySelectorAll(".ct-more").forEach(function (m) {
      if (except && m === except) return;
      var menu = m.querySelector(".ct-menu");
      var btn = m.querySelector(".js-ct-more");
      if (!menu) return;
      menu.hidden = true;
      if (btn) btn.setAttribute("aria-expanded", "false");
    });
  }
  document.addEventListener("click", function (e) {
    var btn = e.target.closest ? e.target.closest(".js-ct-more") : null;
    if (!btn) return;
    e.preventDefault();
    var box = btn.closest(".ct-more");
    var menu = box ? box.querySelector(".ct-menu") : null;
    if (!menu) return;
    var wasOpen = !menu.hidden;
    closeMenus();
    menu.hidden = wasOpen;
    btn.setAttribute("aria-expanded", wasOpen ? "false" : "true");
  });
  document.addEventListener("click", function (e) {
    if (!e.target.closest(".ct-more")) closeMenus();
  });
  document.addEventListener("keydown", function (e) {
    if (e.key === "Escape") closeMenus();
  });
})();
