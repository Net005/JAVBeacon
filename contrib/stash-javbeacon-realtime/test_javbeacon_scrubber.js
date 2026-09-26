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
  positionOverlay,
  parseVttTimestamp,
  parseCueImageLine,
  parseSpriteVtt,
  paintCue,
  containRect,
} = window.__javbeaconScrubberInternals;

// Two earlier revisions of this plugin mutated the scene player element
// itself (.video-js) - once by appending the overlay as its child, once by
// writing to its inline style - and both broke the scene player entirely.
// The source must never touch playerEl (or any Stash/video.js-owned
// element) except through read-only calls, so guard against reintroducing
// either mutation.
const pluginSource = fs.readFileSync(require.resolve("./javbeacon_scrubber.js"), "utf8");
assert.doesNotMatch(pluginSource, /playerEl\.appendChild/, "must never append into the player element");
assert.doesNotMatch(pluginSource, /playerEl\.style/, "must never write to the player element's style");
assert.match(pluginSource, /document\.body\.appendChild\(overlay\)/, "the overlay must be appended to document.body");

const cssSource = fs.readFileSync(require.resolve("./javbeacon_scrubber.css"), "utf8");
assert.match(cssSource, /position:\s*fixed/, "the overlay must be position: fixed, not relative to the player");

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

// Confirmed live: the control bar sits on top of the video, not below it,
// so the full player/poster rect includes the strip the control bar
// occupies. Both preview paths must derive their box from safeVideoBox
// (which subtracts that strip), not from a raw player/poster rect, or the
// overlay's high z-index paints over the control bar and hides the seek
// position while scrubbing.
assert.match(pluginSource, /function safeVideoBox\(/, "must define safeVideoBox to exclude the control bar strip");
{
  const seekMoveBody = /function onSeekMove\(\) \{[\s\S]*?\n    \}/.exec(jsWithoutComments)?.[0] || "";
  const coverBoxBody = /function coverBox\(\) \{[\s\S]*?\n    \}/.exec(jsWithoutComments)?.[0] || "";
  assert.match(seekMoveBody, /safeVideoBox\(playerEl\)/, "onSeekMove must derive its box from safeVideoBox");
  assert.doesNotMatch(seekMoveBody, /playerEl\.getBoundingClientRect\(\)/, "onSeekMove must not use the raw player rect");
  assert.match(coverBoxBody, /safeVideoBox\(playerEl\)/, "coverBox must derive its box from safeVideoBox");
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

// positionOverlay copies a rect (as returned by getBoundingClientRect, which
// is already viewport-relative) directly onto a position: fixed element's
// left/top/width/height, and leaves the element untouched for a degenerate
// (zero-size) rect rather than positioning it at 0x0.
{
  const overlay = { style: {} };
  positionOverlay(overlay, { left: 12, top: 34, width: 560, height: 315 });
  assert.equal(overlay.style.left, "12px");
  assert.equal(overlay.style.top, "34px");
  assert.equal(overlay.style.width, "560px");
  assert.equal(overlay.style.height, "315px");

  const untouched = { style: {} };
  positionOverlay(untouched, { left: 0, top: 0, width: 0, height: 0 });
  assert.equal(untouched.style.left, undefined);
  positionOverlay(untouched, null);
  assert.equal(untouched.style.left, undefined);
}

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
  // containRect fits contentW x contentH inside box the way CSS
  // "background-size: contain" would: scaled up as far as possible without
  // exceeding either axis, then centered - never stretched to fill box on
  // an axis the content doesn't reach. An earlier revision stretched the
  // overlay to fill the whole box while only painting a scaled image sized
  // by the smaller-ratio axis, so the leftover space on the other axis
  // revealed whatever sprite content sat past the edge of the intended
  // cell - visible as a second, wrong frame bleeding in from the next row.
  {
    // uniform box: no letterboxing needed
    assert.deepEqual(containRect({ left: 0, top: 0, width: 800, height: 450 }, 160, 90), {
      left: 0,
      top: 0,
      width: 800,
      height: 450,
      scale: 5,
    });
    // taller box than content aspect: letterboxed top/bottom, centered
    const fit = containRect({ left: 10, top: 20, width: 400, height: 400 }, 160, 90);
    assert.equal(fit.scale, 2.5); // width-limited: 400/160
    assert.equal(fit.width, 400);
    assert.equal(fit.height, 225);
    assert.equal(fit.left, 10);
    assert.equal(fit.top, 20 + (400 - 225) / 2);
    // degenerate inputs
    assert.equal(containRect(null, 10, 10), null);
    assert.equal(containRect({ left: 0, top: 0, width: 0, height: 0 }, 10, 10), null);
    assert.equal(containRect({ left: 0, top: 0, width: 10, height: 10 }, 0, 0), null);
  }

  // mirrorBackground scales a source element's background image, position
  // and size up, preserving the exact crop Stash's own thumbnail element
  // already computed - this is what replaces re-deriving the sprite crop
  // from scratch (the earlier approach that produced overlapping/ghosted
  // frames). It also sizes/positions targetEl itself to the letterboxed
  // fit within box, rather than stretching it to fill box.
  {
    const source = fakeElement(
      {
        backgroundImage: 'url("https://stash.example.com/scene/1/vtt/sprite.jpg")',
        backgroundPosition: "-160px -90px",
        backgroundSize: "1920px 1080px",
      },
      { width: 160, height: 90 }
    );
    const target = fakeElement({});
    const ok = await mirrorBackground(source, target, { left: 0, top: 0, width: 800, height: 450 });
    assert.equal(ok, true);
    assert.equal(target.style.backgroundImage, source.style.backgroundImage);
    assert.equal(target.style.backgroundRepeat, "no-repeat");
    // box aspect matches source aspect exactly, so it fills the box fully
    assert.equal(target.style.left, "0.00px");
    assert.equal(target.style.top, "0.00px");
    assert.equal(target.style.width, "800.00px");
    assert.equal(target.style.height, "450.00px");
    // scale = 800 / 160 = 5
    assert.equal(target.style.backgroundPosition, "-800.00px -450.00px");
    assert.equal(target.style.backgroundSize, "9600.00px 5400.00px");
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
    const target = fakeElement({});
    // box wider than the source box by 1.5x, same aspect ratio
    const ok = await mirrorBackground(source, target, { left: 0, top: 0, width: 960, height: 540 });
    assert.equal(ok, true);
    assert.equal(target.style.backgroundPosition, "-5760.00px -1080.00px");
    assert.equal(target.style.backgroundSize, "8640.00px 4860.00px");
  }

  // Regression case matching the live bug report exactly: box (the player
  // rect) is much taller than the seek-bar thumbnail's 16:9 aspect ratio
  // (905x760.5, mimicking a real player rect that still includes room for
  // the control bar). The overlay must be letterboxed to 905x509.06 and
  // centered, NOT stretched to the full 760.5 height - stretching it is
  // what let the next sprite row bleed into view below the intended frame.
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
    const target = fakeElement({});
    const ok = await mirrorBackground(source, target, { left: 465, top: 55.75, width: 905, height: 760.5 });
    assert.equal(ok, true);
    assert.equal(target.style.left, "465.00px");
    assert.equal(target.style.width, "905.00px");
    // height must be letterboxed to the scaled content height, not the box's
    assert.equal(target.style.height, "509.06px");
    assert.equal(target.style.top, "181.47px");
    assert.equal(target.style.backgroundPosition, "-7240.00px -1527.19px");
    assert.equal(target.style.backgroundSize, "8145.00px 4581.56px");
  }

  // A source element with no thumbnail currently painted (no
  // background-image yet) must not overwrite whatever the target was
  // already showing.
  {
    const source = fakeElement({ backgroundImage: "" }, { width: 160, height: 90 });
    const target = fakeElement({ backgroundImage: "url(previous.jpg)" });
    const ok = await mirrorBackground(source, target, { left: 0, top: 0, width: 800, height: 450 });
    assert.equal(ok, false);
    assert.equal(target.style.backgroundImage, "url(previous.jpg)");
  }

  // A non-uniform target box scales by the smaller ratio and centers on the
  // other axis, so the mirrored crop never overflows either dimension and
  // never leaves stray sprite content visible past its edges.
  {
    const source = fakeElement(
      {
        backgroundImage: "url(sprite.jpg)",
        backgroundPosition: "-100px -50px",
        backgroundSize: "1000px 500px",
      },
      { width: 100, height: 50 }
    );
    const target = fakeElement({});
    await mirrorBackground(source, target, { left: 0, top: 0, width: 400, height: 150 });
    // width ratio = 4, height ratio = 3 -> use 3
    assert.equal(target.style.backgroundPosition, "-300.00px -150.00px");
    assert.equal(target.style.width, "300.00px");
    assert.equal(target.style.height, "150.00px");
    assert.equal(target.style.left, "50.00px"); // centered: (400-300)/2
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
    const target = fakeElement({});
    const ok = await mirrorBackground(source, target, { left: 0, top: 0, width: 200, height: 100 });
    assert.equal(ok, true);
    assert.equal(target.style.backgroundPosition, "-200.00px -100.00px");
  }

  assert.equal(await mirrorBackground(null, {}, { left: 0, top: 0, width: 1, height: 1 }), false);
  assert.equal(
    await mirrorBackground(fakeElement({ backgroundImage: "url(a.jpg)" }, { width: 10, height: 10 }), {}, null),
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
  // sizing/positioning targetEl to the letterboxed fit rather than
  // stretching it to fill box.
  {
    const url = "https://stash.bondt.network/scene/1/vtt/sprite.jpg";
    global.__fakeImageSizes = { ...global.__fakeImageSizes, [url]: { width: 5760, height: 3240 } };
    const target = fakeElement({});
    const cue = { url, x: 640, y: 0, w: 640, h: 360 };
    const ok = await paintCue(target, cue, { left: 0, top: 0, width: 1280, height: 720 });
    assert.equal(ok, true);
    // scale = min(1280/640, 720/360) = 2; box aspect matches cue aspect, so
    // it fills the box fully
    assert.equal(target.style.left, "0.00px");
    assert.equal(target.style.top, "0.00px");
    assert.equal(target.style.width, "1280.00px");
    assert.equal(target.style.height, "720.00px");
    assert.equal(target.style.backgroundPosition, "-1280.00px -0.00px");
    assert.equal(target.style.backgroundSize, "11520.00px 6480.00px");

    assert.equal(await paintCue(null, cue, { left: 0, top: 0, width: 10, height: 10 }), false);
    assert.equal(await paintCue(target, null, { left: 0, top: 0, width: 10, height: 10 }), false);
    assert.equal(await paintCue(target, cue, { left: 0, top: 0, width: 0, height: 0 }), false);
    assert.equal(
      await paintCue(target, { ...cue, url: "https://x/missing.jpg" }, { left: 0, top: 0, width: 10, height: 10 }),
      false
    );
  }

  // Regression case matching the live bug report exactly: box taller than
  // the cue's 16:9 aspect (905x760.5, the pre-control-bar-exclusion player
  // rect). paintCue must letterbox to 905x509.06 and center it, not stretch
  // to 760.5 tall - the same "next sprite row bleeds in below the frame"
  // bug reported for the cover-area cycle as well as the seek-bar mirror.
  {
    const url = "https://stash.bondt.network/scene/1/vtt/regression_sprite.jpg";
    global.__fakeImageSizes = { ...global.__fakeImageSizes, [url]: { width: 5760, height: 3240 } };
    const target = fakeElement({});
    const cue = { url, x: 5120, y: 1080, w: 640, h: 360 };
    const ok = await paintCue(target, cue, { left: 465, top: 55.75, width: 905, height: 760.5 });
    assert.equal(ok, true);
    assert.equal(target.style.left, "465.00px");
    assert.equal(target.style.width, "905.00px");
    assert.equal(target.style.height, "509.06px");
    assert.equal(target.style.top, "181.47px");
    assert.equal(target.style.backgroundPosition, "-7240.00px -1527.19px");
    assert.equal(target.style.backgroundSize, "8145.00px 4581.56px");
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
