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
  const historySyncTimers = new Map();

  // Stash's dedicated play/O/activity mutations bypass Scene.Update.Post.
  // Observe successful GraphQL mutations in the browser and invoke this
  // plugin's server-side operation, keeping the webhook secret out of the UI.
  const historyMutationFields = [
    "sceneAddPlay",
    "sceneDeletePlay",
    "sceneResetPlayCount",
    "sceneIncrementPlayCount",
    "sceneAddO",
    "sceneDeleteO",
    "sceneResetO",
    "sceneIncrementO",
    "sceneDecrementO",
    "sceneSaveActivity",
    "sceneResetActivity",
  ];

  function historySceneIDs(payload) {
    const operations = Array.isArray(payload) ? payload : [payload];
    return operations.flatMap((operation) => {
      const query = String(operation?.query || "");
      if (!historyMutationFields.some((field) => query.includes(field))) return [];
      const id = String(operation?.variables?.id || "").trim();
      return id ? [id] : [];
    });
  }

  function installHistoryMutationBridge() {
    if (window.__javbeaconHistoryMutationBridge || typeof window.fetch !== "function") return;
    window.__javbeaconHistoryMutationBridge = true;
    const originalFetch = window.fetch.bind(window);
    window.fetch = async function javbeaconHistoryAwareFetch(input, init) {
      let payload;
      try {
        const rawBody = typeof init?.body === "string"
          ? init.body
          : input instanceof Request
            ? await input.clone().text()
            : "";
        payload = rawBody ? JSON.parse(rawBody) : null;
      } catch (_) {
        payload = null;
      }
      const sceneIDs = historySceneIDs(payload);
      const response = await originalFetch(input, init);
      if (!response.ok || sceneIDs.length === 0) return response;
      try {
        const result = await response.clone().json();
        const results = Array.isArray(result) ? result : [result];
        if (results.some((entry) => Array.isArray(entry?.errors) && entry.errors.length)) return response;
      } catch (_) {
        return response;
      }
      const endpoint = input instanceof Request ? input.url : input;
      const sourceHeaders = input instanceof Request ? input.headers : init?.headers;
      for (const sceneID of new Set(sceneIDs)) {
        clearTimeout(historySyncTimers.get(sceneID));
        historySyncTimers.set(sceneID, setTimeout(() => {
          historySyncTimers.delete(sceneID);
          const headers = new Headers(sourceHeaders || {});
          headers.set("Content-Type", "application/json");
          originalFetch(endpoint, {
            credentials: "same-origin",
            headers,
            method: "POST",
            body: JSON.stringify({
              operationName: "JAVBeaconRealtimeHistorySync",
              query: "mutation JAVBeaconRealtimeHistorySync($pluginId: ID!, $args: Map) { runPluginOperation(plugin_id: $pluginId, args: $args) }",
              variables: { pluginId: PLUGIN_ID, args: { mode: "history", scene_id: sceneID } },
            }),
          }).catch(() => {
            // The scheduled full sync remains the reconciliation fallback.
          });
        }, 3000));
      }
      return response;
    };
  }

  installHistoryMutationBridge();

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
        details
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

  // Asks the server-side plugin whether the scene's existing .en.srt.json
  // sidecar (if any) already matches JAVBeacon-Subs's current transcription
  // and translation backend, then confirms with wording appropriate to that
  // answer instead of a single generic "replace subtitles?" prompt:
  //   - no sidecar found: subtitles predate version tracking, treated as
  //     outdated (an older subtitle translator).
  //   - sidecar found but older than the current backend: a normal upgrade
  //     prompt naming both backends.
  //   - sidecar already matches the current backend: subtitles are up to
  //     date and overwriting is discouraged, but a second, explicitly
  //     labeled "force overwrite" confirmation still allows it.
  //   - freshness could not be determined (older JAVBeacon-Subs release
  //     without the status endpoint, or a failed request): falls back to
  //     the original plain confirmation so nothing regresses.
  async function confirmSubtitleOverwrite(sceneId, runPluginOperation) {
    let status = null;
    try {
      const response = await runPluginOperation({
        variables: {
          pluginId: PLUGIN_ID,
          args: { mode: "subtitle_status", scene_id: String(sceneId) },
        },
      });
      status = response.data?.runPluginOperation || null;
    } catch (_) {
      status = null;
    }

    const backendLabel = (backends) =>
      `${backends?.transcription_backend || "unknown"} / ${backends?.translation_backend || "unknown"}`;

    if (!status || !status.sidecar_found) {
      return window.confirm(
        "🕰️ Old subtitles\n" +
          "These predate JAVBeacon-Subs version tracking.\n\n" +
          "Replace them with a new result?"
      );
    }

    if (status.up_to_date === false) {
      return window.confirm(
        "🆕 Newer backend available\n" +
          `Sidecar:   ${backendLabel(status.sidecar_backends)}\n` +
          `Current: ${backendLabel(status.current_backends)}\n\n` +
          "Replace the existing subtitles with a new JAVBeacon-Subs result?"
      );
    }

    if (status.up_to_date === true) {
      if (
        !window.confirm(
          "✅ Already up to date\n" +
            `Backend: ${backendLabel(status.current_backends)}\n\n` +
            "Regenerating is not recommended. Continue anyway?"
        )
      ) {
        return false;
      }
      return window.confirm(
        "⚠️ FORCE OVERWRITE\n" +
          "This discards up-to-date subtitles and regenerates\n" +
          "them with the SAME backend. Not recommended.\n\n" +
          "Continue?"
      );
    }

    // status.up_to_date === null: a sidecar exists but JAVBeacon-Subs did
    // not report its current backend (older release, or the check failed).
    return window.confirm(
      "🎬 Existing subtitles\n" +
        "This scene already has subtitles.\n\n" +
        "Replace them with a new JAVBeacon-Subs result?"
    );
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
      if (loading) return;
      setLoading(true);
      try {
        let overwrite = completed;
        if (resolveScene) {
          const scene = await resolveScene();
          overwrite = hasLinkedSubtitles(scene);
        }
        if (overwrite && !(await confirmSubtitleOverwrite(sceneId, runPluginOperation))) {
          return;
        }
        const response = await runPluginOperation({
          variables: {
            pluginId: PLUGIN_ID,
            args: { mode: "subtitles", scene_id: String(sceneId), overwrite },
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
          ? "Request replacement subtitles for this scene"
          : "Request subtitles from JAVBeacon-Subs",
        className: `minimal javbeacon-subs-button${
          completed ? " javbeacon-subs-complete" : ""
        }`,
        disabled: loading,
        onClick,
        onMouseDown: (event) => event.stopPropagation(),
        title: loading
          ? "Sending subtitle request…"
          : completed
            ? "Request replacement subtitles (confirmation required)"
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

  function sceneStory(scene) {
    return String(scene?.details || "")
      .replace(/\s+/g, " ")
      .trim();
  }

  function SceneCardStory({ scene }) {
    const [probe, setProbe] = React.useState(null);
    const [mountNode, setMountNode] = React.useState(null);
    const [expanded, setExpanded] = React.useState(false);
    const story = sceneStory(scene);

    React.useLayoutEffect(() => {
      if (!probe || !story) return undefined;
      const card = probe.closest(".scene-card") || probe.parentElement;
      if (!card) return undefined;

      const title = card.querySelector(
        ".card-section-title, .scene-card-title, .scene-card__title, .card-title"
      );
      const section =
        title?.closest(".card-section") || card.querySelector(".card-section");
      if (!section) return undefined;

      const mount = document.createElement("div");
      mount.className = "javbeacon-scene-story-mount";
      const titleContainer = title?.parentElement;
      if (titleContainer?.parentElement === section) {
        section.insertBefore(mount, titleContainer.nextSibling);
      } else if (title?.parentElement === section) {
        section.insertBefore(mount, title.nextSibling);
      } else section.prepend(mount);
      setMountNode(mount);

      return () => mount.remove();
    }, [probe, story]);

    React.useEffect(() => setExpanded(false), [scene?.id, story]);

    const toggle = (event) => {
      event?.preventDefault();
      event?.stopPropagation();
      setExpanded((value) => !value);
    };
    const onKeyDown = (event) => {
      if (event.key !== "Enter" && event.key !== " ") return;
      toggle(event);
    };
    const content = React.createElement(
      "div",
      {
        "aria-expanded": expanded,
        "aria-label": `Scene details: ${story}`,
        className: `javbeacon-scene-story${expanded ? " is-expanded" : ""}`,
        onClick: toggle,
        onKeyDown,
        onMouseDown: (event) => event.stopPropagation(),
        role: "button",
        tabIndex: 0,
        title: story,
      },
      story
    );

    return React.createElement(
      React.Fragment,
      null,
      React.createElement("span", {
        className: "javbeacon-card-actions-probe",
        ref: setProbe,
      }),
      mountNode ? ReactDOM.createPortal(content, mountNode) : null
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
    const detailsKnown = Object.prototype.hasOwnProperty.call(scene, "details");
    const statusKnown = captionsKnown && tagsKnown && detailsKnown;
    const [loadStatus] = useLazyQuery(FIND_SCENE_CAPTIONS, {
      fetchPolicy: "cache-first",
    });
    const resolvedScene = {
      ...scene,
      captions: captionsKnown ? scene.captions : loadedScene?.captions,
      tags: tagsKnown ? scene.tags : loadedScene?.tags,
      details: detailsKnown ? scene.details : loadedScene?.details,
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
      React.createElement(SceneCardStory, { scene: resolvedScene }),
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
