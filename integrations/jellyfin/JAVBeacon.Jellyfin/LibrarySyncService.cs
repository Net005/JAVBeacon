using Jellyfin.Data.Enums;
using MediaBrowser.Controller.Collections;
using MediaBrowser.Controller.Entities;
using MediaBrowser.Controller.Entities.Movies;
using MediaBrowser.Controller.Library;
using MediaBrowser.Controller.Providers;
using Microsoft.Extensions.Hosting;
using Microsoft.Extensions.Logging;

namespace Jellyfin.Plugin.JAVBeacon;

public sealed class LibrarySyncService(
    ILibraryManager library,
    ICollectionManager collections,
    WatchedStatusSynchronizer watchedSync,
    JAVBeaconClient client,
    ILogger<LibrarySyncService> logger) : BackgroundService
{
    private string? _revision;

    protected override async Task ExecuteAsync(CancellationToken stoppingToken)
    {
        while (!stoppingToken.IsCancellationRequested)
        {
            var config = Plugin.Instance?.Configuration;
            var interval = TimeSpan.FromSeconds(Math.Max(config?.LibrarySyncIntervalSeconds ?? 60, 15));
            try
            {
                if (config is not null && (config.EnableWatchlistCollection || config.ScanLibraryOnStashChanges || config.SyncWatchedFromStash))
                {
                    var snapshot = await client.LibrarySync(stoppingToken).ConfigureAwait(false);
                    if (snapshot is not null)
                    {
                        var changed = _revision is not null && !string.Equals(_revision, snapshot.Revision, StringComparison.Ordinal);
                        var firstRun = _revision is null;
                        _revision = snapshot.Revision;
                        if (config.ScanLibraryOnStashChanges && (changed || firstRun))
                        {
                            library.QueueLibraryScan();
                            logger.LogInformation("Queued Jellyfin library scan after JAVBeacon/Stash change");
                        }
                        if (config.EnableWatchlistCollection)
                        {
                            await ReconcileCollection(snapshot.Watchlist, config.WatchlistCollectionName).ConfigureAwait(false);
                        }
                        if (config.SyncWatchedFromStash)
                        {
                            watchedSync.Synchronize(snapshot.Watched, config.TrackedUserIds);
                        }
                    }
                }
            }
            catch (OperationCanceledException) when (stoppingToken.IsCancellationRequested)
            {
                break;
            }
            catch (Exception ex)
            {
                logger.LogWarning(ex, "Unable to synchronize the JAVBeacon Watchlist collection");
            }
            await Task.Delay(interval, stoppingToken).ConfigureAwait(false);
        }
    }

    private async Task ReconcileCollection(IEnumerable<Models.LibrarySyncItemDto> watchlist, string configuredName)
    {
        var name = string.IsNullOrWhiteSpace(configuredName) ? "Watchlist" : configuredName.Trim();
        var orderedWatchlist = watchlist
            .OrderByDescending(x => x.WatchlistedAt ?? DateTimeOffset.MinValue)
            .ThenByDescending(x => x.ReleaseId)
            .ToArray();
        var desiredReleaseIds = orderedWatchlist.Select(x => x.ReleaseId.ToString(System.Globalization.CultureInfo.InvariantCulture)).ToHashSet(StringComparer.OrdinalIgnoreCase);
        var javItems = library.GetItemList(new InternalItemsQuery
        {
            Recursive = true,
            IncludeItemTypes = [BaseItemKind.Movie],
            IsVirtualItem = false
        }).Where(x => x.ProviderIds.ContainsKey("JAVBeacon")).ToArray();
        var javItemsByReleaseId = javItems
            .Where(x => x.ProviderIds.TryGetValue("JAVBeacon", out var id) && desiredReleaseIds.Contains(id))
            .ToDictionary(x => x.ProviderIds["JAVBeacon"], StringComparer.OrdinalIgnoreCase);
        var desiredItems = orderedWatchlist
            .Select(x => x.ReleaseId.ToString(System.Globalization.CultureInfo.InvariantCulture))
            .Where(javItemsByReleaseId.ContainsKey)
            .Select(id => javItemsByReleaseId[id])
            .ToArray();
        var collection = library.GetItemList(new InternalItemsQuery
        {
            Recursive = true,
            IncludeItemTypes = [BaseItemKind.BoxSet]
        }).OfType<BoxSet>().FirstOrDefault(x => string.Equals(x.Name, name, StringComparison.OrdinalIgnoreCase));

        if (collection is null)
        {
            if (desiredItems.Length == 0) return;
            collection = await collections.CreateCollectionAsync(new CollectionCreationOptions
            {
                Name = name,
                ItemIdList = desiredItems.Select(x => x.Id.ToString("N")).ToArray()
            }).ConfigureAwait(false);
            await ApplyWatchlistOrder(collection, desiredItems, javItems).ConfigureAwait(false);
            logger.LogInformation("Created Jellyfin collection {CollectionName} with {Count} JAVBeacon items", name, desiredItems.Length);
            return;
        }

        var currentIds = collection.GetLinkedChildren().Select(x => x.Id).ToHashSet();
        var desiredIds = desiredItems.Select(x => x.Id).ToHashSet();
        var add = desiredIds.Except(currentIds).ToArray();
        // Preserve unrelated/manual members; only stale JAVBeacon-managed items
        // are removed from an existing collection.
        var remove = javItems.Where(x => currentIds.Contains(x.Id) && !desiredIds.Contains(x.Id)).Select(x => x.Id).ToArray();
        if (add.Length > 0) await collections.AddToCollectionAsync(collection.Id, add).ConfigureAwait(false);
        if (remove.Length > 0) await collections.RemoveFromCollectionAsync(collection.Id, remove).ConfigureAwait(false);
        var reordered = await ApplyWatchlistOrder(collection, desiredItems, javItems).ConfigureAwait(false);
        if (add.Length > 0 || remove.Length > 0 || reordered)
        {
            logger.LogInformation("Synchronized Jellyfin collection {CollectionName}: added {Added}, removed {Removed}, newest Watchlist items first", name, add.Length, remove.Length);
        }
    }

    private static async Task<bool> ApplyWatchlistOrder(BoxSet collection, IReadOnlyList<BaseItem> desiredItems, IReadOnlyList<BaseItem> allJavItems)
    {
        var javItemIds = allJavItems.Select(x => x.Id).ToHashSet();
        var manualLinks = collection.LinkedChildren
            .Where(link => !link.ItemId.HasValue || !javItemIds.Contains(link.ItemId.Value))
            .ToArray();
        var orderedLinks = desiredItems.Select(LinkedChild.Create).Concat(manualLinks).ToArray();
        var changed = !string.Equals(collection.DisplayOrder, ItemSortBy.Default.ToString(), StringComparison.Ordinal)
            || !collection.LinkedChildren.Select(x => x.ItemId).SequenceEqual(orderedLinks.Select(x => x.ItemId));
        if (!changed) return false;

        // BoxSet is pre-sorted. DisplayOrder=Default makes Jellyfin honor the
        // LinkedChildren order instead of re-sorting by premiere date.
        collection.DisplayOrder = ItemSortBy.Default.ToString();
        collection.LinkedChildren = orderedLinks;
        await collection.UpdateToRepositoryAsync(ItemUpdateType.MetadataEdit, CancellationToken.None).ConfigureAwait(false);
        return true;
    }
}
