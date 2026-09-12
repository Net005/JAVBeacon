(function () {
  "use strict";

  const PLUGIN_ID = "javbeacon-realtime";
  const React = window.PluginApi.React;
  const ReactDOM = window.PluginApi.ReactDOM;
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

  function SubtitleToolbarPortal({ sceneId }) {
    const [mountNode, setMountNode] = React.useState(null);

    React.useLayoutEffect(() => {
      const groups = Array.from(
        document.querySelectorAll(".scene-toolbar .scene-toolbar-group")
      );
      const actionGroup = groups[groups.length - 1];
      if (!actionGroup) return undefined;

      const mount = document.createElement("span");
      mount.className = "javbeacon-subs-action";
      actionGroup.insertBefore(mount, actionGroup.firstChild);
      setMountNode(mount);

      return () => {
        mount.remove();
      };
    }, [sceneId]);

    if (!mountNode) return null;
    return ReactDOM.createPortal(
      React.createElement(SubtitleButton, { sceneId }),
      mountNode
    );
  }

  window.PluginApi.patch.after("ScenePage", function (props, rendered) {
    if (!props?.scene?.id) return rendered;
    return React.createElement(
      React.Fragment,
      null,
      rendered,
      React.createElement(SubtitleToolbarPortal, {
        key: "javbeacon-subs-portal",
        sceneId: props.scene.id,
      })
    );
  });
})();
