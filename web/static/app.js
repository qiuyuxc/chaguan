// bbs 前端交互(不依赖任何构建工具)
(function () {
  // htmx 回复成功后清空输入框
  document.body.addEventListener("htmx:afterRequest", function (e) {
    var elt = e.detail.elt;
    if (elt && elt.id === "reply-form" && e.detail.successful) {
      elt.reset();
      var posts = document.getElementById("posts");
      if (posts && posts.lastElementChild) {
        posts.lastElementChild.scrollIntoView({ behavior: "smooth", block: "nearest" });
      }
    }
  });
})();

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
