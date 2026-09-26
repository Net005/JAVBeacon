(function () {
  "use strict";

  const PLUGIN_ID = "javbeacon-realtime";
  const React = window.PluginApi.React;
  const { gql, useQuery } = window.PluginApi.libraries.Apollo;

  const DEFAULT_HOVER_DELAY_MS = 400;
  const DEFAULT_CYCLE_INTERVAL_MS = 700;
  const MIN_CYCLE_INTERVAL_MS = 100;
  // video.js mounts its player element asynchronously after the scene query
  // resolves. Poll briefly instead of relying on a single lookup right after
  // the scene page patch runs.
  const PLAYER_ATTACH_RETRY_MS = 250;
  const PLAYER_ATTACH_MAX_ATTEMPTS = 40;

  const FIND_PLUGIN_SETTINGS = gql`
    query JAVBeaconScrubberSettings {
      configuration {
        plugins(include: ["javbeacon-realtime"])
      }
    }
  `;

  function usePluginSettings() {
    const result = useQuery(FIND_PLUGIN_SETTINGS, { fetchPolicy: "no-cache" });
    return result.data?.configuration?.plugins?.[PLUGIN_ID];
  }

  function numberSetting(settings, key, fallback, minimum = 0) {
    const raw = settings?.[key];
    if (raw === "" || raw == null) return fallback;
    const value = Number(raw);
    return Number.isFinite(value) && value >= minimum ? value : fallback;
  }

  function boolSetting(settings, key, fallback) {
    const raw = settings?.[key];
    if (raw == null || raw === "") return fallback;
    return raw === true || raw === "true";
  }

  // ---- WebVTT sprite sheet parsing -----------------------------------
  //
  // Stash's generated sprite VTT files follow the standard "media fragment"
  // convention also used by e.g. Plex and video.js's own thumbnail plugins:
  // each cue's payload line is an image URL (usually relative to the VTT
  // file itself) with a #xywh=x,y,w,h fragment identifying the crop of a
  // larger sprite sheet that represents that cue's time range. These
  // functions are pure and DOM-free so they can run under a plain Node test.

  const TIMESTAMP_RE = /(\d{2,}):(\d{2}):(\d{2})[.,](\d{3})/;

  function parseVttTimestamp(value) {
    const match = TIMESTAMP_RE.exec(String(value || ""));
    if (!match) return null;
    const [, hours, minutes, seconds, millis] = match;
    return (
      Number(hours) * 3600 +
      Number(minutes) * 60 +
      Number(seconds) +
      Number(millis) / 1000
    );
  }

  function parseCueImageLine(line, baseUrl) {
    const trimmed = String(line || "").trim();
    if (!trimmed) return null;
    const hashIndex = trimmed.indexOf("#xywh=");
    const path = hashIndex >= 0 ? trimmed.slice(0, hashIndex) : trimmed;
    let url = path;
    try {
      url = new URL(path, baseUrl || undefined).href;
    } catch (_) {
      // Relative path with no usable base: fall back to the raw string, the
      // way a plain <img src> would if it could not be resolved either.
    }
    if (hashIndex < 0) return { url, x: 0, y: 0, w: 0, h: 0, whole: true };
    const parts = trimmed
      .slice(hashIndex + 6)
      .split(",")
      .map((part) => Number(part.trim()));
    if (parts.length !== 4 || parts.some((n) => !Number.isFinite(n))) return null;
    const [x, y, w, h] = parts;
    if (w <= 0 || h <= 0) return null;
    return { url, x, y, w, h, whole: false };
  }

  function parseSpriteVtt(text, baseUrl) {
    const lines = String(text || "").split(/\r\n|\n|\r/);
    const cues = [];
    for (let i = 0; i < lines.length; i++) {
      if (!lines[i].includes("-->")) continue;
      const [startRaw, endRaw] = lines[i].split("-->");
      const start = parseVttTimestamp(startRaw);
      const end = parseVttTimestamp(endRaw);
      let payload = i + 1 < lines.length ? lines[i + 1] : "";
      while (payload !== undefined && payload.trim() === "" && i + 1 < lines.length) {
        i++;
        payload = i + 1 < lines.length ? lines[i + 1] : "";
      }
      if (start == null || end == null || end <= start) continue;
      const image = parseCueImageLine(payload, baseUrl);
      if (image) cues.push({ start, end, ...image });
    }
    cues.sort((a, b) => a.start - b.start);
    return cues;
  }

  // Cues are contiguous and sorted, so a binary search finds the cue whose
  // [start, end) range contains the requested time in O(log n). Falls back
  // to the first/last cue for times just outside the covered range (e.g. the
  // last fraction of a second before a video's exact duration).
  function findCueAtTime(cues, time) {
    if (!Array.isArray(cues) || cues.length === 0 || !Number.isFinite(time)) {
      return null;
    }
    let lo = 0;
    let hi = cues.length - 1;
    while (lo <= hi) {
      const mid = (lo + hi) >> 1;
      const cue = cues[mid];
      if (time < cue.start) hi = mid - 1;
      else if (time >= cue.end) lo = mid + 1;
      else return cue;
    }
    if (time <= cues[0].start) return cues[0];
    if (time >= cues[cues.length - 1].end) return cues[cues.length - 1];
    return null;
  }

  // Exposed for the Node-based unit test harness only; production code paths
  // never read this. Mirrors how the rest of this plugin is tested by
  // requiring the browser file against a faked `window`.
  window.__javbeaconScrubberInternals = {
    parseVttTimestamp,
    parseCueImageLine,
    parseSpriteVtt,
    findCueAtTime,
  };

  // ---- Sprite sheet natural size, cached per URL ----------------------

  const spriteSizeCache = new Map();

  function loadSpriteNaturalSize(url) {
    if (spriteSizeCache.has(url)) return spriteSizeCache.get(url);
    const promise = new Promise((resolve) => {
      const img = new Image();
      img.onload = () => resolve({ width: img.naturalWidth, height: img.naturalHeight });
      img.onerror = () => resolve(null);
      img.src = url;
    });
    spriteSizeCache.set(url, promise);
    return promise;
  }

  // ---- Overlay painting -------------------------------------------------

  function ensureOverlay(playerEl) {
    let overlay = playerEl.querySelector(":scope > .javbeacon-scrub-overlay");
    if (overlay) return overlay;
    overlay = document.createElement("div");
    overlay.className = "javbeacon-scrub-overlay";
    overlay.setAttribute("aria-hidden", "true");
    playerEl.appendChild(overlay);
    return overlay;
  }

  function showOverlay(overlay) {
    overlay.classList.add("is-visible");
  }

  function hideOverlay(overlay) {
    overlay.classList.remove("is-visible");
  }

  // Crops the cue's region out of its sprite sheet and scales that crop up
  // (or down) to fill the current player box, using the sprite's real pixel
  // dimensions so the result stays as sharp as the source sprite allows -
  // this is what replaces Stash's small fixed-size seek-bar thumbnail.
  async function paintCue(overlay, cue, box) {
    if (!cue || !box || box.width <= 0 || box.height <= 0) return;
    if (cue.whole) {
      overlay.style.backgroundImage = `url("${cue.url}")`;
      overlay.style.backgroundPosition = "center";
      overlay.style.backgroundSize = "cover";
      return;
    }
    const natural = await loadSpriteNaturalSize(cue.url);
    if (!natural) return;
    const scale = box.width / cue.w;
    overlay.style.backgroundImage = `url("${cue.url}")`;
    overlay.style.backgroundPosition = `-${(cue.x * scale).toFixed(2)}px -${(cue.y * scale).toFixed(2)}px`;
    overlay.style.backgroundSize = `${(natural.width * scale).toFixed(2)}px ${(natural.height * scale).toFixed(2)}px`;
  }

  function playerDurationSeconds(playerEl) {
    const duration = playerEl.querySelector("video")?.duration;
    return Number.isFinite(duration) && duration > 0 ? duration : null;
  }

  function findPlayerElement() {
    const candidates = [
      ".scene-player-container .video-js",
      ".scene-player .video-js",
      ".video-js",
    ];
    for (const selector of candidates) {
      const el = document.querySelector(selector);
      if (el) return el;
    }
    return null;
  }

  // ---- Wiring hover/seek behaviour onto one attached player -------------

  function attachScrubber(playerEl, cuesPromise, options) {
    const { hoverDelayMs, cycleIntervalMs, coverEnabled, seekEnabled } = options;
    const overlay = ensureOverlay(playerEl);
    let cues = null;
    let disposed = false;
    cuesPromise.then((resolved) => {
      if (!disposed) cues = resolved;
    });

    let hoverTimer = null;
    let cycleTimer = null;

    function stopCycle() {
      clearTimeout(hoverTimer);
      hoverTimer = null;
      clearInterval(cycleTimer);
      cycleTimer = null;
    }

    function playerHasStarted() {
      return playerEl.classList.contains("vjs-has-started");
    }

    function coverBox() {
      const poster = playerEl.querySelector(".vjs-poster") || playerEl;
      return poster.getBoundingClientRect();
    }

    function onPosterEnter() {
      if (!coverEnabled || !cues || cues.length === 0 || playerHasStarted()) return;
      stopCycle();
      hoverTimer = setTimeout(() => {
        let index = 0;
        showOverlay(overlay);
        paintCue(overlay, cues[index], coverBox());
        cycleTimer = setInterval(() => {
          index = (index + 1) % cues.length;
          paintCue(overlay, cues[index], coverBox());
        }, cycleIntervalMs);
      }, hoverDelayMs);
    }

    function onPosterLeave() {
      stopCycle();
      hideOverlay(overlay);
    }

    function onSeekMove(event) {
      if (!seekEnabled || !cues || cues.length === 0) return;
      const rect = event.currentTarget.getBoundingClientRect();
      if (rect.width <= 0) return;
      const ratio = Math.min(1, Math.max(0, (event.clientX - rect.left) / rect.width));
      const duration = playerDurationSeconds(playerEl);
      if (!duration) return;
      const cue = findCueAtTime(cues, ratio * duration);
      if (!cue) return;
      stopCycle();
      showOverlay(overlay);
      paintCue(overlay, cue, playerEl.getBoundingClientRect());
    }

    function onSeekLeave() {
      hideOverlay(overlay);
    }

    const poster = playerEl.querySelector(".vjs-poster");
    const progress = playerEl.querySelector(".vjs-progress-control");
    poster?.addEventListener("mouseenter", onPosterEnter);
    poster?.addEventListener("mouseleave", onPosterLeave);
    progress?.addEventListener("mousemove", onSeekMove);
    progress?.addEventListener("mouseleave", onSeekLeave);

    return function detach() {
      disposed = true;
      stopCycle();
      poster?.removeEventListener("mouseenter", onPosterEnter);
      poster?.removeEventListener("mouseleave", onPosterLeave);
      progress?.removeEventListener("mousemove", onSeekMove);
      progress?.removeEventListener("mouseleave", onSeekLeave);
      overlay.remove();
    };
  }

  function fetchCues(vttUrl) {
    return fetch(vttUrl, { credentials: "same-origin" })
      .then((response) => (response.ok ? response.text() : Promise.reject(new Error("sprite VTT request failed"))))
      .then((text) => parseSpriteVtt(text, vttUrl))
      .catch(() => []);
  }

  function setupScrubberForScene(scene, settings) {
    const vttUrl = scene?.paths?.vtt;
    const coverEnabled = boolSetting(settings, "scrubber_cover_enabled", true);
    const seekEnabled = boolSetting(settings, "scrubber_seek_preview_enabled", true);
    if (!vttUrl || (!coverEnabled && !seekEnabled)) return () => {};

    const hoverDelayMs = numberSetting(settings, "scrubber_hover_delay_ms", DEFAULT_HOVER_DELAY_MS, 0);
    const cycleIntervalMs = numberSetting(
      settings,
      "scrubber_interval_ms",
      DEFAULT_CYCLE_INTERVAL_MS,
      MIN_CYCLE_INTERVAL_MS
    );

    let cancelled = false;
    let attempts = 0;
    let detach = null;
    const cuesPromise = fetchCues(vttUrl);

    const tryAttach = () => {
      if (cancelled) return;
      const playerEl = findPlayerElement();
      if (!playerEl) {
        if (attempts++ < PLAYER_ATTACH_MAX_ATTEMPTS) {
          setTimeout(tryAttach, PLAYER_ATTACH_RETRY_MS);
        }
        return;
      }
      detach = attachScrubber(playerEl, cuesPromise, {
        coverEnabled,
        cycleIntervalMs,
        hoverDelayMs,
        seekEnabled,
      });
    };
    tryAttach();

    return () => {
      cancelled = true;
      detach?.();
    };
  }

  function ScenePlayerScrubber({ scene }) {
    const settings = usePluginSettings();

    React.useEffect(() => {
      if (!scene?.id || !scene?.paths?.vtt) return undefined;
      return setupScrubberForScene(scene, settings);
      // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [scene?.id, scene?.paths?.vtt, settings]);

    return null;
  }

  window.PluginApi.patch.after("ScenePage", function (...args) {
    const props = args[0];
    const rendered = args[args.length - 1];
    if (!props?.scene?.id) return rendered;
    return React.createElement(
      React.Fragment,
      null,
      rendered,
      React.createElement(ScenePlayerScrubber, {
        key: "javbeacon-scrubber",
        scene: props.scene,
      })
    );
  });
})();
