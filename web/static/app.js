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
