(function () {
  "use strict";

  const PLUGIN_ID = "javbeacon-realtime";
  const React = window.PluginApi.React;
  const { gql, useQuery } = window.PluginApi.libraries.Apollo;

  const DEFAULT_HOVER_DELAY_MS = 400;
  const DEFAULT_CYCLE_INTERVAL_MS = 700;
  const MIN_CYCLE_INTERVAL_MS = 100;
  // How many evenly-spaced points across the seek bar the cover-area
  // auto-cycle walks through. Not user-configurable: it only controls how
  // finely the cycle samples the video, not the requested timings.
  const CYCLE_STEPS = 24;
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

  // ---- Mirroring Stash's own seek-bar thumbnail ------------------------
  //
  // Earlier versions of this plugin fetched and parsed the scene's sprite
  // VTT file itself, then computed its own crop math to paint a frame. That
  // duplicated logic Stash (via the standard videojs-vtt-thumbnails style
  // plugin) already implements correctly, and getting the crop math wrong
  // produced visible artifacts (overlapping/ghosted frames) instead of a
  // single clean crop.
  //
  // Stash already renders a small, correctly-cropped preview into a
  // thumbnail element (conventionally `.vjs-vtt-thumbnail-display`) whenever
  // the pointer moves over the seek bar; this plugin hides that small
  // element with CSS and instead mirrors its computed background image,
  // position and size onto a large overlay covering the cover/video area,
  // scaled up proportionally. This guarantees the crop is always identical
  // to what Stash itself computed - correctness comes from reusing Stash's
  // own result rather than re-deriving it - and the only resolution ceiling
  // left is the sprite sheet's own source resolution, which scaling cannot
  // improve.
  //
  // The cover-area "hover to preview" cycle reuses the exact same mirroring:
  // it dispatches synthetic mousemove events at evenly-spaced points along
  // the seek bar, which makes Stash's own listener update the same
  // thumbnail element, and mirrors the result each time. This is why the
  // cover-area preview only requires this element and the seek bar to exist;
  // it never touches the sprite/VTT data directly.

  const PX_RE = /(-?\d+(?:\.\d+)?)px/;

  function extractPx(value) {
    const match = PX_RE.exec(String(value == null ? "" : value));
    return match ? Number(match[1]) : null;
  }

  // Pure aside from writing to targetEl.style: takes plain {style,
  // getBoundingClientRect?} shaped objects so it can run against a real DOM
  // element or a fake one in a Node test. Returns false (leaving targetEl
  // untouched) whenever sourceEl currently has no thumbnail painted, so
  // callers can decide whether to keep showing the previous frame.
  function mirrorBackground(sourceEl, targetEl, box) {
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

    const scale = Math.min(box.width / baseWidth, box.height / baseHeight);
    if (!Number.isFinite(scale) || scale <= 0) return false;

    const positionParts = String(style.backgroundPosition || "0px 0px").split(/\s+/);
    const posX = extractPx(positionParts[0]) ?? 0;
    const posY = extractPx(positionParts[1]) ?? 0;
    const sizeParts = String(style.backgroundSize || "").split(/\s+/);
    const sizeW = extractPx(sizeParts[0]);
    const sizeH = extractPx(sizeParts[1]);

    targetEl.style.backgroundImage = image;
    targetEl.style.backgroundRepeat = "no-repeat";
    targetEl.style.backgroundPosition = `${(posX * scale).toFixed(2)}px ${(posY * scale).toFixed(2)}px`;
    targetEl.style.backgroundSize =
      sizeW != null && sizeH != null
        ? `${(sizeW * scale).toFixed(2)}px ${(sizeH * scale).toFixed(2)}px`
        : style.backgroundSize || "auto";
    return true;
  }

  // Exposed for the Node-based unit test harness only; production code paths
  // never read this. Mirrors how the rest of this plugin is tested by
  // requiring the browser file against a faked `window`.
  window.__javbeaconScrubberInternals = { extractPx, mirrorBackground };

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

  function findThumbnailElement(playerEl) {
    const known = playerEl.querySelector(".vjs-vtt-thumbnail-display");
    if (known) return known;
    // Defensive fallback in case Stash's build uses a differently-named
    // class for the same feature; matches anything containing
    // "vtt-thumbnail" rather than requiring the exact conventional name.
    return playerEl.querySelector("[class*='vtt-thumbnail']");
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

  function attachScrubber(playerEl, options) {
    const { hoverDelayMs, cycleIntervalMs, coverEnabled, seekEnabled } = options;
    const overlay = createOverlay();
    const poster = playerEl.querySelector(".vjs-poster");
    const progress = playerEl.querySelector(".vjs-progress-control");

    function playerHasStarted() {
      return playerEl.classList.contains("vjs-has-started");
    }

    function coverBox() {
      const box = poster || playerEl;
      return box.getBoundingClientRect();
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

    function dispatchSyntheticHover(ratio) {
      if (!progress) return;
      const rect = progress.getBoundingClientRect();
      if (rect.width <= 0) return;
      progress.dispatchEvent(
        new MouseEvent("mousemove", {
          bubbles: true,
          cancelable: true,
          clientX: rect.left + rect.width * ratio,
          clientY: rect.top + rect.height / 2,
        })
      );
    }

    function onPosterEnter() {
      if (!coverEnabled || !progress || playerHasStarted()) return;
      stopCycle();
      hoverTimer = setTimeout(() => {
        let index = 0;
        showOverlay(overlay);
        const step = () => {
          const ratio = (index % CYCLE_STEPS) / CYCLE_STEPS;
          index++;
          dispatchSyntheticHover(ratio);
          const box = coverBox();
          positionOverlay(overlay, box);
          // requestAnimationFrame runs after the current synchronous event
          // dispatch (including Stash's own mousemove listener) finishes,
          // regardless of listener registration order, so the thumbnail
          // element is guaranteed to already reflect this ratio's frame.
          requestAnimationFrame(() => mirrorFromThumbnail(box));
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
      const box = playerEl.getBoundingClientRect();
      positionOverlay(overlay, box);
      showOverlay(overlay);
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
      detach = attachScrubber(playerEl, { coverEnabled, cycleIntervalMs, hoverDelayMs, seekEnabled });
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
