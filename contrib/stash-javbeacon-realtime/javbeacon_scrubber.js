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

  // ---- Two independently verified sources for a preview frame -----------
  //
  // Seek-bar hover: Stash already renders a small, correctly-cropped preview
  // into a thumbnail element (confirmed live: ".vjs-vtt-thumbnail-display")
  // whenever the pointer moves over the seek bar. This plugin hides that
  // small element with CSS and mirrors its computed background image,
  // position and size onto a large overlay instead, scaled up
  // proportionally, so the crop is always identical to what Stash itself
  // computed for a real hover.
  //
  // Cover-area auto-cycle: this cannot reuse the mechanism above, because it
  // has to run without a real pointer continuously moving over the seek bar.
  // Dispatching synthetic mousemove events to drive Stash's own listener was
  // tried and confirmed live NOT to work - browsers do not let page script
  // create "trusted" input events, and libraries like this commonly ignore
  // untrusted ones. So the cover-area cycle instead fetches and parses the
  // scene's own sprite VTT file (scene.paths.vtt) once, and paints each cue
  // using the same "measure the sprite's natural size and scale everything
  // together" technique as the mirror above - confirmed live to produce a
  // single, correctly-cropped frame with no ghosting or overlap.

  const PX_RE = /(-?\d+(?:\.\d+)?)px/;

  function extractPx(value) {
    const match = PX_RE.exec(String(value == null ? "" : value));
    return match ? Number(match[1]) : null;
  }

  function extractUrl(backgroundImage) {
    const match = /url\((['"]?)(.*?)\1\)/.exec(String(backgroundImage || ""));
    return match ? match[2] : null;
  }

  // Confirmed live against a running Stash instance: the thumbnail element
  // never sets an explicit background-size (it resolves to the CSS-wide
  // keyword "initial", i.e. the image renders at its own natural pixel
  // size), and background-position is the real negative pixel offset into
  // that naturally-sized sprite sheet. Scaling position without also
  // scaling the image itself would point at the right offset in the wrong
  // (unscaled) image, cropping the wrong area - so when no explicit
  // background-size is present, the sprite's natural size is measured
  // directly and scaled by the same factor as the position.
  const naturalSizeCache = new Map();

  function loadNaturalSize(url) {
    if (naturalSizeCache.has(url)) return naturalSizeCache.get(url);
    const promise = new Promise((resolve) => {
      const img = new Image();
      img.onload = () => resolve({ width: img.naturalWidth, height: img.naturalHeight });
      img.onerror = () => resolve(null);
      img.src = url;
    });
    naturalSizeCache.set(url, promise);
    return promise;
  }

  // Fits a contentW x contentH rectangle inside box the way CSS
  // "background-size: contain" would - scaled up as far as possible without
  // exceeding either dimension, then centered. Confirmed live to be
  // necessary: an earlier revision stretched the overlay to fill the whole
  // box while only painting a scaled image sized by the SMALLER of the two
  // axis ratios, so the leftover space on the larger axis kept showing
  // whatever sprite content sits past the edge of the intended cell -
  // visible as a second, wrong frame bleeding in from the row below.
  // Sizing the overlay itself to the scaled content, instead of stretching
  // it to fill an arbitrarily-shaped box, removes that leftover space
  // entirely.
  function containRect(box, contentW, contentH) {
    if (!box || box.width <= 0 || box.height <= 0 || !contentW || !contentH) return null;
    const scale = Math.min(box.width / contentW, box.height / contentH);
    if (!Number.isFinite(scale) || scale <= 0) return null;
    const width = contentW * scale;
    const height = contentH * scale;
    return {
      left: box.left + (box.width - width) / 2,
      top: box.top + (box.height - height) / 2,
      width,
      height,
      scale,
    };
  }

  // Pure aside from writing to targetEl.style and (when the source has no
  // explicit background-size) loading the sprite image to measure it. Takes
  // plain {style, getBoundingClientRect?} shaped objects so it can run
  // against a real DOM element or a fake one in a Node test. Resolves false
  // (leaving targetEl untouched) whenever sourceEl currently has no
  // thumbnail painted, so callers can decide whether to keep showing the
  // previous frame. Positions and sizes targetEl itself to the letterboxed
  // fit within box (see containRect above), rather than stretching it to
  // fill box - the caller does not need to call positionOverlay separately.
  async function mirrorBackground(sourceEl, targetEl, box) {
    if (!sourceEl || !targetEl || !box || box.width <= 0 || box.height <= 0) {
      return false;
    }
    const style = sourceEl.style || {};
    const image = style.backgroundImage;
    if (!image) return false;

    const rect =
      typeof sourceEl.getBoundingClientRect === "function"
        ? sourceEl.getBoundingClientRect()
        : null;
    const baseWidth = (rect && rect.width) || extractPx(style.width);
    const baseHeight = (rect && rect.height) || extractPx(style.height);
    if (!baseWidth || !baseHeight) return false;

    const fit = containRect(box, baseWidth, baseHeight);
    if (!fit) return false;

    const positionParts = String(style.backgroundPosition || "0px 0px").split(/\s+/);
    const posX = extractPx(positionParts[0]) ?? 0;
    const posY = extractPx(positionParts[1]) ?? 0;

    const sizeParts = String(style.backgroundSize || "").split(/\s+/);
    let sizeW = extractPx(sizeParts[0]);
    let sizeH = extractPx(sizeParts[1]);
    if (sizeW == null || sizeH == null) {
      const url = extractUrl(image);
      const natural = url ? await loadNaturalSize(url) : null;
      if (!natural) return false;
      sizeW = natural.width;
      sizeH = natural.height;
    }

    targetEl.style.left = `${fit.left.toFixed(2)}px`;
    targetEl.style.top = `${fit.top.toFixed(2)}px`;
    targetEl.style.width = `${fit.width.toFixed(2)}px`;
    targetEl.style.height = `${fit.height.toFixed(2)}px`;
    targetEl.style.backgroundImage = image;
    targetEl.style.backgroundRepeat = "no-repeat";
    targetEl.style.backgroundPosition = `${(posX * fit.scale).toFixed(2)}px ${(posY * fit.scale).toFixed(2)}px`;
    targetEl.style.backgroundSize = `${(sizeW * fit.scale).toFixed(2)}px ${(sizeH * fit.scale).toFixed(2)}px`;
    return true;
  }

  // ---- Sprite VTT parsing for the cover-area cycle -----------------------
  //
  // Confirmed live against a running Stash instance - a WEBVTT file whose
  // cues are plain "start --> end" ranges each followed by one line of
  // "<sprite-filename>#xywh=x,y,w,h" (the standard media-fragment
  // convention), e.g.:
  //   00:00:00.000 --> 00:01:57.484
  //   67ef3d000f0466e2_sprite.jpg#xywh=0,0,640,360
  // Pure/DOM-free so it can run under a plain Node test.

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
    if (hashIndex < 0) return null;
    const path = trimmed.slice(0, hashIndex);
    let url = path;
    try {
      url = new URL(path, baseUrl || undefined).href;
    } catch (_) {
      // Relative path with no usable base: fall back to the raw string.
    }
    const parts = trimmed
      .slice(hashIndex + 6)
      .split(",")
      .map((part) => Number(part.trim()));
    if (parts.length !== 4 || parts.some((n) => !Number.isFinite(n))) return null;
    const [x, y, w, h] = parts;
    if (w <= 0 || h <= 0) return null;
    return { url, x, y, w, h };
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

  function fetchSpriteCues(vttUrl) {
    return fetch(vttUrl, { credentials: "same-origin" })
      .then((response) => (response.ok ? response.text() : Promise.reject(new Error("sprite VTT request failed"))))
      .then((text) => parseSpriteVtt(text, vttUrl))
      .catch(() => []);
  }

  // Paints one VTT cue's crop into targetEl, using the same "measure the
  // sprite's natural size, scale position and size together" technique as
  // mirrorBackground above - confirmed live to reproduce a single, correctly
  // cropped frame with no ghosting. Positions and sizes targetEl itself to
  // the letterboxed fit within box (see containRect above) rather than
  // stretching it to fill box, which is what let the next sprite row bleed
  // into view below the intended frame.
  async function paintCue(targetEl, cue, box) {
    if (!targetEl || !cue || !box || box.width <= 0 || box.height <= 0) return false;
    const fit = containRect(box, cue.w, cue.h);
    if (!fit) return false;
    const natural = await loadNaturalSize(cue.url);
    if (!natural) return false;
    targetEl.style.left = `${fit.left.toFixed(2)}px`;
    targetEl.style.top = `${fit.top.toFixed(2)}px`;
    targetEl.style.width = `${fit.width.toFixed(2)}px`;
    targetEl.style.height = `${fit.height.toFixed(2)}px`;
    targetEl.style.backgroundImage = `url("${cue.url}")`;
    targetEl.style.backgroundRepeat = "no-repeat";
    targetEl.style.backgroundPosition = `-${(cue.x * fit.scale).toFixed(2)}px -${(cue.y * fit.scale).toFixed(2)}px`;
    targetEl.style.backgroundSize = `${(natural.width * fit.scale).toFixed(2)}px ${(natural.height * fit.scale).toFixed(2)}px`;
    return true;
  }

  // Exposed for the Node-based unit test harness only; production code paths
  // never read this. Mirrors how the rest of this plugin is tested by
  // requiring the browser file against a faked `window`.
  window.__javbeaconScrubberInternals = {
    extractPx,
    mirrorBackground,
    parseVttTimestamp,
    parseCueImageLine,
    parseSpriteVtt,
    paintCue,
    containRect,
  };

  // ---- DOM wiring --------------------------------------------------------
  //
  // The overlay is appended to document.body, never into .video-js or any
  // other Stash/video.js-owned element, and every interaction with the
  // player element itself is read-only (querySelector, getBoundingClientRect,
  // classList.contains). Two earlier versions of this plugin instead
  // appended the overlay as a child of .video-js and, in one revision, wrote
  // to its inline style - both broke the entire player. video.js and/or
  // Stash's own React wrapper around it manage that element's DOM directly;
  // an externally added child or mutated style can conflict with that
  // ownership in ways that are very hard to predict without the actual
  // running app to test against. Positioning a fully independent, fixed
  // overlay on top of the player's on-screen rect avoids touching that
  // ownership at all.

  function createOverlay() {
    const overlay = document.createElement("div");
    overlay.className = "javbeacon-scrub-overlay";
    overlay.setAttribute("aria-hidden", "true");
    document.body.appendChild(overlay);
    return overlay;
  }

  function positionOverlay(overlay, rect) {
    if (!rect || rect.width <= 0 || rect.height <= 0) return;
    overlay.style.left = `${rect.left}px`;
    overlay.style.top = `${rect.top}px`;
    overlay.style.width = `${rect.width}px`;
    overlay.style.height = `${rect.height}px`;
  }

  window.__javbeaconScrubberInternals.positionOverlay = positionOverlay;

  function showOverlay(overlay) {
    overlay.classList.add("is-visible");
  }

  function hideOverlay(overlay) {
    overlay.classList.remove("is-visible");
  }

  // Confirmed live against a running Stash instance: the player root itself
  // carries a "vjs-vtt-thumbnails" feature-flag class, and the actual
  // preview element is "vjs-vtt-thumbnail-display". Do not broaden this to a
  // substring match - "vtt-thumbnail" is itself a substring of the player
  // root's own "vjs-vtt-thumbnails" class, so a `[class*="vtt-thumbnail"]`
  // fallback (used in an earlier revision, in the CSS that hides this
  // element) matched the player root and hid the entire player.
  function findThumbnailElement(playerEl) {
    return playerEl.querySelector(".vjs-vtt-thumbnail-display");
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

  // Confirmed live: the control bar is absolutely positioned over the
  // bottom of the video, not below it, so playerEl's own rect (or the
  // poster's, which matches it exactly) includes the strip the control bar
  // sits on. An earlier revision used that full rect as the preview box,
  // so the overlay's very high z-index painted over the control bar,
  // hiding the seek position the user needs to see while scrubbing. This
  // returns the player rect with that bottom strip subtracted, read-only
  // (getBoundingClientRect only, never mutating the control bar).
  function safeVideoBox(playerEl) {
    const rect = playerEl.getBoundingClientRect();
    const controlBar = playerEl.querySelector(".vjs-control-bar");
    const controlRect = controlBar ? controlBar.getBoundingClientRect() : null;
    if (controlRect && controlRect.height > 0 && controlRect.top > rect.top) {
      return {
        left: rect.left,
        top: rect.top,
        width: rect.width,
        height: Math.max(0, controlRect.top - rect.top),
      };
    }
    return { left: rect.left, top: rect.top, width: rect.width, height: rect.height };
  }

  function attachScrubber(playerEl, cuesPromise, options) {
    const { hoverDelayMs, cycleIntervalMs, coverEnabled, seekEnabled } = options;
    const overlay = createOverlay();
    const poster = playerEl.querySelector(".vjs-poster");
    const progress = playerEl.querySelector(".vjs-progress-control");
    let cues = null;
    cuesPromise.then((resolved) => {
      cues = resolved;
    });

    function playerHasStarted() {
      return playerEl.classList.contains("vjs-has-started");
    }

    function coverBox() {
      return safeVideoBox(playerEl);
    }

    function mirrorFromThumbnail(box) {
      const thumbnail = findThumbnailElement(playerEl);
      if (thumbnail) mirrorBackground(thumbnail, overlay, box);
    }

    let hoverTimer = null;
    let cycleTimer = null;

    function stopCycle() {
      clearTimeout(hoverTimer);
      hoverTimer = null;
      clearInterval(cycleTimer);
      cycleTimer = null;
    }

    function onPosterEnter() {
      if (!coverEnabled || playerHasStarted() || !cues || cues.length === 0) return;
      stopCycle();
      hoverTimer = setTimeout(() => {
        let index = 0;
        showOverlay(overlay);
        const step = () => {
          paintCue(overlay, cues[index], coverBox());
          index = (index + 1) % cues.length;
        };
        step();
        cycleTimer = setInterval(step, cycleIntervalMs);
      }, hoverDelayMs);
    }

    function onPosterLeave() {
      stopCycle();
      hideOverlay(overlay);
    }

    function onSeekMove() {
      if (!seekEnabled) return;
      stopCycle();
      showOverlay(overlay);
      const box = safeVideoBox(playerEl);
      requestAnimationFrame(() => mirrorFromThumbnail(box));
    }

    function onSeekLeave() {
      hideOverlay(overlay);
    }

    poster?.addEventListener("mouseenter", onPosterEnter);
    poster?.addEventListener("mouseleave", onPosterLeave);
    progress?.addEventListener("mousemove", onSeekMove);
    progress?.addEventListener("mouseleave", onSeekLeave);

    return function detach() {
      stopCycle();
      poster?.removeEventListener("mouseenter", onPosterEnter);
      poster?.removeEventListener("mouseleave", onPosterLeave);
      progress?.removeEventListener("mousemove", onSeekMove);
      progress?.removeEventListener("mouseleave", onSeekLeave);
      overlay.remove();
    };
  }

  function setupScrubberForScene(scene, settings) {
    const coverEnabled = boolSetting(settings, "scrubber_cover_enabled", true);
    const seekEnabled = boolSetting(settings, "scrubber_seek_preview_enabled", true);
    if (!coverEnabled && !seekEnabled) return () => {};

    const hoverDelayMs = numberSetting(settings, "scrubber_hover_delay_ms", DEFAULT_HOVER_DELAY_MS, 0);
    const cycleIntervalMs = numberSetting(
      settings,
      "scrubber_interval_ms",
      DEFAULT_CYCLE_INTERVAL_MS,
      MIN_CYCLE_INTERVAL_MS
    );

    // Only the cover-area cycle needs the parsed sprite VTT (the seek-bar
    // mirror reads Stash's own already-computed thumbnail instead), but
    // fetching it is harmless when only seek-preview is enabled - it just
    // goes unused.
    const cuesPromise = coverEnabled && scene?.paths?.vtt
      ? fetchSpriteCues(scene.paths.vtt)
      : Promise.resolve([]);

    let cancelled = false;
    let attempts = 0;
    let detach = null;

    const tryAttach = () => {
      if (cancelled) return;
      const playerEl = findPlayerElement();
      if (!playerEl) {
        if (attempts++ < PLAYER_ATTACH_MAX_ATTEMPTS) {
          setTimeout(tryAttach, PLAYER_ATTACH_RETRY_MS);
        }
        return;
      }
      detach = attachScrubber(playerEl, cuesPromise, { coverEnabled, cycleIntervalMs, hoverDelayMs, seekEnabled });
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
      if (!scene?.id) return undefined;
      return setupScrubberForScene(scene, settings);
      // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [scene?.id, settings]);

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
