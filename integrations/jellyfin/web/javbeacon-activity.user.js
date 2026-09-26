// ==UserScript==
// @name         JAVBeacon Activity Panel for Jellyfin Web
// @namespace    https://github.com/Net005/JAVBeacon
// @version      1.0.0
// @description  Adds O count, play count, played duration, and a +1 O button to JAVBeacon-backed item pages in Jellyfin Web. No server-side plugin (e.g. Jellyfin-JavaScript-Injector) required - just a userscript manager.
// @author       Rick
// @match        *://*/web/*
// @match        *://*/*index.html*
// @run-at       document-idle
// @grant        unsafeWindow
// ==/UserScript==

/* Tampermonkey/Violentmonkey/Greasemonkey build of
 * integrations/jellyfin/web/javbeacon-activity.js - identical behavior, just
 * packaged as a standalone userscript instead of requiring the
 * Jellyfin-JavaScript-Injector plugin to load it server-side. Install it
 * directly in your userscript manager and it activates itself only on pages
 * where Jellyfin Web's own `ApiClient` global is present (harmless no-op
 * everywhere else), so the broad @match above is safe to leave as-is even if
 * you browse other sites in the same browser profile. Narrow the @match
 * lines to your own Jellyfin server's origin (e.g. "https://jav.example.com/web/*")
 * if you'd rather this only ever run there.
 */
(() => {
  const page = typeof unsafeWindow !== 'undefined' ? unsafeWindow : window;
  if (page.__javBeaconActivityInjected) return;
  page.__javBeaconActivityInjected = true;

  const styleId = 'javbeaconActivityStyle';
  if (!document.getElementById(styleId)) {
    const style = document.createElement('style');
    style.id = styleId;
    // Scoped, minimal styling matching Jellyfin Web's own spacing/typography
    // conventions (verticalSection/sectionTitle) instead of a single run-on
    // line of text and an inline button with no layout of its own.
    style.textContent = `
      #javbeaconActivityPanel .javbeaconActivityRow{display:flex;align-items:center;flex-wrap:wrap;gap:1.5em;}
      #javbeaconActivityPanel .javbeaconActivityStats{display:flex;flex-wrap:wrap;gap:1.75em;flex:1 1 auto;min-width:0;}
      #javbeaconActivityPanel .javbeaconStat{display:flex;flex-direction:column;gap:0.15em;min-width:0;}
      #javbeaconActivityPanel .javbeaconStat .javbeaconStatValue{font-size:1.3em;font-weight:600;line-height:1.1;}
      #javbeaconActivityPanel .javbeaconStat .javbeaconStatLabel{font-size:0.8em;opacity:0.7;text-transform:uppercase;letter-spacing:0.04em;}
      #javbeaconActivityPanel .javbeaconActivityAdd{flex:0 0 auto;white-space:nowrap;}
    `;
    document.head.appendChild(style);
  }

  const panelId = 'javbeaconActivityPanel';
  let generation = 0;
  let renderingId = '';
  let scheduled = false;
  let pollTimer = 0;

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
    // Running as a userscript rather than a page-injected <script>, ApiClient
    // may not exist yet the first few times the MutationObserver below fires
    // (Jellyfin Web's own bundle can still be loading) - just wait for the
    // next mutation/poll rather than treating that as "not a Jellyfin page".
    const ApiClient = page.ApiClient;
    if (!ApiClient) return;
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
      panel.innerHTML = `<h2 class="sectionTitle">JAVBeacon</h2><div class="itemsContainer javbeaconActivityRow">` +
        `<div class="javbeaconActivityStats">` +
        `<div class="javbeaconStat"><span class="javbeaconStatValue" data-jb-o>${oCount}</span><span class="javbeaconStatLabel">O count</span></div>` +
        `<div class="javbeaconStat"><span class="javbeaconStatValue">${playCount}</span><span class="javbeaconStatLabel">Plays</span></div>` +
        `<div class="javbeaconStat"><span class="javbeaconStatValue">${Math.round(playedSeconds / 60)} min</span><span class="javbeaconStatLabel">Played</span></div>` +
        `</div>` +
        `<button is="emby-button" type="button" class="raised javbeaconActivityAdd" data-jb-add><span>+1 O</span></button>` +
        `</div>`;
      panel.querySelector('[data-jb-add]').onclick = async event => {
        const button = event.currentTarget;
        button.disabled = true;
        try {
          const updated = responseJson(await ApiClient.ajax({ type: 'POST', url: ApiClient.getUrl(`JAVBeacon/items/${id}/o`), dataType: 'json' }));
          panel.querySelector('[data-jb-o]').textContent = number(field(updated, 'o_count', 'oCount'));
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
  // A userscript manager can inject before Jellyfin Web's own app bundle has
  // defined `ApiClient` at all (unlike the JavaScript Injector plugin, which
  // loads after the app shell). A short poll covers that first-load race
  // without needing a longer-lived observer once ApiClient shows up.
  pollTimer = setInterval(() => {
    if (page.ApiClient) {
      clearInterval(pollTimer);
      scheduleRender();
    }
  }, 500);
})();
