/* Optional Jellyfin Web script. Load with the JavaScript Injector plugin. */
(() => {
  if (window.__javBeaconActivityInjected) return;
  window.__javBeaconActivityInjected = true;

  const panelId = 'javbeaconActivityPanel';
  let generation = 0;
  let renderingId = '';
  let scheduled = false;

  const itemId = () => new URLSearchParams(location.hash.split('?')[1] || '').get('id') || '';
  const field = (value, snake, camel) => value?.[snake] ?? value?.[camel] ?? value?.[camel[0].toUpperCase() + camel.slice(1)];
  const number = value => {
    const parsed = Number(value);
    return Number.isFinite(parsed) ? parsed : 0;
  };
  const responseJson = value => {
    if (typeof value !== 'string') return value || {};
    try { return JSON.parse(value); } catch (_) { return {}; }
  };
  const visibleHost = () => [...document.querySelectorAll('.detailPagePrimaryContent')]
    .find(element => element.offsetParent !== null) || document.querySelector('.detailPagePrimaryContent');
  const removePanels = () => document.querySelectorAll(`#${panelId}`).forEach(panel => panel.remove());

  async function render() {
    const id = itemId();
    const host = visibleHost();
    if (!id || !host || renderingId === id || document.getElementById(panelId)) return;
    const requestGeneration = generation;
    renderingId = id;
    try {
      const item = await ApiClient.getItem(ApiClient.getCurrentUserId(), id);
      if (requestGeneration !== generation || itemId() !== id || !item.ProviderIds?.JAVBeacon) return;
      const activity = responseJson(await ApiClient.getJSON(ApiClient.getUrl(`JAVBeacon/items/${id}/activity`)));
      if (requestGeneration !== generation || itemId() !== id || document.getElementById(panelId)) return;

      const oCount = number(field(activity, 'o_count', 'oCount'));
      const playCount = number(field(activity, 'play_count', 'playCount'));
      const playedSeconds = number(field(activity, 'play_duration_seconds', 'playDurationSeconds'));
      const panel = document.createElement('div');
      panel.id = panelId;
      panel.className = 'verticalSection';
      panel.dataset.itemId = id;
      panel.innerHTML = `<h2 class="sectionTitle">JAVBeacon</h2><div class="itemsContainer"><span data-jb-o>O count: ${oCount}</span> · <span>Plays: ${playCount}</span> · <span>Played: ${Math.round(playedSeconds / 60)} min</span> <button is="emby-button" class="raised" data-jb-add>+1 O</button></div>`;
      panel.querySelector('[data-jb-add]').onclick = async event => {
        const button = event.currentTarget;
        button.disabled = true;
        try {
          const updated = responseJson(await ApiClient.ajax({ type: 'POST', url: ApiClient.getUrl(`JAVBeacon/items/${id}/o`), dataType: 'json' }));
          panel.querySelector('[data-jb-o]').textContent = `O count: ${number(field(updated, 'o_count', 'oCount'))}`;
        } finally {
          button.disabled = false;
        }
      };
      host.appendChild(panel);
    } catch (_) {
      // Non-JAVBeacon items and transient API failures stay unobtrusive.
    } finally {
      if (renderingId === id) renderingId = '';
    }
  }

  const scheduleRender = () => {
    if (scheduled) return;
    scheduled = true;
    requestAnimationFrame(() => { scheduled = false; render(); });
  };
  new MutationObserver(scheduleRender).observe(document.documentElement, { subtree: true, childList: true });
  addEventListener('hashchange', () => { generation++; renderingId = ''; removePanels(); scheduleRender(); });
  scheduleRender();
})();
