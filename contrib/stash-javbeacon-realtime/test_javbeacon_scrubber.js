"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");

const afterPatches = {};
const React = {
  Fragment: Symbol("Fragment"),
  useState(initial) {
    return [initial, () => {}];
  },
  useEffect() {},
  createElement(type, props, ...children) {
    return {
      type,
      props: {
        ...(props || {}),
        children: children.length === 1 ? children[0] : children,
      },
    };
  },
};

global.window = {
  PluginApi: {
    React,
    libraries: {
      Apollo: {
        gql(strings) {
          return strings.join("");
        },
        useQuery() {
          return { data: null };
        },
      },
    },
    patch: {
      after(name, callback) {
        afterPatches[name] = callback;
      },
    },
  },
};

require("./javbeacon_scrubber.js");

const {
  extractPx,
  mirrorBackground,
  parseVttTimestamp,
  parseCueImageLine,
  parseSpriteVtt,
  paintCue,
  containFit,
} = window.__javbeaconScrubberInternals;

// Two earlier revisions of this plugin mutated the scene player element
// carelessly - once by appending an ad-hoc child, once by writing to its
// inline style - and both broke the scene player entirely. The current
// architecture DOES insert our own overlay as a child of playerEl
// (playerEl.insertBefore(backdrop, controlBar), confirmed live to be safe -
// see the comment above createOverlay for why), but nothing else about
// playerEl may ever be touched: no writes to its own style, and our
// insertion must only ever add our own backdrop node via insertBefore or, as
// a fallback, appendChild - never remove, replace, or reorder any existing
// child.
const pluginSource = fs.readFileSync(require.resolve("./javbeacon_scrubber.js"), "utf8");
assert.doesNotMatch(pluginSource, /playerEl\.style/, "must never write to the player element's style");
assert.doesNotMatch(pluginSource, /playerEl\.removeChild/, "must never remove an existing child of the player element");
assert.doesNotMatch(pluginSource, /playerEl\.replaceChild/, "must never replace an existing child of the player element");
assert.match(
  pluginSource,
  /playerEl\.insertBefore\(backdrop,\s*controlBar\)/,
  "the overlay backdrop must be inserted into the player element right before the control bar"
);
assert.match(
  pluginSource,
  /playerEl\.appendChild\(backdrop\)/,
  "must fall back to appending the backdrop to the player element when no control bar is found"
);

const cssSource = fs.readFileSync(require.resolve("./javbeacon_scrubber.css"), "utf8");
assert.match(
  cssSource,
  /\.javbeacon-scrub-overlay\s*\{[^}]*position:\s*absolute/,
  "the overlay must be position: absolute, positioned relative to the player it is now inserted into"
);

// This exact CSS selector match (a substring of .video-js's own
// "vjs-vtt-thumbnails" feature-flag class) was confirmed live to hide the
// entire scene player, and caused three consecutive "entirely broken" bug
// reports before being root-caused. Guard against it reappearing as an
// actual selector or querySelector call - both source files legitimately
// mention the string once, in a comment explaining the history, so strip
// comments before checking.
const jsWithoutComments = pluginSource.replace(/\/\/.*$/gm, "").replace(/\/\*[\s\S]*?\*\//g, "");
const cssWithoutComments = cssSource.replace(/\/\*[\s\S]*?\*\//g, "");
assert.doesNotMatch(jsWithoutComments, /\[class\*=["']vtt-thumbnail["']\]/, "must never use a substring selector to find the thumbnail element");
assert.doesNotMatch(cssWithoutComments, /\[class\*=["']vtt-thumbnail["']\]/, "must never use a substring selector in CSS for the thumbnail element");

// The overlay no longer needs to carve the control-bar strip out of its own
// box: since it is now a DOM child of playerEl inserted right before the
// control bar, it naturally paints below the control bar (and its seek bar)
// regardless of its own size, so both preview paths simply use the full
// player box. (An earlier revision computed a safeVideoBox that subtracted
// the control bar's height - that function no longer exists, and its
// removal is what fixed both the "seek bar hidden" and "native cover
// exposed through the seek bar's transparent hit-area" regressions.)
assert.doesNotMatch(pluginSource, /function safeVideoBox\(/, "safeVideoBox must not be reintroduced - DOM order now excludes the control bar, not box math");
assert.match(pluginSource, /function playerBox\(/, "must define playerBox to size the overlay to the full player element");
{
  const seekMoveBody = /function onSeekMove\(\) \{[\s\S]*?\n    \}/.exec(jsWithoutComments)?.[0] || "";
  const coverBoxBody = /function coverBox\(\) \{[\s\S]*?\n    \}/.exec(jsWithoutComments)?.[0] || "";
  assert.match(seekMoveBody, /playerBox\(playerEl\)/, "onSeekMove must derive its box from playerBox");
  assert.match(coverBoxBody, /playerBox\(playerEl\)/, "coverBox must derive its box from playerBox");
}

// The underlying static poster/cover must be hidden while the overlay shows
// a scrubbed frame, via a body-level class the CSS keys off, not by writing
// directly to the player-owned .vjs-poster element.
assert.match(
  jsWithoutComments,
  /document\.body\.classList\.add\(["']javbeacon-scrubbing["']\)/,
  "showOverlay must add the javbeacon-scrubbing body class"
);
assert.match(
  jsWithoutComments,
  /document\.body\.classList\.remove\(["']javbeacon-scrubbing["']\)/,
  "hideOverlay/detach must remove the javbeacon-scrubbing body class"
);
assert.match(
  cssWithoutComments,
  /\.javbeacon-scrubbing\s+\.vjs-poster\s*\{[^}]*opacity:\s*0/,
  "CSS must hide .vjs-poster while .javbeacon-scrubbing is set"
);

// Two elements, not one: the backdrop (opaque, full box) blocks the native
// <video poster> from showing through, and the frame (sized to exactly the
// scaled cue) makes bleed from an adjacent sprite row/column impossible.
// Confirmed live, twice: a single element sized to the full box cannot
// avoid revealing adjacent sprite content, because CSS background-position/
// background-size only place and scale the sprite sheet - they don't clip
// it to one cell, so any of the element's own viewport beyond the intended
// cell shows real (wrong) pixels, not empty space.
assert.match(pluginSource, /function createOverlay\(/, "must define createOverlay to build the backdrop+frame pair");
assert.match(pluginSource, /backdrop\.appendChild\(frame\)/, "the frame element must be a child of the backdrop");
assert.match(cssSource, /\.javbeacon-scrub-frame\s*\{/, "CSS must style the child frame element separately from the backdrop");

// extractPx pulls the numeric pixel value out of a CSS length, including
// negative offsets (background-position commonly uses these).
assert.equal(extractPx("160px"), 160);
assert.equal(extractPx("-320px"), -320);
assert.equal(extractPx("12.5px"), 12.5);
assert.equal(extractPx("auto"), null);
assert.equal(extractPx(""), null);
assert.equal(extractPx(undefined), null);

function fakeElement(style, rect) {
  return {
    style,
    getBoundingClientRect: rect ? () => rect : undefined,
  };
}

// Confirmed live against a running Stash instance: the thumbnail library
// never sets an explicit two-value background-size (style.backgroundSize
// resolves to "initial"), relying instead on the sprite image rendering at
// its own natural pixel size, with background-position as a real pixel
// offset into that natural-size image. global.Image here stands in for that
// natural-size lookup.
global.Image = class {
  set src(value) {
    this._src = value;
    const size = global.__fakeImageSizes?.[value];
    setTimeout(() => {
      if (size) {
        this.naturalWidth = size.width;
        this.naturalHeight = size.height;
        this.onload?.();
      } else {
        this.onerror?.();
      }
    }, 0);
  }
};

(async () => {
  // containFit computes the "background-size: contain" scale, centering
  // margins, and scaled content dimensions for a contentW x contentH image
  // inside box, without sizing anything itself. Callers use this to size a
  // BACKDROP element to the full box (opaque, blocking the native
  // <video poster> underneath) and a child FRAME element to exactly the
  // scaled content size (marginX/marginY as its position within the
  // backdrop) - confirmed live, twice, that a single element sized to the
  // full box cannot show a clean crop: CSS background-position/
  // background-size only place and scale the sprite sheet, they don't clip
  // it to one cell, so any of the element's own viewport beyond the
  // intended cell reveals real (wrong) pixels from the adjacent sprite row
  // or column instead of empty space.
  {
    // uniform box: no letterboxing needed, margins are zero
    assert.deepEqual(containFit({ left: 0, top: 0, width: 800, height: 450 }, 160, 90), {
      marginX: 0,
      marginY: 0,
      width: 800,
      height: 450,
      scale: 5,
    });
    // taller box than content aspect: letterboxed top/bottom, centered
    const fit = containFit({ left: 10, top: 20, width: 400, height: 400 }, 160, 90);
    assert.equal(fit.scale, 2.5); // width-limited: 400/160
    assert.equal(fit.width, 400);
    assert.equal(fit.height, 225);
    assert.equal(fit.marginX, 0);
    assert.equal(fit.marginY, (400 - 225) / 2);
    // degenerate inputs
    assert.equal(containFit(null, 10, 10), null);
    assert.equal(containFit({ left: 0, top: 0, width: 0, height: 0 }, 10, 10), null);
    assert.equal(containFit({ left: 0, top: 0, width: 10, height: 10 }, 0, 0), null);
  }

  // mirrorBackground scales a source element's background image, position
  // and size up, preserving the exact crop Stash's own thumbnail element
  // already computed - this is what replaces re-deriving the sprite crop
  // from scratch (the earlier approach that produced overlapping/ghosted
  // frames). It sizes backdropEl to the FULL box (opaque) and frameEl to
  // exactly the scaled content size, positioned at the centering margin
  // within backdropEl - see containFit above for why frameEl must never be
  // larger than the scaled content.
  {
    const source = fakeElement(
      {
        backgroundImage: 'url("https://stash.example.com/scene/1/vtt/sprite.jpg")',
        backgroundPosition: "-160px -90px",
        backgroundSize: "1920px 1080px",
      },
      { width: 160, height: 90 }
    );
    const backdrop = fakeElement({});
    const frame = fakeElement({});
    const ok = await mirrorBackground(source, backdrop, frame, { left: 0, top: 0, width: 800, height: 450 });
    assert.equal(ok, true);
    assert.equal(frame.style.backgroundImage, source.style.backgroundImage);
    assert.equal(frame.style.backgroundRepeat, "no-repeat");
    // backdrop always covers the full box
    assert.equal(backdrop.style.left, "0.00px");
    assert.equal(backdrop.style.top, "0.00px");
    assert.equal(backdrop.style.width, "800.00px");
    assert.equal(backdrop.style.height, "450.00px");
    // box aspect matches source aspect exactly, so frame fills it fully too
    assert.equal(frame.style.left, "0.00px");
    assert.equal(frame.style.top, "0.00px");
    assert.equal(frame.style.width, "800.00px");
    assert.equal(frame.style.height, "450.00px");
    // scale = 800 / 160 = 5
    assert.equal(frame.style.backgroundPosition, "-800.00px -450.00px");
    assert.equal(frame.style.backgroundSize, "9600.00px 5400.00px");
  }

  // Real-world case: no explicit background-size at all (style.backgroundSize
  // is "initial", as observed live), so the sprite's natural size must be
  // measured and scaled by the same factor as the position - scaling
  // position without also scaling an equally-sized image would point at the
  // right offset in the wrong (unscaled) image.
  {
    const url = "https://stash.bondt.network/scene/test_sprite.jpg";
    global.__fakeImageSizes = { [url]: { width: 5760, height: 3240 } };
    const source = fakeElement(
      {
        backgroundImage: `url("${url}")`,
        backgroundPosition: "-3840px -720px",
        backgroundSize: "initial",
        width: "640px",
        height: "360px",
      },
      { width: 640, height: 360 }
    );
    const backdrop = fakeElement({});
    const frame = fakeElement({});
    // box wider than the source box by 1.5x, same aspect ratio
    const ok = await mirrorBackground(source, backdrop, frame, { left: 0, top: 0, width: 960, height: 540 });
    assert.equal(ok, true);
    assert.equal(frame.style.backgroundPosition, "-5760.00px -1080.00px");
    assert.equal(frame.style.backgroundSize, "8640.00px 4860.00px");
  }

  // Regression case matching the live bug report exactly: box (the safe
  // video area, control bar already excluded) is taller than the seek-bar
  // thumbnail's 16:9 aspect ratio (905x760.5). The backdrop must cover the
  // FULL 905x760.5 box (opaque, so the native <video poster> underneath can
  // never show through the letterbox margin), while the frame is
  // letterboxed to exactly 905x509.06 within it (never larger - a taller
  // frame is exactly what let an adjacent sprite row bleed in).
  {
    const url = "https://stash.bondt.network/scene/regression_sprite.jpg";
    global.__fakeImageSizes = { ...global.__fakeImageSizes, [url]: { width: 5760, height: 3240 } };
    const source = fakeElement(
      {
        backgroundImage: `url("${url}")`,
        backgroundPosition: "-5120px -1080px",
        backgroundSize: "initial",
        width: "640px",
        height: "360px",
      },
      { width: 640, height: 360 }
    );
    const backdrop = fakeElement({});
    const frame = fakeElement({});
    const ok = await mirrorBackground(source, backdrop, frame, { left: 465, top: 55.75, width: 905, height: 760.5 });
    assert.equal(ok, true);
    // backdrop covers the entire box - no gap for the native poster
    assert.equal(backdrop.style.left, "465.00px");
    assert.equal(backdrop.style.top, "55.75px");
    assert.equal(backdrop.style.width, "905.00px");
    assert.equal(backdrop.style.height, "760.50px");
    // frame is letterboxed to exactly the scaled content, not the box -
    // it must never be taller than the cue it's cropping
    assert.equal(frame.style.left, "0.00px");
    assert.equal(frame.style.top, "125.72px");
    assert.equal(frame.style.width, "905.00px");
    assert.equal(frame.style.height, "509.06px");
    assert.equal(frame.style.backgroundPosition, "-7240.00px -1527.19px");
    assert.equal(frame.style.backgroundSize, "8145.00px 4581.56px");
  }

  // A source element with no thumbnail currently painted (no
  // background-image yet) must not overwrite whatever the frame was
  // already showing.
  {
    const source = fakeElement({ backgroundImage: "" }, { width: 160, height: 90 });
    const backdrop = fakeElement({});
    const frame = fakeElement({ backgroundImage: "url(previous.jpg)" });
    const ok = await mirrorBackground(source, backdrop, frame, { left: 0, top: 0, width: 800, height: 450 });
    assert.equal(ok, false);
    assert.equal(frame.style.backgroundImage, "url(previous.jpg)");
  }

  // A non-uniform target box scales by the smaller ratio; the frame is
  // centered within the backdrop via its own left/top (not the background
  // offset), so the crop never overflows either dimension and the frame
  // itself is never larger than the scaled content.
  {
    const source = fakeElement(
      {
        backgroundImage: "url(sprite.jpg)",
        backgroundPosition: "-100px -50px",
        backgroundSize: "1000px 500px",
      },
      { width: 100, height: 50 }
    );
    const backdrop = fakeElement({});
    const frame = fakeElement({});
    await mirrorBackground(source, backdrop, frame, { left: 0, top: 0, width: 400, height: 150 });
    // width ratio = 4, height ratio = 3 -> use 3; marginX = (400-300)/2 = 50
    assert.equal(frame.style.backgroundPosition, "-300.00px -150.00px");
    assert.equal(frame.style.width, "300.00px");
    assert.equal(frame.style.height, "150.00px");
    assert.equal(frame.style.left, "50.00px");
    assert.equal(frame.style.top, "0.00px");
    assert.equal(backdrop.style.width, "400.00px");
    assert.equal(backdrop.style.height, "150.00px");
  }

  // Falls back to reading the source element's own width/height style when
  // getBoundingClientRect is unavailable (defensive; real DOM elements
  // always provide it, but keeps this function usable in more constrained
  // contexts).
  {
    const source = fakeElement({
      backgroundImage: "url(sprite.jpg)",
      backgroundPosition: "-20px -10px",
      backgroundSize: "200px 100px",
      width: "20px",
      height: "10px",
    });
    const backdrop = fakeElement({});
    const frame = fakeElement({});
    const ok = await mirrorBackground(source, backdrop, frame, { left: 0, top: 0, width: 200, height: 100 });
    assert.equal(ok, true);
    assert.equal(frame.style.backgroundPosition, "-200.00px -100.00px");
  }

  assert.equal(await mirrorBackground(null, {}, {}, { left: 0, top: 0, width: 1, height: 1 }), false);
  assert.equal(
    await mirrorBackground(
      fakeElement({ backgroundImage: "url(a.jpg)" }, { width: 10, height: 10 }),
      {},
      {},
      null
    ),
    false
  );

  // parseVttTimestamp reads HH:MM:SS.mmm (confirmed live format).
  assert.equal(parseVttTimestamp("00:01:57.484"), 117.484);
  assert.equal(parseVttTimestamp("00:00:00.000"), 0);
  assert.equal(parseVttTimestamp("not a timestamp"), null);

  // parseCueImageLine reads the "<file>#xywh=x,y,w,h" media-fragment line
  // confirmed live, resolving the sprite filename against the VTT's own URL.
  {
    const cue = parseCueImageLine(
      "67ef3d000f0466e2_sprite.jpg#xywh=640,0,640,360",
      "https://stash.bondt.network/scene/1/vtt/sprite.vtt"
    );
    assert.deepEqual(cue, {
      url: "https://stash.bondt.network/scene/1/vtt/67ef3d000f0466e2_sprite.jpg",
      x: 640,
      y: 0,
      w: 640,
      h: 360,
    });
    assert.equal(parseCueImageLine("", "https://x/y.vtt"), null);
    assert.equal(parseCueImageLine("sprite.jpg", "https://x/y.vtt"), null);
    assert.equal(parseCueImageLine("sprite.jpg#xywh=0,0,0,0", "https://x/y.vtt"), null);
  }

  // parseSpriteVtt walks a real-shaped WEBVTT file (matching the confirmed
  // live example: standard cues, one image line each, sorted by start time).
  {
    const vttText = [
      "WEBVTT",
      "",
      "00:00:00.000 --> 00:01:57.484",
      "sprite.jpg#xywh=0,0,640,360",
      "",
      "00:01:57.484 --> 00:03:54.968",
      "sprite.jpg#xywh=640,0,640,360",
      "",
    ].join("\n");
    const cues = parseSpriteVtt(vttText, "https://stash.bondt.network/scene/1/vtt/sprite.vtt");
    assert.equal(cues.length, 2);
    assert.equal(cues[0].start, 0);
    assert.equal(cues[0].x, 0);
    assert.equal(cues[1].start, 117.484);
    assert.equal(cues[1].x, 640);
    // out of order input is sorted by start time
    const reversed = parseSpriteVtt(
      [
        "00:01:57.484 --> 00:03:54.968",
        "sprite.jpg#xywh=640,0,640,360",
        "",
        "00:00:00.000 --> 00:01:57.484",
        "sprite.jpg#xywh=0,0,640,360",
      ].join("\n"),
      "https://x/sprite.vtt"
    );
    assert.equal(reversed[0].start, 0);
    assert.equal(reversed[1].start, 117.484);
  }

  // paintCue measures the sprite's natural size (same technique validated
  // for mirrorBackground) and crops+scales a single cue with no ghosting,
  // sizing backdropEl to the full box and frameEl to exactly the scaled cue
  // dimensions - confirmed live (via a direct <canvas> crop of the same
  // cue for comparison) that frameEl must never be larger than the cue,
  // or the adjacent sprite row/column bleeds into the extra viewport space.
  {
    const url = "https://stash.bondt.network/scene/1/vtt/sprite.jpg";
    global.__fakeImageSizes = { ...global.__fakeImageSizes, [url]: { width: 5760, height: 3240 } };
    const backdrop = fakeElement({});
    const frame = fakeElement({});
    const cue = { url, x: 640, y: 0, w: 640, h: 360 };
    const ok = await paintCue(backdrop, frame, cue, { left: 0, top: 0, width: 1280, height: 720 });
    assert.equal(ok, true);
    // scale = min(1280/640, 720/360) = 2; box aspect matches cue aspect, so
    // the frame fills the backdrop fully too
    assert.equal(backdrop.style.left, "0.00px");
    assert.equal(backdrop.style.top, "0.00px");
    assert.equal(backdrop.style.width, "1280.00px");
    assert.equal(backdrop.style.height, "720.00px");
    assert.equal(frame.style.left, "0.00px");
    assert.equal(frame.style.top, "0.00px");
    assert.equal(frame.style.width, "1280.00px");
    assert.equal(frame.style.height, "720.00px");
    assert.equal(frame.style.backgroundPosition, "-1280.00px -0.00px");
    assert.equal(frame.style.backgroundSize, "11520.00px 6480.00px");

    assert.equal(await paintCue(null, frame, cue, { left: 0, top: 0, width: 10, height: 10 }), false);
    assert.equal(await paintCue(backdrop, null, cue, { left: 0, top: 0, width: 10, height: 10 }), false);
    assert.equal(await paintCue(backdrop, frame, null, { left: 0, top: 0, width: 10, height: 10 }), false);
    assert.equal(await paintCue(backdrop, frame, cue, { left: 0, top: 0, width: 0, height: 0 }), false);
    assert.equal(
      await paintCue(backdrop, frame, { ...cue, url: "https://x/missing.jpg" }, { left: 0, top: 0, width: 10, height: 10 }),
      false
    );
  }

  // Regression case matching the live bug report exactly: box taller than
  // the cue's 16:9 aspect (905x760.5, the safe video area with the control
  // bar already excluded). The backdrop must cover the FULL box (opaque,
  // blocking the native <video poster> underneath), while the frame is
  // letterboxed to exactly 905x509.06 within it - NOT stretched to the
  // full 760.5 height, which is exactly what let an adjacent sprite row
  // bleed in below the intended frame (confirmed live by comparing against
  // a direct <canvas> crop of the identical cue, which was always clean).
  {
    const url = "https://stash.bondt.network/scene/1/vtt/regression_sprite.jpg";
    global.__fakeImageSizes = { ...global.__fakeImageSizes, [url]: { width: 5760, height: 3240 } };
    const backdrop = fakeElement({});
    const frame = fakeElement({});
    const cue = { url, x: 5120, y: 1080, w: 640, h: 360 };
    const ok = await paintCue(backdrop, frame, cue, { left: 465, top: 55.75, width: 905, height: 760.5 });
    assert.equal(ok, true);
    assert.equal(backdrop.style.left, "465.00px");
    assert.equal(backdrop.style.top, "55.75px");
    assert.equal(backdrop.style.width, "905.00px");
    assert.equal(backdrop.style.height, "760.50px");
    // frame is letterboxed to exactly the scaled cue, never the box's height
    assert.equal(frame.style.left, "0.00px");
    assert.equal(frame.style.top, "125.72px");
    assert.equal(frame.style.width, "905.00px");
    assert.equal(frame.style.height, "509.06px");
    assert.equal(frame.style.backgroundPosition, "-7240.00px -1527.19px");
    assert.equal(frame.style.backgroundSize, "8145.00px 4581.56px");
  }

  // The scene page patch mounts the scrubber controller without disturbing
  // whatever the previous patch in the chain already rendered.
  const renderedScene = React.createElement("main", { id: "scene-page" });
  const legacyContext = {};
  const result = afterPatches.ScenePage({ scene: { id: "39382" } }, legacyContext, renderedScene);
  assert.equal(result.props.children[0], renderedScene);
  assert.equal(result.props.children[1].props.scene.id, "39382");

  const withoutScene = afterPatches.ScenePage({}, legacyContext, renderedScene);
  assert.equal(withoutScene, renderedScene, "must not wrap the render when no scene is present yet");

  console.log("Thumbnail mirroring and scene page patch behave as expected");
})().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
