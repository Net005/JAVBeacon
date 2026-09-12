using MediaBrowser.Model.Tasks;

namespace Jellyfin.Plugin.JAVBeacon.Tasks;

/// <summary>
/// An optional, on-demand/schedulable counterpart to the always-running
/// background sync: "Sync watched status from StashApp" in the JAVBeacon
/// plugin settings already reconciles played state every
/// LibrarySyncIntervalSeconds while enabled. This task runs the exact same
/// reconciliation once, so an admin can trigger it manually from Jellyfin's
/// own Scheduled Tasks page ("Run Now"), or add their own trigger (daily,
/// hourly, ...) independent of the plugin's continuous loop - useful right
/// after turning the setting on, or if the background sync was ever
/// disabled and only periodic catch-up is wanted.
/// </summary>
public sealed class SyncWatchedStatusTask(JAVBeaconClient client, WatchedStatusSynchronizer synchronizer) : IScheduledTask
{
    public string Name => "Sync watched status from StashApp";
    public string Key => "JAVBeaconSyncWatchedStatus";
    public string Description => "Marks JAVBeacon items played in Jellyfin for every scene StashApp reports as watched (play_count > 0), fetched through JAVBeacon. One-directional: never marks anything unwatched. Applies to the plugin's Tracked Jellyfin user IDs setting, or every user when it is blank.";
    public string Category => "JAVBeacon";

    public async Task ExecuteAsync(IProgress<double> progress, CancellationToken cancellationToken)
    {
        progress.Report(0);
        var snapshot = await client.LibrarySync(cancellationToken).ConfigureAwait(false);
        progress.Report(50);
        var trackedUserIds = Plugin.Instance?.Configuration.TrackedUserIds ?? [];
        if (snapshot is not null)
        {
            synchronizer.Synchronize(snapshot.Watched, trackedUserIds);
        }
        progress.Report(100);
    }

    public IEnumerable<TaskTriggerInfo> GetDefaultTriggers() => [];
}
