"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");

const afterPatches = {};
let captionQueryResult = {
  data: { findScene: { captions: null, tags: [] } },
  loading: false,
};
const mutationCalls = [];
const confirmationCalls = [];
let confirmResult = true;
let confirmQueue = [];
let subtitleStatusResult = null;
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
  useLayoutEffect() {},
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
  confirm(message) {
    confirmationCalls.push(message);
    return confirmQueue.length ? confirmQueue.shift() : confirmResult;
  },
  PluginApi: {
    React,
    ReactDOM: { createPortal(element) { return element; } },
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
              if (options?.variables?.args?.mode === "subtitle_status") {
                return { data: { runPluginOperation: subtitleStatusResult } };
              }
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
assert.match(pluginSource, /JAVBeaconRealtimeHistorySync/);
assert.match(pluginSource, /sceneAddPlay/);
assert.match(pluginSource, /sceneAddO/);
assert.match(pluginSource, /sceneSaveActivity/);
assert.match(pluginSource, /details\s*\n\s*captions/);
assert.match(pluginSource, /title: story/);
assert.match(pluginSource, /"aria-expanded": expanded/);
assert.match(pluginSource, /setExpanded\(\(value\) => !value\)/);
assert.ok(
  pluginSource.indexOf("React.createElement(SubtitleButton") <
    pluginSource.indexOf("React.createElement(ReleaseLinkButton"),
  "the + CC action must remain to the left of the JAVBeacon release link"
);
assert.ok(
  pluginSource.indexOf("React.createElement(SceneCardWatchlistAction") <
    pluginSource.indexOf("React.createElement(SceneCardSubtitleAction"),
  "Watchlist must render before (left of) +CC in the card actions row, " +
    "matching their original position: absolute left/right corners"
);
// subtitleStatusColor's four branches (see its own doc comment): the mock
// test harness stubs React.useEffect as a no-op, so the hover-triggered
// subtitle_status fetch that feeds this function in the running plugin can't
// be exercised end to end here - these are structural checks that the
// mapping itself, and the CSS classes it drives, exist as documented.
assert.match(pluginSource, /function subtitleStatusColor\(hasSubtitles, status\)/);
assert.match(pluginSource, /if \(!hasSubtitles\) return null;/);
assert.match(pluginSource, /if \(status === undefined\) return null;/);
assert.match(pluginSource, /if \(!status \|\| !status\.sidecar_found\) return "red";/);
assert.match(pluginSource, /if \(status\.up_to_date === true\) return "green";/);
assert.match(pluginSource, /statusColor: subtitleStatusColor\(completed, subtitleStatus\)/);
const cssSource = fs.readFileSync(
  require.resolve("./javbeacon_subtitles.css"),
  "utf8"
);
assert.match(cssSource, /\.javbeacon-subs-status-green\s*\{/);
assert.match(cssSource, /\.javbeacon-subs-status-orange\s*\{/);
assert.match(cssSource, /\.javbeacon-subs-status-red\s*\{/);

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

// SceneCard.Popovers now returns SceneCardActions directly, with Stash's own
// popovers content passed through as the "popovers" prop rather than
// rendered as an independent sibling - so the merge below adds no extra line.
assert.equal(cardResult.props.popovers, renderedPopovers);
assert.equal(cardResult.props.scene.id, "39382");
captionQueryResult = {
  data: { findScene: { captions: null, tags: [] } },
  loading: false,
};
function renderCardActions(result) {
  return result.type(result.props);
}
// The subtitle/watchlist buttons are merged into the same
// "javbeacon-card-actions-row" div (cardActions.props.children[2]) that now
// also carries Stash's own popovers content - see the merge-row comment
// above .javbeacon-card-actions-row for why. Watchlist sits inside the
// row's first child, a "javbeacon-card-actions-left" cluster it shares with
// Stash's popovers (left, index 0 of that cluster); +CC is the row's own
// second child (right, index 1 of the row).
function renderCardAction(actions, index) {
  const row = actions.props.children[2];
  if (index === 0) {
    const left = row.props.children[0];
    const element = left.props.children[0];
    return element.type(element.props);
  }
  const element = row.props.children[1];
  return element.type(element.props);
}
function renderWatchlistAction(actions) {
  return renderCardAction(actions, 0);
}
function renderSubtitleAction(actions) {
  return renderCardAction(actions, 1);
}

const captionQueriesBeforeCards = queryCalls.filter((call) =>
  call.query.includes("JAVBeaconSceneCaptions")
).length;
let cardActions = renderCardActions(cardResult);
assert.equal(
  cardActions.props.children[2].props.className,
  "javbeacon-card-actions-row"
);
const leftCluster = cardActions.props.children[2].props.children[0];
assert.equal(leftCluster.props.className, "javbeacon-card-actions-left");
assert.equal(leftCluster.props.children[1], renderedPopovers);
let subtitleAction = renderSubtitleAction(cardActions);
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
const watchlistAction = renderWatchlistAction(cardActions);
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
assert.notEqual(renderSubtitleAction(cardActions), null);
settingsQueryResult.data.configuration.plugins[
  "javbeacon-realtime"
].subs_scene_path_filters = "/media/other/";
cardActions = renderCardActions(cardResult);
assert.equal(renderSubtitleAction(cardActions), null);
assert.notEqual(renderWatchlistAction(cardActions), null);
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
      details: "A detailed story that should appear below the scene ID.",
    },
  },
  legacyContext,
  renderedPopovers
);
cardActions = renderCardActions(knownCompleteCard);
assert.equal(
  cardActions.props.children[1].props.scene.details,
  "A detailed story that should appear below the scene ID."
);
const storyElement = cardActions.props.children[1];
const originalUseState = React.useState;
let storyStateIndex = 0;
let expandedState = null;
React.useState = (initial) => {
  storyStateIndex += 1;
  if (storyStateIndex === 1) return [{ closest() {} }, () => {}];
  if (storyStateIndex === 2) return [{}, () => {}];
  return [initial, (updater) => {
    expandedState = updater(initial);
  }];
};
const storyResult = storyElement.type(storyElement.props);
React.useState = originalUseState;
const storyContent = storyResult.props.children[1];
assert.equal(storyContent.props.title, storyElement.props.scene.details);
assert.equal(storyContent.props["aria-expanded"], false);
let prevented = false;
let stopped = false;
storyContent.props.onClick({
  preventDefault() { prevented = true; },
  stopPropagation() { stopped = true; },
});
assert.equal(expandedState, true);
assert.equal(prevented, true);
assert.equal(stopped, true);
const completedCardAction = renderSubtitleAction(cardActions);
assert.equal(completedCardAction.props.className, "javbeacon-subs-card-action");
assert.equal(completedCardAction.props.children.props.completed, true);
const completedSubtitleButton = completedCardAction.props.children.type(
  completedCardAction.props.children.props
);
assert.equal(completedSubtitleButton.props.disabled, false);
function subtitleModeCalls(mode) {
  return mutationCalls.filter((call) => call.options?.variables?.args?.mode === mode);
}

// No sidecar/status info available (subtitleStatusResult stays null): the
// original plain "replace subtitles?" confirmation still gates the request,
// but a subtitle_status check now always runs first.
const subtitleRequestsBeforeCancel = subtitleModeCalls("subtitles").length;
const statusRequestsBeforeCancel = subtitleModeCalls("subtitle_status").length;
confirmResult = false;
await completedSubtitleButton.props.onClick({ preventDefault() {}, stopPropagation() {} });
assert.equal(subtitleModeCalls("subtitle_status").length, statusRequestsBeforeCancel + 1);
assert.equal(confirmationCalls.length, 1);
assert.equal(subtitleModeCalls("subtitles").length, subtitleRequestsBeforeCancel);
confirmResult = true;
await completedSubtitleButton.props.onClick({ preventDefault() {}, stopPropagation() {} });
assert.deepEqual(mutationCalls.at(-1).options.variables.args, {
  mode: "subtitles", scene_id: "39382", overwrite: true,
});

// A sidecar exists but is older than JAVBeacon-Subs's current backend: one
// confirmation naming both backends gates the request.
subtitleStatusResult = {
  sidecar_found: true,
  up_to_date: false,
  reason: "outdated",
  sidecar_backends: { transcription_backend: "whisper-large-v2", translation_backend: "gpt-4o-mini" },
  current_backends: { transcription_backend: "Qwen/Qwen3-ASR-1.7B", translation_backend: "gpt-5.6-luna" },
};
const confirmationsBeforeOutdated = confirmationCalls.length;
confirmResult = true;
await completedSubtitleButton.props.onClick({ preventDefault() {}, stopPropagation() {} });
assert.equal(confirmationCalls.length, confirmationsBeforeOutdated + 1);
assert.match(confirmationCalls.at(-1), /whisper-large-v2 \/ gpt-4o-mini/);
assert.match(confirmationCalls.at(-1), /Qwen\/Qwen3-ASR-1\.7B \/ gpt-5\.6-luna/);
assert.deepEqual(mutationCalls.at(-1).options.variables.args, {
  mode: "subtitles", scene_id: "39382", overwrite: true,
});

// A sidecar already matches the current backend: overwriting requires TWO
// confirmations, the second an explicit force-overwrite warning. Declining
// either one sends no subtitle request.
subtitleStatusResult = {
  sidecar_found: true,
  up_to_date: true,
  reason: "up_to_date",
  sidecar_backends: { transcription_backend: "Qwen/Qwen3-ASR-1.7B", translation_backend: "gpt-5.6-luna" },
  current_backends: { transcription_backend: "Qwen/Qwen3-ASR-1.7B", translation_backend: "gpt-5.6-luna" },
};
const subtitleRequestsBeforeUpToDate = subtitleModeCalls("subtitles").length;
confirmQueue = [true, false];
await completedSubtitleButton.props.onClick({ preventDefault() {}, stopPropagation() {} });
assert.match(confirmationCalls.at(-1), /FORCE OVERWRITE/);
assert.equal(subtitleModeCalls("subtitles").length, subtitleRequestsBeforeUpToDate);
confirmQueue = [true, true];
await completedSubtitleButton.props.onClick({ preventDefault() {}, stopPropagation() {} });
assert.deepEqual(mutationCalls.at(-1).options.variables.args, {
  mode: "subtitles", scene_id: "39382", overwrite: true,
});
subtitleStatusResult = null;
const completedWatchlistAction = renderWatchlistAction(cardActions);
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
assert.equal(knownCompletedCard.props.popovers, renderedPopovers);
const knownCompletedActions = renderCardActions(knownCompletedCard);
assert.equal(
  renderSubtitleAction(knownCompletedActions).props.children.props.completed,
  true
);

const sceneWithoutCaptions = afterPatches["SceneCard.Popovers"](
  { scene: { id: "39400", captions: [], tags: [], details: "", files: [{ path: "/Collections/JAV/TEST-001.mp4" }] } },
  legacyContext,
  renderedPopovers
);
const noCaptionAction = renderSubtitleAction(renderCardActions(sceneWithoutCaptions));
const noCaptionButton = noCaptionAction.props.children.type(noCaptionAction.props.children.props);
const confirmationsBeforeNew = confirmationCalls.length;
await noCaptionButton.props.onClick({ preventDefault() {}, stopPropagation() {} });
assert.equal(confirmationCalls.length, confirmationsBeforeNew);
assert.deepEqual(mutationCalls.at(-1).options.variables.args, {
  mode: "subtitles", scene_id: "39400", overwrite: false,
});

console.log("Scene page and card patches preserve results after legacy context");
})().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
