"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");

const afterPatches = {};
let captionQueryResult = {
  data: { findScene: { captions: null, tags: [] } },
  loading: false,
};
const mutationCalls = [];
const queryCalls = [];
const lazyQueryCalls = [];
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
        useLazyQuery(query, options) {
          return [async (executeOptions) => {
            lazyQueryCalls.push({ query, options, executeOptions });
            return captionQueryResult;
          }];
        },
        useQuery(query, options) {
          queryCalls.push({ query, options });
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

const pluginSource = fs.readFileSync(
  require.resolve("./javbeacon_subtitles.js"),
  "utf8"
);
assert.match(pluginSource, /mode: "release_link"/);
assert.ok(
  pluginSource.indexOf("React.createElement(SubtitleButton") <
    pluginSource.indexOf("React.createElement(ReleaseLinkButton"),
  "the + CC action must remain to the left of the JAVBeacon release link"
);

(async () => {

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
const filteredPageAction = result.props.children[1].type(
  result.props.children[1].props
);
assert.notEqual(filteredPageAction, null);
assert.equal(filteredPageAction.props.showSubtitles, false);
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
function renderCardActions(result) {
  return result.props.children[1].type(result.props.children[1].props);
}
function renderCardAction(actions, index) {
  const element = actions.props.children[index];
  return element.type(element.props);
}

const captionQueriesBeforeCards = queryCalls.filter((call) =>
  call.query.includes("JAVBeaconSceneCaptions")
).length;
let cardActions = renderCardActions(cardResult);
let subtitleAction = renderCardAction(cardActions, 1);
assert.equal(subtitleAction.props.className, "javbeacon-subs-card-action");
assert.equal(
  queryCalls.find((call) => call.query.includes("JAVBeaconSubtitleSettings"))
    .options.fetchPolicy,
  "no-cache"
);
assert.equal(
  queryCalls.filter((call) => call.query.includes("JAVBeaconSceneCaptions"))
    .length,
  captionQueriesBeforeCards
);
assert.equal(lazyQueryCalls.length, 0);
const watchlistAction = renderCardAction(cardActions, 2);
const watchlistButton = watchlistAction.props.children;
assert.equal(watchlistAction.props.className, "javbeacon-watchlist-card-action");
assert.equal(watchlistButton.props.children.props.children, "+ Watchlist");
assert.equal(watchlistButton.props.disabled, false);
await watchlistButton.props.onClick({ preventDefault() {}, stopPropagation() {} });
assert.equal(lazyQueryCalls.length, 1);
assert.equal(lazyQueryCalls[0].options.variables, undefined);
assert.deepEqual(lazyQueryCalls[0].executeOptions.variables, { id: "39382" });
assert.deepEqual(mutationCalls.at(-1).options.variables, {
  input: { id: "39382", tag_ids: ["9"] },
});
settingsQueryResult.data.configuration.plugins[
  "javbeacon-realtime"
].subs_scene_path_filters = "/COLLECTIONS/jav/";
cardActions = renderCardActions(cardResult);
assert.notEqual(renderCardAction(cardActions, 1), null);
settingsQueryResult.data.configuration.plugins[
  "javbeacon-realtime"
].subs_scene_path_filters = "/media/other/";
cardActions = renderCardActions(cardResult);
assert.equal(renderCardAction(cardActions, 1), null);
assert.notEqual(renderCardAction(cardActions, 2), null);
settingsQueryResult.data.configuration.plugins[
  "javbeacon-realtime"
].subs_scene_path_filters = "";
const knownCompleteCard = afterPatches["SceneCard.Popovers"](
  {
    scene: {
      id: "39382",
      captions: [{ language_code: "en" }],
      tags: [
        { id: "9", name: "Watchlist" },
        { id: "4", name: "Keep me" },
      ],
      files: [{ path: "/Collections/JAV/PFES-046.mp4" }],
    },
  },
  legacyContext,
  renderedPopovers
);
cardActions = renderCardActions(knownCompleteCard);
const completedCardAction = renderCardAction(cardActions, 1);
assert.equal(completedCardAction.props.className, "javbeacon-subs-card-action");
assert.equal(completedCardAction.props.children.props.completed, true);
const completedWatchlistAction = renderCardAction(cardActions, 2);
assert.equal(
  completedWatchlistAction.props.children.props.children.props.children,
  "✓ Watchlist"
);
assert.equal(
  completedWatchlistAction.props.children.props["aria-pressed"],
  true
);
await completedWatchlistAction.props.children.props.onClick({
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
const knownCompletedActions = renderCardActions(knownCompletedCard);
assert.equal(
  renderCardAction(knownCompletedActions, 1).props.children.props.completed,
  true
);

console.log("Scene page and card patches preserve results after legacy context");
})().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
