(function () {
  'use strict'
  const state = { articles: [], ids: new Set(), latest: 0, scope: 'symbol', kind: '', activeTab: 'depth', unread: 0, socket: null, retry: 500 }
  const byId = id => document.getElementById(id)

  function safeURL(value) {
    try {
      const url = new URL(value)
      return url.protocol === 'https:' || url.protocol === 'http:' ? url.href : ''
    } catch (_) { return '' }
  }

  function currentSymbol() { return (byId('symbol')?.value || '').trim().toUpperCase() }
  function filteredArticles() {
    const symbol = currentSymbol()
    return state.articles.filter(article => (!state.kind || article.kind === state.kind) && (state.scope === 'all' || (article.symbols || []).includes(symbol)))
  }

  function render() {
    const rows = byId('news-rows')
    if (!rows) return
    rows.replaceChildren()
    const articles = filteredArticles().slice(0, 100)
    if (!articles.length) {
      const empty = document.createElement('p'); empty.className = 'live-empty'; empty.textContent = state.scope === 'all' ? '暂无新闻' : '当前股票暂无新闻'; rows.append(empty); return
    }
    for (const article of articles) {
      const link = document.createElement('a'); link.className = 'news-item'; link.target = '_blank'; link.rel = 'noopener noreferrer'; link.href = safeURL(article.url) || '#'
      const meta = document.createElement('span'); meta.className = 'news-meta'
      const kind = document.createElement('span'); kind.className = 'news-kind'; kind.textContent = article.kind === 'press_release' ? '公司公告' : '股票新闻'; meta.append(kind)
      for (const symbol of (article.symbols || []).slice(0, 3)) { const tag = document.createElement('span'); tag.className = 'news-symbol'; tag.textContent = symbol; meta.append(tag) }
      const publisher = document.createElement('span'); publisher.textContent = article.publisher || article.provider || 'FMP'; meta.append(publisher)
      const time = document.createElement('time'); const date = new Date(article.published_at); time.dateTime = article.published_at || ''; time.textContent = Number.isNaN(date.getTime()) ? '' : date.toLocaleString(); meta.append(time)
      const title = document.createElement('strong'); title.textContent = article.title || ''
      const imageURL = safeURL(article.image_url)
      if (imageURL) { const image = document.createElement('img'); image.src = imageURL; image.alt = ''; image.loading = 'lazy'; image.referrerPolicy = 'no-referrer'; link.append(image) }
      link.append(meta, title)
      if (article.summary) { const summary = document.createElement('p'); summary.textContent = article.summary; link.append(summary) }
      rows.append(link)
    }
  }

  function add(article, live) {
    if (!article || state.ids.has(article.id)) return
    state.ids.add(article.id); state.latest = Math.max(state.latest, Number(article.sequence) || 0)
    state.articles.unshift(article); state.articles.sort((a, b) => Number(b.sequence) - Number(a.sequence)); state.articles = state.articles.slice(0, 500)
    if (live && state.activeTab !== 'news') { state.unread++; const badge = byId('news-unread'); badge.hidden = false; badge.textContent = String(state.unread) }
    render()
  }

  async function load() {
    try {
      const response = await fetch('/v1/news?limit=100')
      if (!response.ok) throw new Error(`HTTP ${response.status}`)
      const payload = await response.json()
      for (const article of payload.news || []) add(article, false)
      state.latest = Math.max(state.latest, Number(payload.latest_sequence) || 0)
    } catch (error) { byId('news-state').textContent = `加载失败：${error.message}` }
  }

  function connect() {
    const scheme = location.protocol === 'https:' ? 'wss' : 'ws'
    const ws = new WebSocket(`${scheme}://${location.host}/v1/news/ws`); state.socket = ws; byId('news-state').textContent = '连接中'
    ws.onmessage = event => {
      let payload
      try { payload = JSON.parse(event.data) } catch (_) { return }
      if (payload.type === 'status') { byId('news-state').textContent = payload.state === 'connected' ? '实时更新' : (payload.detail || payload.state); return }
      if (payload.type === 'news') { add(payload.article, true); byId('news-state').textContent = '实时更新' }
      if (payload.type === 'gap') { byId('news-state').textContent = '正在补齐'; load() }
    }
    ws.onopen = () => { state.retry = 500; ws.send(JSON.stringify({ after_sequence: state.latest, status: true })) }
    ws.onclose = () => { if (state.socket !== ws) return; byId('news-state').textContent = '正在重连'; setTimeout(connect, state.retry); state.retry = Math.min(30000, state.retry * 2) }
    ws.onerror = () => byId('news-state').textContent = '连接异常'
  }

  for (const button of document.querySelectorAll('[data-live-tab]')) button.addEventListener('click', () => {
    state.activeTab = button.dataset.liveTab
    for (const item of document.querySelectorAll('[data-live-tab]')) item.setAttribute('aria-pressed', String(item === button))
    for (const view of document.querySelectorAll('[data-live-view]')) view.hidden = view.dataset.liveView !== state.activeTab
    if (state.activeTab === 'news') { state.unread = 0; byId('news-unread').hidden = true; render() }
  })
  for (const button of document.querySelectorAll('[data-news-scope]')) button.addEventListener('click', () => {
    state.scope = button.dataset.newsScope
    for (const item of document.querySelectorAll('[data-news-scope]')) item.setAttribute('aria-pressed', String(item === button))
    render()
  })
  byId('news-kind')?.addEventListener('change', event => { state.kind = event.target.value; render() })
  byId('query')?.addEventListener('submit', () => setTimeout(render, 0))
  load().then(connect)

  if (typeof module !== 'undefined') module.exports = { safeURL }
})()
