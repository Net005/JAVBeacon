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

  // Computes the "background-size: contain" fit of a contentW x contentH
  // rectangle inside box - scaled up as far as possible without exceeding
  // either dimension, then centered.
  //
  // Confirmed live, twice, that a single element cannot satisfy both of
  // this overlay's requirements at once:
  //   1. It must cover the *entire* safe video box, opaquely - the native
  //      <video> element carries its own "poster" HTML attribute, rendered
  //      by the browser independently of Stash's .vjs-poster div (hiding
  //      that div with CSS never touches it), so any uncovered margin
  //      exposes it.
  //   2. Its background-image "window" must be exactly the scaled cue
  //      size - CSS background-position/background-size only place and
  //      scale the (much larger) sprite sheet; they do not clip it to the
  //      intended cell. If the element's own box is taller or wider than
  //      the scaled cue, the extra viewport space is NOT filled with
  //      background-color - it reveals real pixels from the adjacent
  //      sprite row/column, because the positioned image still extends
  //      underneath. That is what "a second frame appears below/above the
  //      intended one" actually was, confirmed by cropping the exact same
  //      cue with a <canvas> for comparison: the canvas crop was always
  //      clean, proving the sprite math itself was correct and the bleed
  //      was purely from the element's viewport being larger than the cue.
  //
  // Solved with two layered elements instead of one: an opaque backdrop
  // sized to the full box (no background-image, just background-color),
  // and a child "frame" element sized to EXACTLY the scaled cue dimensions
  // (contentW*scale x contentH*scale), positioned absolutely within the
  // backdrop at the centering margin. The frame's own box being exactly
  // the cue size makes bleed impossible (no leftover viewport space to
  // reveal adjacent content), and the backdrop being opaque and full-size
  // makes exposure impossible (nothing behind it can show through).
  function containFit(box, contentW, contentH) {
    if (!box || box.width <= 0 || box.height <= 0 || !contentW || !contentH) return null;
    const scale = Math.min(box.width / contentW, box.height / contentH);
    if (!Number.isFinite(scale) || scale <= 0) return null;
    const width = contentW * scale;
    const height = contentH * scale;
    return {
      marginX: (box.width - width) / 2,
      marginY: (box.height - height) / 2,
      width,
      height,
      scale,
    };
  }

  // Sizes backdropEl to the full box (opaque - see containFit above) and
  // frameEl to the exact scaled fit, positioned absolutely within it.
  function positionBackdropAndFrame(backdropEl, frameEl, box, fit) {
    backdropEl.style.left = `${box.left.toFixed(2)}px`;
    backdropEl.style.top = `${box.top.toFixed(2)}px`;
    backdropEl.style.width = `${box.width.toFixed(2)}px`;
    backdropEl.style.height = `${box.height.toFixed(2)}px`;
    frameEl.style.left = `${fit.marginX.toFixed(2)}px`;
    frameEl.style.top = `${fit.marginY.toFixed(2)}px`;
    frameEl.style.width = `${fit.width.toFixed(2)}px`;
    frameEl.style.height = `${fit.height.toFixed(2)}px`;
  }

  // Pure aside from writing to element styles and (when the source has no
  // explicit background-size) loading the sprite image to measure it. Takes
  // plain {style, getBoundingClientRect?} shaped objects so it can run
  // against a real DOM element or a fake one in a Node test. Resolves false
  // (leaving the elements untouched) whenever sourceEl currently has no
  // thumbnail painted, so callers can decide whether to keep showing the
  // previous frame.
  async function mirrorBackground(sourceEl, backdropEl, frameEl, box) {
    if (!sourceEl || !backdropEl || !frameEl || !box || box.width <= 0 || box.height <= 0) {
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

    const fit = containFit(box, baseWidth, baseHeight);
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

    positionBackdropAndFrame(backdropEl, frameEl, box, fit);
    frameEl.style.backgroundImage = image;
    frameEl.style.backgroundRepeat = "no-repeat";
    frameEl.style.backgroundPosition = `${(posX * fit.scale).toFixed(2)}px ${(posY * fit.scale).toFixed(2)}px`;
    frameEl.style.backgroundSize = `${(sizeW * fit.scale).toFixed(2)}px ${(sizeH * fit.scale).toFixed(2)}px`;
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

  // Paints one VTT cue's crop, using the same "measure the sprite's natural
  // size, scale position and size together" technique as mirrorBackground
  // above. Sizes backdropEl to the full box (opaque) and frameEl to exactly
  // the scaled cue dimensions (see containFit above) - frameEl's own box
  // being exactly the cue size is what makes bleed from an adjacent sprite
  // row/column impossible, confirmed live by comparing against a <canvas>
  // crop of the identical cue.
  async function paintCue(backdropEl, frameEl, cue, box) {
    if (!backdropEl || !frameEl || !cue || !box || box.width <= 0 || box.height <= 0) return false;
    const fit = containFit(box, cue.w, cue.h);
    if (!fit) return false;
    const natural = await loadNaturalSize(cue.url);
    if (!natural) return false;
    positionBackdropAndFrame(backdropEl, frameEl, box, fit);
    frameEl.style.backgroundImage = `url("${cue.url}")`;
    frameEl.style.backgroundRepeat = "no-repeat";
    frameEl.style.backgroundPosition = `-${(cue.x * fit.scale).toFixed(2)}px -${(cue.y * fit.scale).toFixed(2)}px`;
    frameEl.style.backgroundSize = `${(natural.width * fit.scale).toFixed(2)}px ${(natural.height * fit.scale).toFixed(2)}px`;
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
    containFit,
  };

  // ---- DOM wiring --------------------------------------------------------
  //
  // Two elements, not one: backdropEl (opaque, sized to the full player box)
  // blocks the native <video poster> from showing through, and frameEl (a
  // child of backdropEl, sized to exactly the scaled cue dimensions) paints
  // the actual crop with no room for an adjacent sprite row/column to bleed
  // in. See containFit's comment above for why a single element cannot
  // satisfy both requirements.
  //
  // backdropEl is inserted as a child of playerEl itself now - specifically
  // via insertBefore(backdrop, controlBar), never appendChild and never
  // touching playerEl's own style or any of its EXISTING children. Two
  // earlier versions of this plugin broke the entire player by mutating
  // .video-js more invasively (appending as an uncontrolled additional
  // child with no defined position, and separately writing to .video-js's
  // own inline style), which is why every version since kept the overlay
  // entirely outside the player, positioned with `position: fixed` and
  // copied coordinates. That approach turned out to have its own
  // unavoidable flaw, confirmed live: an ancestor of .video-js establishes
  // its own stacking context (an explicit z-index on a wrapper several
  // levels up), so an element living outside that ancestor - like an
  // overlay on document.body - can only ever render entirely above or
  // entirely below the WHOLE player at once. There is no z-index that
  // sandwiches it between the video and the control bar, which is exactly
  // where it needs to be: above the native poster/video, but below the
  // control bar and seek bar so those stay visible and usable. The only
  // way to achieve that layering is to become a sibling within the
  // player's own stacking context, positioned earlier in DOM order than
  // the control bar. Confirmed live, including through play/pause and
  // repeated hover cycles, that inserting one single, inert
  // (pointer-events: none), never-removed-and-recreated-except-by-us
  // element this way does not disturb video.js's own children or its
  // control bar's functionality.
  function createOverlay(playerEl) {
    const backdrop = document.createElement("div");
    backdrop.className = "javbeacon-scrub-overlay";
    backdrop.setAttribute("aria-hidden", "true");
    const frame = document.createElement("div");
    frame.className = "javbeacon-scrub-frame";
    backdrop.appendChild(frame);
    const controlBar = playerEl.querySelector(".vjs-control-bar");
    if (controlBar) {
      playerEl.insertBefore(backdrop, controlBar);
    } else {
      playerEl.appendChild(backdrop);
    }
    return { backdrop, frame };
  }

  // backdropEl is positioned absolutely within playerEl's own box (playerEl
  // itself is position: absolute, so it is the containing block), so the
  // box passed to mirrorBackground/paintCue is simply playerEl's own local
  // dimensions - no getBoundingClientRect or control-bar-height math
  // needed, since backdropEl sitting behind the control bar in DOM order
  // (see createOverlay above) is what keeps the control bar and seek bar
  // visible now, not the box excluding their area.
  function playerBox(playerEl) {
    return { left: 0, top: 0, width: playerEl.clientWidth, height: playerEl.clientHeight };
  }

  // Hides the underlying static poster/cover image while the overlay shows
  // a scrubbed frame, via a body-level class the CSS keys off (see
  // javbeacon_scrubber.css) rather than writing directly to the
  // player-owned .vjs-poster element's own style or classList.
  function showOverlay(backdrop) {
    backdrop.classList.add("is-visible");
    document.body.classList.add("javbeacon-scrubbing");
  }

  function hideOverlay(backdrop) {
    backdrop.classList.remove("is-visible");
    document.body.classList.remove("javbeacon-scrubbing");
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

  function attachScrubber(playerEl, cuesPromise, options) {
    const { hoverDelayMs, cycleIntervalMs, coverEnabled, seekEnabled } = options;
    const { backdrop, frame } = createOverlay(playerEl);
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
      return playerBox(playerEl);
    }

    function mirrorFromThumbnail(box) {
      const thumbnail = findThumbnailElement(playerEl);
      if (thumbnail) mirrorBackground(thumbnail, backdrop, frame, box);
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
        showOverlay(backdrop);
        const step = () => {
          paintCue(backdrop, frame, cues[index], coverBox());
          index = (index + 1) % cues.length;
        };
        step();
        cycleTimer = setInterval(step, cycleIntervalMs);
      }, hoverDelayMs);
    }

    function onPosterLeave() {
      stopCycle();
      hideOverlay(backdrop);
    }

    function onSeekMove() {
      if (!seekEnabled) return;
      stopCycle();
      showOverlay(backdrop);
      const box = playerBox(playerEl);
      requestAnimationFrame(() => mirrorFromThumbnail(box));
    }

    function onSeekLeave() {
      hideOverlay(backdrop);
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
      document.body.classList.remove("javbeacon-scrubbing");
      backdrop.remove();
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
