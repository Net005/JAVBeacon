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

const { parseVttTimestamp, parseCueImageLine, parseSpriteVtt, findCueAtTime } =
  window.__javbeaconScrubberInternals;

// parseVttTimestamp accepts both "." and "," millisecond separators, since
// generated sprite VTT files are not guaranteed to use the strict WebVTT
// "." form the way subtitle files usually do.
assert.equal(parseVttTimestamp("00:00:05.500"), 5.5);
assert.equal(parseVttTimestamp("00:01:30,000"), 90);
assert.equal(parseVttTimestamp("01:00:00.000"), 3600);
assert.equal(parseVttTimestamp("not a timestamp"), null);

// parseCueImageLine resolves relative sprite paths against the VTT file's own
// URL and extracts the #xywh= media fragment.
const base = "https://stash.example.com/scene/12/vtt/1234.vtt";
assert.deepEqual(parseCueImageLine("sprite.jpg#xywh=0,0,160,90", base), {
  url: "https://stash.example.com/scene/12/vtt/sprite.jpg",
  x: 0,
  y: 0,
  w: 160,
  h: 90,
  whole: false,
});
assert.deepEqual(parseCueImageLine("sprite.jpg#xywh=320,90,160,90", base), {
  url: "https://stash.example.com/scene/12/vtt/sprite.jpg",
  x: 320,
  y: 90,
  w: 160,
  h: 90,
  whole: false,
});
assert.equal(parseCueImageLine("", base), null);
assert.equal(parseCueImageLine("sprite.jpg#xywh=1,2,3", base), null, "malformed fragment must be rejected");
assert.equal(parseCueImageLine("sprite.jpg#xywh=1,2,0,90", base), null, "zero width must be rejected");

// parseSpriteVtt walks a full sprite VTT document.
const vttText = [
  "WEBVTT",
  "",
  "1",
  "00:00:00.000 --> 00:00:02.000",
  "sprite.jpg#xywh=0,0,160,90",
  "",
  "2",
  "00:00:02.000 --> 00:00:04.000",
  "sprite.jpg#xywh=160,0,160,90",
  "",
  "3",
  "00:00:04.000 --> 00:00:06.000",
  "sprite.jpg#xywh=320,0,160,90",
  "",
].join("\n");
const cues = parseSpriteVtt(vttText, base);
assert.equal(cues.length, 3);
assert.equal(cues[0].start, 0);
assert.equal(cues[0].end, 2);
assert.equal(cues[1].x, 160);
assert.equal(cues[2].end, 6);

// Cues out of order in the source file are sorted by start time.
const shuffled = parseSpriteVtt(
  [
    "WEBVTT",
    "",
    "00:00:04.000 --> 00:00:06.000",
    "sprite.jpg#xywh=320,0,160,90",
    "",
    "00:00:00.000 --> 00:00:02.000",
    "sprite.jpg#xywh=0,0,160,90",
    "",
  ].join("\n"),
  base
);
assert.equal(shuffled[0].start, 0);
assert.equal(shuffled[1].start, 4);

// findCueAtTime locates the cue covering a given playback time, including
// the boundary just before the first cue and just after the last one.
assert.equal(findCueAtTime(cues, 0).x, 0);
assert.equal(findCueAtTime(cues, 1.999).x, 0);
assert.equal(findCueAtTime(cues, 2).x, 160);
assert.equal(findCueAtTime(cues, 5.999).x, 320);
assert.equal(findCueAtTime(cues, 6).x, 320, "time at exact duration falls back to the last cue");
assert.equal(findCueAtTime(cues, -1).x, 0, "time before the first cue falls back to the first cue");
assert.equal(findCueAtTime([], 1), null);
assert.equal(findCueAtTime(cues, Number.NaN), null);

// The scene page patch mounts the scrubber controller without disturbing
// whatever the previous patch in the chain already rendered.
const renderedScene = React.createElement("main", { id: "scene-page" });
const legacyContext = {};
const result = afterPatches.ScenePage(
  { scene: { id: "39382", paths: { vtt: "https://stash.example.com/scene/39382/vtt" } } },
  legacyContext,
  renderedScene
);
assert.equal(result.props.children[0], renderedScene);
assert.equal(result.props.children[1].props.scene.id, "39382");

const withoutScene = afterPatches.ScenePage({}, legacyContext, renderedScene);
assert.equal(withoutScene, renderedScene, "must not wrap the render when no scene is present yet");

console.log("Sprite VTT parsing and scene page patch behave as expected");
