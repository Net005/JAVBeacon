"use strict";

const assert = require("node:assert/strict");

const afterPatches = {};
let captionQueryResult = {};
const mutationCalls = [];
let settingsQueryResult = {
  data: {
    configuration: {
      plugins: {
        "javbeacon-realtime": {
          subs_scene_path_filters: "",
          watchlist_tag_id: "9",
        },
      },
    },
  },
  loading: false,
};
const React = {
  Fragment: Symbol("Fragment"),
  useState(initial) {
    return [initial, () => {}];
  },
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
    hooks: { useToast: () => ({ error() {}, success() {} }) },
    libraries: {
      Apollo: {
        gql(strings) {
          return strings.join("");
        },
        useMutation(query) {
          return [
            async (options) => {
              mutationCalls.push({ options, query });
              return { data: {} };
            },
          ];
        },
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
          watchlist_tag_id: "9",
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
  data: { findScene: { captions: null, tags: [] } },
  loading: false,
};
assert.equal(
  cardResult.props.children[1].type(cardResult.props.children[1].props).props
    .className,
  "javbeacon-subs-card-action"
);
const watchlistAction = cardResult.props.children[2].type(
  cardResult.props.children[2].props
);
const watchlistButton = watchlistAction.props.children;
assert.equal(watchlistAction.props.className, "javbeacon-watchlist-card-action");
assert.equal(watchlistButton.props.children.props.children, "+ Watchlist");
assert.equal(watchlistButton.props.disabled, false);
watchlistButton.props.onClick({ preventDefault() {}, stopPropagation() {} });
assert.deepEqual(mutationCalls.at(-1).options.variables, {
  input: { id: "39382", tag_ids: ["9"] },
});
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
assert.notEqual(
  cardResult.props.children[2].type(cardResult.props.children[2].props),
  null
);
settingsQueryResult.data.configuration.plugins[
  "javbeacon-realtime"
].subs_scene_path_filters = "";
captionQueryResult = {
  data: {
    findScene: {
      captions: [{ language_code: "en" }],
      tags: [
        { id: "9", name: "Watchlist" },
        { id: "4", name: "Keep me" },
      ],
    },
  },
  loading: false,
};
const completedCardAction = cardResult.props.children[1].type(
  cardResult.props.children[1].props
);
assert.equal(completedCardAction.props.className, "javbeacon-subs-card-action");
assert.equal(completedCardAction.props.children.props.completed, true);
const completedWatchlistAction = cardResult.props.children[2].type(
  cardResult.props.children[2].props
);
assert.equal(
  completedWatchlistAction.props.children.props.children.props.children,
  "✓ Watchlist"
);
assert.equal(
  completedWatchlistAction.props.children.props["aria-pressed"],
  true
);
completedWatchlistAction.props.children.props.onClick({
  preventDefault() {},
  stopPropagation() {},
});
assert.deepEqual(mutationCalls.at(-1).options.variables, {
  input: { id: "39382", tag_ids: ["4"] },
});

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
