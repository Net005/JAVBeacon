using MediaBrowser.Model.Tasks;

namespace Jellyfin.Plugin.JAVBeacon.Tasks;

/// <summary>
/// Catch-up counterpart to LibrarySyncService's continuous background poll.
/// The background loop already keeps the Watchlist collection and every
/// saved-filter-set collection in sync roughly every
/// LibrarySyncIntervalSeconds, and reacts to Stash/JAVBeacon changes through
/// the revision it polls for - but a missed poll (Jellyfin restart mid-cycle,
/// a JAVBeacon outage during the interval, the plugin being reloaded, etc.)
/// has no other safety net. This task runs the exact same full reconcile
/// pass on demand ("Run Now" in Jellyfin's Scheduled Tasks page) and on its
/// own default schedule, independent of whether the background loop happens
/// to be behind, so a missed realtime update is always eventually caught up.
/// </summary>
public sealed class SyncCollectionsTask(LibrarySyncService librarySync) : IScheduledTask
{
    public string Name => "Resync JAVBeacon collections (catch-up)";
    public string Key => "JAVBeaconSyncCollections";
    public string Description => "Forces a full resync of the Watchlist collection, every saved-filter-set collection, and watched status from StashApp - independent of the continuous background sync, so anything missed in realtime is caught up.";
    public string Category => "JAVBeacon";

    public async Task ExecuteAsync(IProgress<double> progress, CancellationToken cancellationToken)
    {
        progress.Report(0);
        await librarySync.RunFullSyncAsync(cancellationToken).ConfigureAwait(false);
        progress.Report(100);
    }

    // Runs on its own every 6 hours by default, in addition to being
    // available for "Run Now" or a custom admin-configured trigger - a
    // reasonable catch-up cadence given the background loop already
    // typically polls every 15-60+ seconds while enabled.
    public IEnumerable<TaskTriggerInfo> GetDefaultTriggers() =>
    [
        new TaskTriggerInfo { Type = TaskTriggerInfo.TriggerInterval, IntervalTicks = TimeSpan.FromHours(6).Ticks }
    ];
}
