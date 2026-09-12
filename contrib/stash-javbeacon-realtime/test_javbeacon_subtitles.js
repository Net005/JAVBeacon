"use strict";

const assert = require("node:assert/strict");

const afterPatches = {};
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
        gql() {
          return {};
        },
        useMutation() {},
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
  { scene: { id: "39382" } },
  legacyContext,
  renderedScene
);

assert.equal(result.props.children[0], renderedScene);
assert.notEqual(result.props.children[0], legacyContext);
assert.equal(result.props.children[1].props.sceneId, "39382");

const renderedPopovers = React.createElement("div", {
  className: "card-popovers",
});
const cardResult = afterPatches["SceneCard.Popovers"](
  { scene: { id: "39382" } },
  legacyContext,
  renderedPopovers
);

assert.equal(cardResult.props.children[0], renderedPopovers);
assert.equal(
  cardResult.props.children[1].props.className,
  "javbeacon-subs-card-action"
);
assert.equal(cardResult.props.children[1].props.children.props.sceneId, "39382");

console.log("Scene page and card patches preserve results after legacy context");
