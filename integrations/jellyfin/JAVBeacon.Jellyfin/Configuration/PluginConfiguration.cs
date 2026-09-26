using MediaBrowser.Model.Plugins;

namespace Jellyfin.Plugin.JAVBeacon.Configuration;

public sealed class PluginConfiguration : BasePluginConfiguration
{
    public string JAVBeaconUrl { get; set; } = "http://javbeacon:8080";
    public string ApiKey { get; set; } = string.Empty;
    public bool EnableMetadata { get; set; } = true;
    public bool EnablePlayback { get; set; } = true;
    public bool EnableWebActivity { get; set; } = false;
    public bool EnableWatchlistCollection { get; set; } = false;
    public string WatchlistCollectionName { get; set; } = "Watchlist";
    // EnableFilterPresetCollections creates and keeps in sync one Jellyfin
    // collection per saved filter set from the Release Library, each sorted
    // exactly like JAVBeacon itself sorts it (see LibrarySyncSnapshot's
    // FilterPresets - it is already fully resolved and ordered server-side).
    public bool EnableFilterPresetCollections { get; set; } = false;
    // FilterPresetCollectionPrefix is prepended to every saved filter set's
    // own name to form its Jellyfin collection name, so these collections
    // are easy to tell apart from manually-created ones at a glance. Blank
    // uses the filter set's name unchanged.
    public string FilterPresetCollectionPrefix { get; set; } = string.Empty;
    // SyncWatchedFromStash marks a Jellyfin item played (and stamps its last
    // played date) once StashApp reports play_count > 0 for the matching
    // scene - regardless of whether that scene was ever played through
    // Jellyfin itself. One-directional: it never marks anything unwatched,
    // since Jellyfin's own play state may be more current than the last
    // library sync. Applies to TrackedUserIds, or every user when that list
    // is empty, matching how EnablePlayback already scopes its own tracking.
    public bool SyncWatchedFromStash { get; set; } = false;
    public bool ScanLibraryOnStashChanges { get; set; } = true;
    public int LibrarySyncIntervalSeconds { get; set; } = 60;
    public string[] TrackedUserIds { get; set; } = [];
}
