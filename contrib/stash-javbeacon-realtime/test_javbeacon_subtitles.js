"use strict";

const assert = require("node:assert/strict");

let scenePageAfter;
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
        assert.equal(name, "ScenePage");
        scenePageAfter = callback;
      },
    },
  },
};

require("./javbeacon_subtitles.js");

const renderedScene = React.createElement("main", { id: "scene-page" });
const legacyContext = {};
const result = scenePageAfter(
  { scene: { id: "39382" } },
  legacyContext,
  renderedScene
);

assert.equal(result.props.children[0], renderedScene);
assert.notEqual(result.props.children[0], legacyContext);
assert.equal(result.props.children[1].props.sceneId, "39382");

console.log("ScenePage patch preserves the rendered result after legacy context");
