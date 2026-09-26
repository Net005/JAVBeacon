"use strict";

const assert = require("node:assert/strict");

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

const { extractPx, mirrorBackground } = window.__javbeaconScrubberInternals;

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

// mirrorBackground scales a source element's background image, position and
// size up to fill a larger box, preserving the exact crop Stash's own
// thumbnail element already computed - this is what replaces re-deriving
// the sprite crop from scratch (the earlier approach that produced
// overlapping/ghosted frames).
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
  const ok = mirrorBackground(source, target, { width: 800, height: 450 });
  assert.equal(ok, true);
  assert.equal(target.style.backgroundImage, source.style.backgroundImage);
  assert.equal(target.style.backgroundRepeat, "no-repeat");
  // scale = 800 / 160 = 5
  assert.equal(target.style.backgroundPosition, "-800.00px -450.00px");
  assert.equal(target.style.backgroundSize, "9600.00px 5400.00px");
}

// A source element with no thumbnail currently painted (no background-image
// yet) must not overwrite whatever the target was already showing.
{
  const source = fakeElement({ backgroundImage: "" }, { width: 160, height: 90 });
  const target = fakeElement({ backgroundImage: "url(previous.jpg)" });
  const ok = mirrorBackground(source, target, { width: 800, height: 450 });
  assert.equal(ok, false);
  assert.equal(target.style.backgroundImage, "url(previous.jpg)");
}

// A non-uniform target box scales by the smaller ratio so the mirrored crop
// never overflows either dimension.
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
  mirrorBackground(source, target, { width: 400, height: 150 });
  // width ratio = 4, height ratio = 3 -> use 3
  assert.equal(target.style.backgroundPosition, "-300.00px -150.00px");
}

// Falls back to reading the source element's own width/height style when
// getBoundingClientRect is unavailable (defensive; real DOM elements always
// provide it, but keeps this function usable in more constrained contexts).
{
  const source = fakeElement({
    backgroundImage: "url(sprite.jpg)",
    backgroundPosition: "-20px -10px",
    backgroundSize: "200px 100px",
    width: "20px",
    height: "10px",
  });
  const target = fakeElement({});
  const ok = mirrorBackground(source, target, { width: 200, height: 100 });
  assert.equal(ok, true);
  assert.equal(target.style.backgroundPosition, "-200.00px -100.00px");
}

assert.equal(mirrorBackground(null, {}, { width: 1, height: 1 }), false);
assert.equal(
  mirrorBackground(fakeElement({ backgroundImage: "url(a.jpg)" }, { width: 10, height: 10 }), {}, null),
  false
);

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
