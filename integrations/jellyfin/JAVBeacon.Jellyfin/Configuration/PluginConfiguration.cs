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
    public bool ScanLibraryOnStashChanges { get; set; } = true;
    public int LibrarySyncIntervalSeconds { get; set; } = 60;
    public string[] TrackedUserIds { get; set; } = [];
}
