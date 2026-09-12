(function () {
  "use strict";

  const PLUGIN_ID = "javbeacon-realtime";
  const React = window.PluginApi.React;
  const { Button, Spinner } = window.PluginApi.libraries.Bootstrap;
  const { gql, useMutation } = window.PluginApi.libraries.Apollo;
  const { FontAwesomeIcon } = window.PluginApi.libraries.ReactFontAwesome;
  const icons = window.PluginApi.libraries.FontAwesomeSolid;
  const subtitleIcon =
    icons.faClosedCaptioning || icons.faLanguage || icons.faFileAlt;

  const REQUEST_SUBTITLES = gql`
    mutation JAVBeaconRequestSubtitles($pluginId: ID!, $args: Map) {
      runPluginOperation(plugin_id: $pluginId, args: $args)
    }
  `;

  function SubtitleButton({ sceneId }) {
    const Toast = window.PluginApi.hooks.useToast();
    const [runPluginOperation] = useMutation(REQUEST_SUBTITLES);
    const [loading, setLoading] = React.useState(false);

    const onClick = async () => {
      if (loading) return;
      setLoading(true);
      try {
        const response = await runPluginOperation({
          variables: {
            pluginId: PLUGIN_ID,
            args: { mode: "subtitles", scene_id: String(sceneId) },
          },
        });
        const result = response.data?.runPluginOperation;
        const filename = result?.filename;
        Toast.success(
          filename
            ? `Subtitle request queued for ${filename}`
            : "Subtitle request queued in JAVBeacon-Subs"
        );
      } catch (error) {
        Toast.error(error instanceof Error ? error.message : String(error));
      } finally {
        setLoading(false);
      }
    };

    return React.createElement(
      Button,
      {
        "aria-label": "Request subtitles from JAVBeacon-Subs",
        className: "minimal javbeacon-subs-button",
        disabled: loading,
        onClick,
        title: loading
          ? "Sending subtitle request…"
          : "Request subtitles from JAVBeacon-Subs",
        variant: "secondary",
      },
      loading
        ? React.createElement(Spinner, {
            animation: "border",
            role: "status",
            size: "sm",
          })
        : React.createElement(FontAwesomeIcon, { icon: subtitleIcon })
    );
  }

  function isActionGroup(element) {
    if (!React.isValidElement(element)) return false;
    const className = element.props?.className;
    if (typeof className !== "string") return false;
    if (!className.split(/\s+/).includes("scene-toolbar-group")) return false;
    return React.Children.count(element.props.children) > 1;
  }

  function injectButton(element, sceneId, state) {
    if (!React.isValidElement(element) || state.inserted) return element;

    if (isActionGroup(element)) {
      state.inserted = true;
      return React.cloneElement(
        element,
        element.props,
        React.createElement(
          "span",
          { className: "javbeacon-subs-action", key: "javbeacon-subs" },
          React.createElement(SubtitleButton, { sceneId })
        ),
        element.props.children
      );
    }

    if (!element.props?.children) return element;
    const children = React.Children.map(element.props.children, (child) =>
      injectButton(child, sceneId, state)
    );
    return React.cloneElement(element, element.props, children);
  }

  window.PluginApi.patch.after("ScenePage", function (props, rendered) {
    if (!props?.scene?.id) return rendered;
    return injectButton(rendered, props.scene.id, { inserted: false });
  });
})();
