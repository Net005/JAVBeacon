(function () {
  "use strict";

  const PLUGIN_ID = "javbeacon-realtime";
  const React = window.PluginApi.React;
  const ReactDOM = window.PluginApi.ReactDOM;
  const { Button, Spinner } = window.PluginApi.libraries.Bootstrap;
  const { gql, useLazyQuery, useMutation, useQuery } =
    window.PluginApi.libraries.Apollo;
  const sceneStatusCache = new Map();
  const sceneStatusRequests = new Map();

  const REQUEST_SUBTITLES = gql`
    mutation JAVBeaconRequestSubtitles($pluginId: ID!, $args: Map) {
      runPluginOperation(plugin_id: $pluginId, args: $args)
    }
  `;

  const UPDATE_SCENE_WATCHLIST = gql`
    mutation JAVBeaconUpdateSceneWatchlist($input: SceneUpdateInput!) {
      sceneUpdate(input: $input) {
        id
        tags {
          id
          name
        }
      }
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
        tags {
          id
          name
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
      // This partial configuration object has no cache identity and conflicts
      // with Stash's full Query.configuration result when Apollo merges it.
      fetchPolicy: "no-cache",
    });
    return {
      ...result,
      settings: result.data?.configuration?.plugins?.[PLUGIN_ID],
    };
  }

  function SubtitleButton({ sceneId, completed = false, resolveScene }) {
    const Toast = window.PluginApi.hooks.useToast();
    const [runPluginOperation] = useMutation(REQUEST_SUBTITLES);
    const [loading, setLoading] = React.useState(false);

    const onClick = async (event) => {
      event?.preventDefault();
      event?.stopPropagation();
      if (loading || completed) return;
      setLoading(true);
      try {
        if (resolveScene) {
          const scene = await resolveScene();
          if (hasLinkedSubtitles(scene)) {
            Toast.success("Subtitle already linked to this scene");
            return;
          }
        }
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

  function BeaconIcon() {
    return React.createElement(
      "svg",
      {
        "aria-hidden": "true",
        className: "javbeacon-release-icon",
        fill: "none",
        viewBox: "0 0 24 24",
      },
      React.createElement("path", {
        d: "M12 3v2M4.22 6.22l1.42 1.42M19.78 6.22l-1.42 1.42M2 13h3M19 13h3",
        stroke: "currentColor",
        strokeLinecap: "round",
        strokeWidth: "1.8",
      }),
      React.createElement("path", {
        d: "M8.4 17h7.2l-1.1-6.1A2.54 2.54 0 0 0 12 8.8a2.54 2.54 0 0 0-2.5 2.1L8.4 17Z",
        stroke: "currentColor",
        strokeLinejoin: "round",
        strokeWidth: "1.8",
      }),
      React.createElement("path", {
        d: "M7 20h10M10 17l-.5 3M14 17l.5 3",
        stroke: "currentColor",
        strokeLinecap: "round",
        strokeWidth: "1.8",
      })
    );
  }

  function ReleaseLinkButton({ sceneId }) {
    const Toast = window.PluginApi.hooks.useToast();
    const [runPluginOperation] = useMutation(REQUEST_SUBTITLES);
    const [loading, setLoading] = React.useState(false);

    const onClick = async (event) => {
      event?.preventDefault();
      event?.stopPropagation();
      if (loading) return;
      // Open synchronously so popup blockers do not discard the destination
      // while the authenticated server-side scene lookup is in progress.
      const target = window.open("about:blank", "_blank");
      if (target) target.opener = null;
      setLoading(true);
      try {
        const response = await runPluginOperation({
          variables: {
            pluginId: PLUGIN_ID,
            args: { mode: "release_link", scene_id: String(sceneId) },
          },
        });
        const url = response.data?.runPluginOperation?.url;
        if (!url) throw new Error("JAVBeacon did not return a release link");
        if (target) target.location.replace(url);
        else window.open(url, "_blank", "noopener");
      } catch (error) {
        target?.close();
        Toast.error(error instanceof Error ? error.message : String(error));
      } finally {
        setLoading(false);
      }
    };

    return React.createElement(
      Button,
      {
        "aria-label": "Open this release in JAVBeacon",
        className: "minimal javbeacon-release-button",
        disabled: loading,
        onClick,
        onMouseDown: (event) => event.stopPropagation(),
        title: loading
          ? "Finding JAVBeacon release…"
          : "Open release in JAVBeacon",
        variant: "secondary",
      },
      loading
        ? React.createElement(Spinner, {
            animation: "border",
            role: "status",
            size: "sm",
          })
        : React.createElement(BeaconIcon)
    );
  }

  function SceneCardWatchlistAction({ scene, settings, resolveScene }) {
    const Toast = window.PluginApi.hooks.useToast();
    const [updateScene] = useMutation(UPDATE_SCENE_WATCHLIST);
    const [pending, setPending] = React.useState(false);
    const [membershipOverride, setMembershipOverride] = React.useState(null);
    const tags = scene?.tags;
    const tagID = String(settings?.watchlist_tag_id || "").trim();
    const storedMembership =
      tagID !== "" &&
      Array.isArray(tags) &&
      tags.some((tag) => String(tag?.id) === tagID);
    const inWatchlist =
      membershipOverride == null ? storedMembership : membershipOverride;
    const configured = tagID !== "";
    const disabled = pending || settings == null || !configured;

    const onClick = async (event) => {
      event?.preventDefault();
      event?.stopPropagation();
      if (disabled) return;

      setPending(true);
      try {
        const currentScene = await resolveScene();
        const currentTags = currentScene?.tags;
        const currentMembership =
          Array.isArray(currentTags) &&
          currentTags.some((tag) => String(tag?.id) === tagID);
        const existingTagIDs = Array.isArray(currentTags)
          ? currentTags.map((tag) => String(tag?.id || "")).filter(Boolean)
          : [];
        const tagIDs = currentMembership
          ? existingTagIDs.filter((id) => id !== tagID)
          : Array.from(new Set([...existingTagIDs, tagID]));
        await updateScene({
          variables: { input: { id: String(scene.id), tag_ids: tagIDs } },
        });
        setMembershipOverride(!currentMembership);
        Toast.success(
          currentMembership ? "Removed from Watchlist" : "Added to Watchlist"
        );
      } catch (error) {
        Toast.error(error instanceof Error ? error.message : String(error));
      } finally {
        setPending(false);
      }
    };

    const title = !configured
      ? "Configure the Watchlist tag ID in plugin settings"
      : inWatchlist
        ? "In Watchlist · click to remove"
        : "Add to Watchlist";

    return React.createElement(
      "div",
      { className: "javbeacon-watchlist-card-action" },
      React.createElement(
        Button,
        {
          "aria-label": title,
          "aria-pressed": inWatchlist,
          className: `minimal javbeacon-watchlist-button${
            inWatchlist ? " is-watchlisted" : ""
          }`,
          disabled,
          onClick,
          onMouseDown: (event) => event.stopPropagation(),
          title,
          variant: "secondary",
        },
        pending
          ? React.createElement(Spinner, {
              animation: "border",
              role: "status",
              size: "sm",
            })
          : React.createElement(
              "span",
              { "aria-hidden": "true" },
              inWatchlist ? "✓ Watchlist" : "+ Watchlist"
            )
      )
    );
  }

  function SceneCardSubtitleAction({ scene, settings, resolveScene }) {
    if (settings == null || !sceneMatchesPathFilters(scene, settings)) {
      return null;
    }

    return React.createElement(
      "div",
      {
        className: "javbeacon-subs-card-action",
      },
      React.createElement(SubtitleButton, {
        completed: hasLinkedSubtitles(scene),
        resolveScene,
        sceneId: scene.id,
      })
    );
  }

  function SceneCardActions({ scene }) {
    const settingsQuery = usePluginSettings();
    const sceneID = String(scene.id);
    const [probe, setProbe] = React.useState(null);
    const [loadedScene, setLoadedScene] = React.useState(
      sceneStatusCache.get(sceneID) || null
    );
    const captionsKnown = Object.prototype.hasOwnProperty.call(scene, "captions");
    const tagsKnown = Object.prototype.hasOwnProperty.call(scene, "tags");
    const statusKnown = captionsKnown && tagsKnown;
    const [loadStatus] = useLazyQuery(FIND_SCENE_CAPTIONS, {
      fetchPolicy: "cache-first",
    });
    const resolvedScene = {
      ...scene,
      captions: captionsKnown ? scene.captions : loadedScene?.captions,
      tags: tagsKnown ? scene.tags : loadedScene?.tags,
    };
    const resolveScene = async () => {
      if (statusKnown) return scene;
      if (loadedScene) return resolvedScene;
      let request = sceneStatusRequests.get(sceneID);
      if (!request) {
        request = loadStatus({ variables: { id: sceneID } })
          .then((result) => result.data?.findScene)
          .finally(() => sceneStatusRequests.delete(sceneID));
        sceneStatusRequests.set(sceneID, request);
      }
      const found = await request;
      if (!found) throw new Error("Could not load scene status");
      sceneStatusCache.set(sceneID, found);
      setLoadedScene(found);
      return { ...scene, ...found };
    };
    React.useEffect(() => {
      if (!probe || statusKnown || loadedScene) return undefined;
      const card = probe.closest(".scene-card") || probe.parentElement;
      if (!card) return undefined;
      const checkStatus = () => {
        resolveScene().catch(() => {
          // A failed hover check must not interrupt card navigation.
        });
      };
      card.addEventListener("mouseenter", checkStatus, { once: true });
      return () => card.removeEventListener("mouseenter", checkStatus);
    }, [probe, sceneID, statusKnown, loadedScene]);
    const settings =
      settingsQuery.loading || settingsQuery.error
        ? null
        : settingsQuery.settings;

    return React.createElement(
      React.Fragment,
      null,
      React.createElement("span", {
        className: "javbeacon-card-actions-probe",
        ref: setProbe,
      }),
      React.createElement(SceneCardSubtitleAction, {
        scene: resolvedScene,
        settings,
        resolveScene,
      }),
      React.createElement(SceneCardWatchlistAction, {
        scene: resolvedScene,
        settings,
        resolveScene,
      })
    );
  }

  function ScenePageSubtitleAction({ scene }) {
    const { settings, loading, error } = usePluginSettings();
    const captionsKnown = Object.prototype.hasOwnProperty.call(scene, "captions");
    const statusQuery = useQuery(FIND_SCENE_CAPTIONS, {
      fetchPolicy: "cache-first",
      skip: captionsKnown,
      variables: { id: String(scene.id) },
    });
    const resolvedScene = captionsKnown
      ? scene
      : { ...scene, captions: statusQuery.data?.findScene?.captions };
    if (loading || error || settings == null) return null;
    const showSubtitles =
      sceneMatchesPathFilters(scene, settings) &&
      (captionsKnown ||
        (!statusQuery.loading && !statusQuery.error && statusQuery.data?.findScene));
    return React.createElement(SubtitleToolbarPortal, {
      completed: hasLinkedSubtitles(resolvedScene),
      sceneId: scene.id,
      showSubtitles,
    });
  }

  function SubtitleToolbarPortal({ sceneId, completed, showSubtitles = true }) {
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
      React.createElement(
        React.Fragment,
        null,
        showSubtitles
          ? React.createElement(SubtitleButton, { completed, sceneId })
          : null,
        React.createElement(ReleaseLinkButton, { sceneId })
      ),
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
      React.createElement(SceneCardActions, {
        key: "javbeacon-card-actions",
        scene: props.scene,
      })
    );
  });
})();
