"use strict";

const assert = require("node:assert/strict");

const afterPatches = {};
let captionQueryResult = {};
let settingsQueryResult = {
  data: {
    configuration: {
      plugins: { "javbeacon-realtime": { subs_scene_path_filters: "" } },
    },
  },
  loading: false,
};
const React = {
  Fragment: Symbol("Fragment"),
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
    ReactDOM: { createPortal() {} },
    hooks: { useToast() {} },
    libraries: {
      Apollo: {
        gql(strings) {
          return strings.join("");
        },
        useMutation() {},
        useQuery(query) {
          return query.includes("JAVBeaconSubtitleSettings")
            ? settingsQueryResult
            : captionQueryResult;
        },
      },
      Bootstrap: { Button() {}, Spinner() {} },
      FontAwesomeSolid: { faClosedCaptioning: {} },
      ReactFontAwesome: { FontAwesomeIcon() {} },
    },
    patch: {
      after(name, callback) {
        afterPatches[name] = callback;
      },
    },
  },
};

require("./javbeacon_subtitles.js");

const renderedScene = React.createElement("main", { id: "scene-page" });
const legacyContext = {};
const result = afterPatches.ScenePage(
  {
    scene: {
      id: "39382",
      files: [{ path: "/Collections/JAV/PFES-046.mp4" }],
    },
  },
  legacyContext,
  renderedScene
);

assert.equal(result.props.children[0], renderedScene);
assert.notEqual(result.props.children[0], legacyContext);
assert.equal(result.props.children[1].props.scene.id, "39382");
assert.equal(
  result.props.children[1].type(result.props.children[1].props).props.sceneId,
  "39382"
);
assert.equal(
  result.props.children[1].type(result.props.children[1].props).props.completed,
  false
);

settingsQueryResult = {
  data: {
    configuration: {
      plugins: {
        "javbeacon-realtime": {
          subs_scene_path_filters: "/other/path; /collections/jav/",
        },
      },
    },
  },
  loading: false,
};
assert.notEqual(
  result.props.children[1].type(result.props.children[1].props),
  null
);
settingsQueryResult.data.configuration.plugins[
  "javbeacon-realtime"
].subs_scene_path_filters = "/media/other/";
assert.equal(result.props.children[1].type(result.props.children[1].props), null);
settingsQueryResult.data.configuration.plugins[
  "javbeacon-realtime"
].subs_scene_path_filters = "";

const sceneWithCaptions = {
  scene: {
    id: "39382",
    captions: [{}],
    files: [{ path: "/Collections/JAV/PFES-046.mp4" }],
  },
};
const completedPageResult = afterPatches.ScenePage(
  sceneWithCaptions,
  legacyContext,
  renderedScene
);
assert.equal(completedPageResult.props.children[0], renderedScene);
assert.equal(
  completedPageResult.props.children[1].type(
    completedPageResult.props.children[1].props
  ).props.completed,
  true
);

const renderedPopovers = React.createElement("div", {
  className: "card-popovers",
});
const cardResult = afterPatches["SceneCard.Popovers"](
  {
    scene: {
      id: "39382",
      files: [{ path: "/Collections/JAV/PFES-046.mp4" }],
    },
  },
  legacyContext,
  renderedPopovers
);

assert.equal(cardResult.props.children[0], renderedPopovers);
assert.equal(
  cardResult.props.children[1].props.scene.id,
  "39382"
);
captionQueryResult = {
  data: { findScene: { captions: null } },
  loading: false,
};
assert.equal(
  cardResult.props.children[1].type(cardResult.props.children[1].props).props
    .className,
  "javbeacon-subs-card-action"
);
settingsQueryResult.data.configuration.plugins[
  "javbeacon-realtime"
].subs_scene_path_filters = "/COLLECTIONS/jav/";
assert.notEqual(
  cardResult.props.children[1].type(cardResult.props.children[1].props),
  null
);
settingsQueryResult.data.configuration.plugins[
  "javbeacon-realtime"
].subs_scene_path_filters = "/media/other/";
assert.equal(
  cardResult.props.children[1].type(cardResult.props.children[1].props),
  null
);
settingsQueryResult.data.configuration.plugins[
  "javbeacon-realtime"
].subs_scene_path_filters = "";
captionQueryResult = {
  data: { findScene: { captions: [{ language_code: "en" }] } },
  loading: false,
};
const completedCardAction = cardResult.props.children[1].type(
  cardResult.props.children[1].props
);
assert.equal(completedCardAction.props.className, "javbeacon-subs-card-action");
assert.equal(completedCardAction.props.children.props.completed, true);

const knownCompletedCard = afterPatches["SceneCard.Popovers"](
  sceneWithCaptions,
  legacyContext,
  renderedPopovers
);
assert.equal(knownCompletedCard.props.children[0], renderedPopovers);
assert.equal(
  knownCompletedCard.props.children[1].type(
    knownCompletedCard.props.children[1].props
  ).props.children.props.completed,
  true
);

console.log("Scene page and card patches preserve results after legacy context");
