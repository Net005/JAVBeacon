(function () {
  "use strict";

  const PLUGIN_ID = "javbeacon-realtime";
  const React = window.PluginApi.React;
  const ReactDOM = window.PluginApi.ReactDOM;
  const { Button, Spinner } = window.PluginApi.libraries.Bootstrap;
  const { gql, useMutation, useQuery } = window.PluginApi.libraries.Apollo;

  const REQUEST_SUBTITLES = gql`
    mutation JAVBeaconRequestSubtitles($pluginId: ID!, $args: Map) {
      runPluginOperation(plugin_id: $pluginId, args: $args)
    }
  `;

  const FIND_SCENE_CAPTIONS = gql`
    query JAVBeaconSceneCaptions($id: ID!) {
      findScene(id: $id) {
        id
        captions {
          language_code
          caption_type
        }
      }
    }
  `;

  const FIND_PLUGIN_SETTINGS = gql`
    query JAVBeaconSubtitleSettings {
      configuration {
        plugins(include: ["javbeacon-realtime"])
      }
    }
  `;

  function hasLinkedSubtitles(scene) {
    return Array.isArray(scene?.captions) && scene.captions.length > 0;
  }

  function sceneMatchesPathFilters(scene, settings) {
    const filters = String(settings?.subs_scene_path_filters || "")
      .split(/[\n,;]+/)
      .map((value) => value.trim().toLowerCase())
      .filter(Boolean);
    if (filters.length === 0) return true;

    const path = String(scene?.files?.[0]?.path || "").toLowerCase();
    return filters.some((value) => path.includes(value));
  }

  function usePluginSettings() {
    const result = useQuery(FIND_PLUGIN_SETTINGS, {
      fetchPolicy: "cache-first",
    });
    return {
      ...result,
      settings: result.data?.configuration?.plugins?.[PLUGIN_ID],
    };
  }

  function SubtitleButton({ sceneId, completed = false }) {
    const Toast = window.PluginApi.hooks.useToast();
    const [runPluginOperation] = useMutation(REQUEST_SUBTITLES);
    const [loading, setLoading] = React.useState(false);

    const onClick = async (event) => {
      event?.preventDefault();
      event?.stopPropagation();
      if (loading || completed) return;
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
        "aria-label": completed
          ? "Subtitle linked to this scene"
          : "Request subtitles from JAVBeacon-Subs",
        className: `minimal javbeacon-subs-button${
          completed ? " javbeacon-subs-complete" : ""
        }`,
        disabled: loading || completed,
        onClick: completed ? undefined : onClick,
        onMouseDown: (event) => event.stopPropagation(),
        title: completed
          ? "Subtitle linked to this scene"
          : loading
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
        : React.createElement(
            "span",
            { className: "javbeacon-subs-label", "aria-hidden": "true" },
            completed ? "✓ CC" : "+ CC"
          )
    );
  }

  function SceneCardSubtitleAction({ scene }) {
    const settingsQuery = usePluginSettings();
    const captionsKnown =
      scene != null &&
      Object.prototype.hasOwnProperty.call(scene, "captions");
    const { data, loading, error } = useQuery(FIND_SCENE_CAPTIONS, {
      fetchPolicy: "cache-first",
      skip: captionsKnown,
      variables: { id: String(scene.id) },
    });
    const captions = captionsKnown
      ? scene.captions
      : data?.findScene?.captions;
    const resolved = captionsKnown || data?.findScene != null;

    // Keep the action hidden until Stash confirms the linked-subtitle state.
    if (
      settingsQuery.loading ||
      settingsQuery.error ||
      settingsQuery.settings == null ||
      !sceneMatchesPathFilters(scene, settingsQuery.settings) ||
      loading ||
      error ||
      !resolved
    ) {
      return null;
    }

    return React.createElement(
      "div",
      {
        className: "javbeacon-subs-card-action",
      },
      React.createElement(SubtitleButton, {
        completed: hasLinkedSubtitles({ captions }),
        sceneId: scene.id,
      })
    );
  }

  function ScenePageSubtitleAction({ scene }) {
    const { settings, loading, error } = usePluginSettings();
    if (
      loading ||
      error ||
      settings == null ||
      !sceneMatchesPathFilters(scene, settings)
    ) {
      return null;
    }
    return React.createElement(SubtitleToolbarPortal, {
      completed: hasLinkedSubtitles(scene),
      sceneId: scene.id,
    });
  }

  function SubtitleToolbarPortal({ sceneId, completed }) {
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
      React.createElement(SubtitleButton, { completed, sceneId }),
      mountNode
    );
  }

  window.PluginApi.patch.after("ScenePage", function (...args) {
    const props = args[0];
    const rendered = args[args.length - 1];
    if (!props?.scene?.id) return rendered;
    return React.createElement(
      React.Fragment,
      null,
      rendered,
      React.createElement(ScenePageSubtitleAction, {
        key: "javbeacon-subs-portal",
        scene: props.scene,
      })
    );
  });

  window.PluginApi.patch.after("SceneCard.Popovers", function (...args) {
    const props = args[0];
    const rendered = args[args.length - 1];
    if (!props?.scene?.id) return rendered;
    return React.createElement(
      React.Fragment,
      null,
      rendered,
      React.createElement(SceneCardSubtitleAction, {
        key: "javbeacon-subs-card-action",
        scene: props.scene,
      })
    );
  });
})();
