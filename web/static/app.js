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
