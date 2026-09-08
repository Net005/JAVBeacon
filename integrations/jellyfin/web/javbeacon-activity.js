/* Optional Jellyfin Web script. Load with a compatible JavaScript Injector plugin. */
(() => {
  const panelId = 'javbeaconActivityPanel';
  const itemId = () => new URLSearchParams(location.hash.split('?')[1] || '').get('id');
  async function render() {
    const id = itemId(), host = document.querySelector('.detailPagePrimaryContent');
    if (!id || !host || document.getElementById(panelId)) return;
    try {
      const item = await ApiClient.getItem(ApiClient.getCurrentUserId(), id);
      if (!item.ProviderIds?.JAVBeacon) return;
      const activity = await ApiClient.getJSON(ApiClient.getUrl(`JAVBeacon/items/${id}/activity`));
      const panel = document.createElement('div'); panel.id = panelId; panel.className = 'verticalSection';
      panel.innerHTML = `<h2 class="sectionTitle">JAVBeacon</h2><div class="itemsContainer"><span data-jb-o>O count: ${activity.oCount}</span> · <span>Plays: ${activity.playCount}</span> · <span>Played: ${Math.round(activity.playDurationSeconds / 60)} min</span> <button is="emby-button" class="raised" data-jb-add>+1 O</button></div>`;
      panel.querySelector('[data-jb-add]').onclick = async e => { e.currentTarget.disabled = true; try { const updated = await ApiClient.ajax({type:'POST',url:ApiClient.getUrl(`JAVBeacon/items/${id}/o`),dataType:'json'}); panel.querySelector('[data-jb-o]').textContent=`O count: ${updated.oCount}`; } finally { e.currentTarget.disabled = false; } };
      host.appendChild(panel);
    } catch (_) { /* Non-JAVBeacon items and transient API failures stay unobtrusive. */ }
  }
  new MutationObserver(render).observe(document.documentElement, {subtree:true,childList:true});
  addEventListener('hashchange', () => document.getElementById(panelId)?.remove()); render();
})();
