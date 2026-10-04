"use strict";
(() => {
  const $ = id => document.getElementById(id);
  const apiBase = "/moments/api/v1";
  const state = { me: null, csrf: "", epoch: 0, feed: [], feedCursor: "", detail: null, comments: [], likes: [], commentCursor: "", likeCursor: "", reply: null, draft: [], postKey: crypto.randomUUID(), pendingPost: null, pendingComment: null, gallery: [], photo: 0 };
  const date = value => new Intl.DateTimeFormat("zh-CN", { timeZone: "Asia/Shanghai", month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit" }).format(new Date(value));
  function node(tag, cls, value) { const item = document.createElement(tag); if (cls) item.className = cls; if (value !== undefined) item.textContent = value; return item; }
  function button(label, action, cls) { const item = node("button", cls, label); item.type = "button"; item.addEventListener("click", action); return item; }
  function notice(message) { $("notice").textContent = message; $("notice").hidden = !message; }
  async function api(path, options = {}) {
    const controller = new AbortController(); const timeout = setTimeout(() => controller.abort(), 40000);
    try {
      const headers = { ...options.headers };
      if (options.body !== undefined) headers["Content-Type"] = "application/json";
      if (options.method && options.method !== "GET") headers["X-CSRF-Token"] = state.csrf;
      const response = await fetch(apiBase + path, { ...options, headers, credentials: "same-origin", signal: controller.signal, body: options.body === undefined ? undefined : JSON.stringify(options.body) });
      const data = await response.json();
      if (!response.ok) { const error = new Error(data.error?.message || "请求失败"); error.status = response.status; throw error; }
      return data;
    } catch (error) { if (!error.status) error.message = "网络暂时不可用，请保留草稿并重试。"; throw error; }
    finally { clearTimeout(timeout); }
  }
  function show(id) { for (const view of ["login-view", "feed-view", "compose-view", "detail-view"]) $(view).hidden = view !== id; $("loading").hidden = true; }
  function loggedIn() { $("compose-open").hidden = !state.me; $("signout").hidden = !state.me; }
  function report(error) { notice(error.message); if (error.status === 401) { state.me = null; loggedIn(); show("login-view"); } }
  let activeImages = 0;
  const imageQueue = [];
  function scheduleImage(fn) { return new Promise((resolve, reject) => { imageQueue.push({ fn, resolve, reject }); drainImages(); }); }
  function drainImages() { while (activeImages < 4 && imageQueue.length) { const task = imageQueue.shift(); activeImages++; task.fn().then(task.resolve, task.reject).finally(() => { activeImages--; drainImages(); }); } }
  const observer = new IntersectionObserver(entries => { for (const entry of entries) if (entry.isIntersecting) { observer.unobserve(entry.target); entry.target.resolveImage(); } }, { rootMargin: "200px" });
  function imageFrom(path, alt) {
    const image = node("img"); image.alt = alt; image.loading = "lazy"; let retries = 0;
    image.resolveImage = async () => { try { const data = await scheduleImage(() => api(path)); if (image.isConnected) image.src = data.url; } catch { if (image.isConnected) image.alt = "图片暂时不可用"; } };
    image.addEventListener("error", () => { if (retries++ === 0) image.resolveImage(); });
    observer.observe(image); return image;
  }
  function avatar(author) { const item = node("span", "avatar", [...(author.name || "?")][0]); if (author.avatar) { const image = imageFrom(`/users/${author.id}/avatar`, author.name + "的头像"); image.addEventListener("load", () => { item.replaceChildren(image); }); item.append(image); } return item; }
  function authorLink(author) { const item = node("a", "author-name", author.name); item.href = author.id === state.me.id ? "#mine" : "#user/" + author.id; return item; }
  function previewComment(comment) { const item = node("div", "comment-preview"); item.append(node("span", "comment-name", comment.author.name + "："), node("span", "", comment.content)); return item; }
  async function openPhotos(assets, index) { state.gallery = assets; state.photo = index; $("photo-viewer").showModal(); await renderPhoto(); }
  async function renderPhoto() {
    const item = state.gallery[state.photo]; if (!item) return;
    $("photo-index").textContent = `${state.photo + 1} / ${state.gallery.length}`; $("photo-prev").disabled = state.photo === 0; $("photo-next").disabled = state.photo === state.gallery.length - 1;
    $("full-photo").removeAttribute("src"); $("full-photo").alt = "正在加载图片…";
    try { const data = await api(`/assets/${item.id}/download`); if (state.gallery[state.photo]?.id === item.id) { $("full-photo").src = data.url; $("full-photo").alt = "动态图片"; } } catch (error) { $("full-photo").alt = error.message; }
  }
  function renderPost(post, full = false) {
    const article = node("article", "post"); const header = node("div", "post-header"); const who = node("div"); who.append(authorLink(post.author), node("div", "post-time", `${date(post.created_at)} · ${post.visibility === "private" ? "仅自己可见" : "好友可见"}`)); header.append(avatar(post.author), who);
    if (post.can_delete) header.append(button("删除", async () => { if (!confirm("删除这条动态？图片和评论也将不再可见。")) return; try { await api(`/posts/${post.id}`, { method: "DELETE" }); if (full) location.hash = "#feed"; else await loadFeed(false); } catch (error) { report(error); } }, "delete-post"));
    const body = node("div", "post-body"); if (post.content) body.append(node("p", "post-content", post.content));
    if (post.assets.length) { const grid = node("div", "photo-grid" + (post.assets.length === 1 ? " single" : post.assets.length === 2 ? " two" : "")); post.assets.forEach((asset, i) => { const tile = button("", () => openPhotos(post.assets, i)); tile.setAttribute("aria-label", `查看第 ${i + 1} 张图片`); tile.append(imageFrom(`/assets/${asset.id}/download`, "动态图片")); grid.append(tile); }); body.append(grid); }
    const actions = node("div", "post-actions"); const like = button((post.liked_by_me ? "♥ 已赞" : "♡ 点赞") + ` ${post.like_count}`, async () => { like.disabled = true; try { await api(`/posts/${post.id}/like`, { method: post.liked_by_me ? "DELETE" : "PUT" }); const canonical = await api(`/posts/${post.id}`); const replacement = renderPost(canonical, full); article.replaceWith(replacement); if (full) { state.detail = canonical; await loadLikes(false); } } catch (error) { like.disabled = false; report(error); } }, post.liked_by_me ? "liked" : ""); actions.append(like, button(`评论 ${post.comment_count}`, () => { location.hash = "#post/" + post.id; })); body.append(actions);
    if (!full) { if (post.likes_preview.length) body.append(node("div", "like-preview", "♥ " + post.likes_preview.map(u => u.name).join("、") + (post.like_count > post.likes_preview.length ? " 等" : ""))); post.comments_preview.forEach(c => body.append(previewComment(c))); }
    article.append(header, body); return article;
  }
  function authorFilter() { if (location.hash === "#mine") return state.me.id; if (location.hash.startsWith("#user/")) return location.hash.slice(6); return ""; }
  async function loadFeed(more) {
    const epoch = state.epoch; const author = authorFilter(); const path = author ? `/users/${author}/posts` : "/feed";
    $("load-posts").disabled = true;
    try {
      const page = await api(path + "?limit=20" + (more && state.feedCursor ? "&cursor=" + encodeURIComponent(state.feedCursor) : "")); if (epoch !== state.epoch) return;
      if (!more) { for (const image of $("posts").querySelectorAll('img')) observer.unobserve(image); $("posts").replaceChildren(); state.feed = []; }
      for (const post of page.items) if (!state.feed.some(p => p.id === post.id)) { state.feed.push(post); $("posts").append(renderPost(post)); }
      if (!state.feed.length) $("posts").append(node("p", "empty", "还没有动态。写下你的第一条近况吧。"));
      state.feedCursor = page.next_cursor; $("load-posts").hidden = !page.has_more;
      $("feed-title").textContent = author === state.me.id ? "我的近况" : author ? "个人动态" : "朋友们的近况";
    } catch (error) { if (epoch === state.epoch) report(error); } finally { $("load-posts").disabled = false; $("loading").hidden = true; }
  }
  function renderComment(comment) {
    const item = node("div", "comment-row"); item.append(authorLink(comment.author)); if (comment.reply_to_comment_id) item.append(node("span", "muted", " · 回复评论 #" + comment.reply_to_comment_id)); item.append(node("p", "", comment.deleted ? "原评论已删除" : comment.content)); const meta = node("div", "meta", date(comment.created_at));
    if (!comment.deleted) meta.append(button("回复", () => { state.reply = comment; $("reply-target").textContent = "回复 " + comment.author.name; $("reply-target").hidden = false; $("reply-cancel").hidden = false; $("comment-input").focus(); }));
    if (comment.can_delete) meta.append(button("删除", async () => { if (!confirm("删除这条评论？")) return; try { await api(`/comments/${comment.id}`, { method: "DELETE" }); await loadComments(false); await refreshDetail(); } catch (error) { report(error); } })); item.append(meta); return item;
  }
  async function loadComments(more) {
    if (!state.detail) return; const postID = state.detail.id; const epoch = state.epoch;
    $("load-comments").disabled = true;
    try { const page = await api(`/posts/${postID}/comments?limit=20` + (more && state.commentCursor ? "&cursor=" + encodeURIComponent(state.commentCursor) : "")); if (epoch !== state.epoch) return;
      if (!more) { state.comments = []; $("all-comments").replaceChildren(); }
      for (const c of page.items) if (!state.comments.some(old => old.id === c.id)) { state.comments.push(c); $("all-comments").append(renderComment(c)); }
      state.commentCursor = page.next_cursor; $("load-comments").hidden = !page.has_more;
      if (!state.comments.length) $("all-comments").append(node("p", "muted", "还没有评论。"));
    } catch (error) { report(error); } finally { $("load-comments").disabled = false; }
  }
  async function loadLikes(more) {
    if (!state.detail) return; const postID = state.detail.id; const epoch = state.epoch; $("load-likes").disabled = true;
    try { const page = await api(`/posts/${postID}/likes?limit=20` + (more && state.likeCursor ? "&cursor=" + encodeURIComponent(state.likeCursor) : "")); if (epoch !== state.epoch) return;
      if (!more) { state.likes = []; $("all-likes").replaceChildren(); }
      for (const like of page.items) if (!state.likes.some(old => old.author.id === like.author.id)) { state.likes.push(like); const item = node("div", "like-list"); item.append(avatar(like.author), authorLink(like.author), node("span", "muted", date(like.created_at))); $("all-likes").append(item); }
      state.likeCursor = page.next_cursor; $("load-likes").hidden = !page.has_more; if (!state.likes.length) $("all-likes").append(node("p", "muted", "还没有人点赞。"));
    } catch (error) { report(error); } finally { $("load-likes").disabled = false; }
  }
  async function refreshDetail() { const post = await api(`/posts/${state.detail.id}`); state.detail = post; $("detail-post").replaceChildren(renderPost(post, true)); }
  async function navigate() {
    state.epoch++; notice(""); if (!state.me) { show("login-view"); return; }
    $("nav-feed").classList.toggle("active", !location.hash || location.hash === "#feed"); $("nav-mine").classList.toggle("active", location.hash === "#mine");
    if (location.hash === "#compose") { show("compose-view"); renderDraft(); return; }
    if (location.hash.startsWith("#post/")) { show("detail-view"); $("detail-post").replaceChildren(); $("all-comments").replaceChildren(); $("all-likes").replaceChildren(); cancelReply(); const epoch = state.epoch;
      try { const post = await api(`/posts/${location.hash.slice(6)}`); if (epoch !== state.epoch) return; state.detail = post; $("detail-post").append(renderPost(post, true)); await Promise.all([loadComments(false), loadLikes(false)]); } catch (error) { if (epoch === state.epoch) report(error); } return;
    }
    show("feed-view"); $("posts").replaceChildren(); $("loading").hidden = false; $("load-posts").hidden = true;
    $("feed-subtitle").textContent = new Intl.DateTimeFormat("zh-CN", { timeZone: "Asia/Shanghai", month: "long", day: "numeric", weekday: "long" }).format(new Date()); await loadFeed(false);
  }
  function cancelReply() { state.reply = null; $("reply-target").hidden = true; $("reply-cancel").hidden = true; }
  function renderDraft() { $("draft-images").replaceChildren(); state.draft.forEach((photo, i) => { const item = node("div", "draft-photo"); const image = node("img"); image.src = photo.preview; image.alt = photo.file.name; item.append(image, button("移除", () => { if (state.pendingPost) { $("publish-error").textContent = "上一条发布结果不确定，请先重试确认后再修改。"; return; } URL.revokeObjectURL(photo.preview); state.draft.splice(i, 1); renderDraft(); }), node("span", "", photo.status || "待上传")); $("draft-images").append(item); }); }
  async function uploadPhoto(photo) {
    if (photo.assetID) return photo.assetID;
    if (!crypto.subtle) throw new Error("图片上传需要 HTTPS，或在 localhost 调试。");
    photo.status = "上传中…"; renderDraft();
    const bytes = await photo.file.arrayBuffer(); const digest = await crypto.subtle.digest("SHA-512", bytes); const hash = [...new Uint8Array(digest)].map(b => b.toString(16).padStart(2, "0")).join("");
    const upload = await api("/assets", { method: "POST", body: { client_asset_id: photo.key, hash, size: photo.file.size, mime: photo.file.type } });
    if (upload.status !== "verified") {
      const controller = new AbortController(); const timeout = setTimeout(() => controller.abort(), 60000);
      try { const response = await fetch(upload.upload_url, { method: "PUT", headers: upload.headers, body: photo.file, signal: controller.signal, credentials: "omit" }); if (!response.ok) throw new Error("图片上传失败，请重试。"); } finally { clearTimeout(timeout); }
      await api(`/assets/${upload.asset.id}/verify`, { method: "POST" });
    }
    photo.assetID = upload.asset.id; photo.status = "已校验"; renderDraft(); return photo.assetID;
  }
  function updateCount() { $("draft-count").textContent = `${[...$("draft").value].length} / 4096`; }
  $("login-form").addEventListener("submit", async event => { event.preventDefault(); const btn = event.submitter; btn.disabled = true; $("login-error").textContent = "";
    try { const session = await api("/session", { method: "POST", body: { account: $("account").value, password: $("password").value } }); state.csrf = session.csrf_token; $("password").value = ""; await boot(); } catch (error) { $("login-error").textContent = error.message; } finally { btn.disabled = false; }
  });
  $("signout").addEventListener("click", async () => { try { await api("/session", { method: "DELETE" }); state.me = null; state.csrf = ""; state.draft.forEach(p => URL.revokeObjectURL(p.preview)); state.draft = []; state.pendingPost = null; state.pendingComment = null; state.feed = []; state.detail = null; state.comments = []; state.likes = []; observer.disconnect(); for (const id of ['posts','detail-post','all-comments','all-likes']) $(id).replaceChildren(); $("draft").value = ""; $("comment-input").value = ""; state.postKey = crypto.randomUUID(); state.epoch++; loggedIn(); show("login-view"); } catch (error) { report(error); } });
  $("compose-open").addEventListener("click", () => { location.hash = "#compose"; }); $("quick-compose").addEventListener("click", () => { location.hash = "#compose"; }); $("compose-cancel").addEventListener("click", () => { location.hash = "#feed"; }); $("detail-back").addEventListener("click", () => { location.hash = "#feed"; });
  $("refresh").addEventListener("click", () => { notice(""); loadFeed(false); }); $("load-posts").addEventListener("click", () => loadFeed(true)); $("load-comments").addEventListener("click", () => loadComments(true)); $("load-likes").addEventListener("click", () => loadLikes(true)); $("reply-cancel").addEventListener("click", cancelReply); $("draft").addEventListener("input", updateCount);
  $("photo-input").addEventListener("change", event => { $("publish-error").textContent = ""; for (const file of event.target.files) { if (state.pendingPost) { $("publish-error").textContent = "上一条发布结果不确定，请先重试确认。"; break; } if (state.draft.length >= 9) { $("publish-error").textContent = "最多添加 9 张图片。"; break; } if (!["image/jpeg", "image/png"].includes(file.type) || file.size > 10 * 1024 * 1024 || file.size === 0) { $("publish-error").textContent = "仅支持 10 MiB 以内的 JPEG / PNG 图片。"; continue; } state.draft.push({ file, preview: URL.createObjectURL(file), key: crypto.randomUUID() }); } event.target.value = ""; renderDraft(); });
  $("publish").addEventListener("click", async () => {
    $("publish-error").textContent = ""; const content = $("draft").value; const visibility = $("visibility").value;
    if (!content.trim() && !state.draft.length || [...content].length > 4096 || new TextEncoder().encode(content).length > 16384) { $("publish-error").textContent = "请输入正文或添加图片，正文最多 4096 字 / 16 KiB。"; return; }
    if (state.pendingPost && (state.pendingPost.content !== content || state.pendingPost.visibility !== visibility)) { $("publish-error").textContent = "上一条结果不确定。请恢复原正文和可见性并重试，以免重复发布。"; return; }
    $("publish").disabled = true; $("draft").readOnly = true; $("visibility").disabled = true; $("photo-input").disabled = true;
    try {
      if (!state.pendingPost) { const ids = []; for (const photo of state.draft) ids.push(await uploadPhoto(photo)); state.pendingPost = { client_post_id: state.postKey, content, visibility, asset_ids: ids }; }
      const post = await api("/posts", { method: "POST", body: state.pendingPost }); state.draft.forEach(p => URL.revokeObjectURL(p.preview)); state.draft = []; state.pendingPost = null; state.postKey = crypto.randomUUID(); $("draft").value = ""; updateCount(); location.hash = "#post/" + post.id;
    } catch (error) { $("publish-error").textContent = error.message; if (error.status && error.status < 500 && error.status !== 401) { state.pendingPost = null; state.postKey = crypto.randomUUID(); } }
    finally { $("publish").disabled = false; $("draft").readOnly = false; $("visibility").disabled = false; $("photo-input").disabled = false; }
  });
  $("comment-form").addEventListener("submit", async event => { event.preventDefault(); if (!state.detail) return; const content = $("comment-input").value; $("comment-error").textContent = "";
    if (!content.trim() || [...content].length > 1000 || new TextEncoder().encode(content).length > 4096) { $("comment-error").textContent = "评论不能为空，最多 1000 字 / 4 KiB。"; return; }
    const reply = state.reply?.id || "";
    if (state.pendingComment && (state.pendingComment.postID !== state.detail.id || state.pendingComment.body.content !== content || state.pendingComment.body.reply_to_comment_id !== reply)) { $("comment-error").textContent = "上条评论结果不确定，请先重试原内容。"; return; }
    if (!state.pendingComment) state.pendingComment = { postID: state.detail.id, body: { client_comment_id: crypto.randomUUID(), content, reply_to_comment_id: reply } };
    event.submitter.disabled = true;
    try { await api(`/posts/${state.pendingComment.postID}/comments`, { method: "POST", body: state.pendingComment.body }); state.pendingComment = null; $("comment-input").value = ""; cancelReply(); await loadComments(false); await refreshDetail(); } catch (error) { $("comment-error").textContent = error.message; if (error.status && error.status < 500 && error.status !== 401) state.pendingComment = null; } finally { event.submitter.disabled = false; }
  });
  $("photo-close").addEventListener("click", () => $("photo-viewer").close()); $("photo-prev").addEventListener("click", () => { state.photo--; renderPhoto(); }); $("photo-next").addEventListener("click", () => { state.photo++; renderPhoto(); });
  window.addEventListener("hashchange", navigate);
  async function boot() { try { const response = await api("/me"); if (state.draftOwner && state.draftOwner !== response.user.id) { state.draft.forEach(p => URL.revokeObjectURL(p.preview)); state.draft = []; state.pendingPost = null; state.pendingComment = null; state.postKey = crypto.randomUUID(); $("draft").value = ""; $("comment-input").value = ""; } state.draftOwner = response.user.id; state.me = response.user; state.csrf = response.csrf_token; loggedIn(); const self = avatar(state.me); self.id = "self-avatar"; $("self-avatar").replaceWith(self); await navigate(); } catch (error) { if (error.status === 401) { state.me = null; loggedIn(); show("login-view"); } else { $("loading").hidden = true; report(error); } } }
  boot();
})();
