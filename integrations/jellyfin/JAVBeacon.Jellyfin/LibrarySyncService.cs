using Jellyfin.Data.Enums;
using MediaBrowser.Controller.Collections;
using MediaBrowser.Controller.Entities;
using MediaBrowser.Controller.Entities.Movies;
using MediaBrowser.Controller.Library;
using MediaBrowser.Controller.Providers;
using MediaBrowser.Model.Entities;
using Microsoft.Extensions.Hosting;
using Microsoft.Extensions.Logging;

namespace Jellyfin.Plugin.JAVBeacon;

public sealed class LibrarySyncService(
    ILibraryManager library,
    ICollectionManager collections,
    IProviderManager providerManager,
    WatchedStatusSynchronizer watchedSync,
    JAVBeaconClient client,
    ILogger<LibrarySyncService> logger) : BackgroundService
{
    // Every JAVBeacon-managed saved-filter-set collection carries this
    // ProviderId, with the JAVBeacon filter preset's own (stable) ID as its
    // value, so a renamed filter set updates its existing collection instead
    // of creating a duplicate, and a deleted filter set's collection can be
    // found and removed even though its name no longer matches anything in
    // the current snapshot.
    private const string FilterPresetProviderId = "JAVBeaconFilterPreset";

    private string? _revision;

    protected override async Task ExecuteAsync(CancellationToken stoppingToken)
    {
        while (!stoppingToken.IsCancellationRequested)
        {
            var config = Plugin.Instance?.Configuration;
            var interval = TimeSpan.FromSeconds(Math.Max(config?.LibrarySyncIntervalSeconds ?? 60, 15));
            try
            {
                if (config is not null && (config.EnableWatchlistCollection || config.EnableFilterPresetCollections || config.ScanLibraryOnStashChanges || config.SyncWatchedFromStash))
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
                            var name = string.IsNullOrWhiteSpace(config.WatchlistCollectionName) ? "Watchlist" : config.WatchlistCollectionName.Trim();
                            var orderedWatchlist = snapshot.Watchlist
                                .OrderByDescending(x => x.WatchlistedAt ?? DateTimeOffset.MinValue)
                                .ThenByDescending(x => x.ReleaseId)
                                .Select(x => x.ReleaseId)
                                .ToArray();
                            await ReconcileCollection(name, orderedWatchlist, forceImageRefresh: false, stoppingToken).ConfigureAwait(false);
                        }
                        if (config.EnableFilterPresetCollections)
                        {
                            await ReconcileFilterPresetCollections(snapshot.FilterPresets, config.FilterPresetCollectionPrefix, forceImageRefresh: false, stoppingToken).ConfigureAwait(false);
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
                logger.LogWarning(ex, "Unable to synchronize JAVBeacon Jellyfin collections");
            }
            await Task.Delay(interval, stoppingToken).ConfigureAwait(false);
        }
    }

    /// <summary>
    /// Forces one full pass right now, independent of the polling loop above
    /// and its cached revision - used by the scheduled catch-up tasks so an
    /// admin-triggered (or cron-triggered) run always does real work instead
    /// of silently no-op'ing because the revision hasn't changed since the
    /// last poll.
    /// </summary>
    public async Task RunFullSyncAsync(CancellationToken ct)
    {
        var config = Plugin.Instance?.Configuration;
        if (config is null) return;
        var snapshot = await client.LibrarySync(ct).ConfigureAwait(false);
        if (snapshot is null) return;
        _revision = snapshot.Revision;
        if (config.EnableWatchlistCollection)
        {
            var name = string.IsNullOrWhiteSpace(config.WatchlistCollectionName) ? "Watchlist" : config.WatchlistCollectionName.Trim();
            var orderedWatchlist = snapshot.Watchlist
                .OrderByDescending(x => x.WatchlistedAt ?? DateTimeOffset.MinValue)
                .ThenByDescending(x => x.ReleaseId)
                .Select(x => x.ReleaseId)
                .ToArray();
            // forceImageRefresh: true - this path runs from the scheduled
            // catch-up task (every 6h, or "Run Now"), which is also the
            // occasion this plugin uses to rotate each collection's cover to
            // a freshly-picked member image (see EnsureCollectionImage).
            await ReconcileCollection(name, orderedWatchlist, forceImageRefresh: true, ct).ConfigureAwait(false);
        }
        if (config.EnableFilterPresetCollections)
        {
            await ReconcileFilterPresetCollections(snapshot.FilterPresets, config.FilterPresetCollectionPrefix, forceImageRefresh: true, ct).ConfigureAwait(false);
        }
        if (config.SyncWatchedFromStash)
        {
            watchedSync.Synchronize(snapshot.Watched, config.TrackedUserIds);
        }
    }

    private async Task ReconcileFilterPresetCollections(IReadOnlyList<Models.FilterPresetCollectionDto>? presets, string? prefix, bool forceImageRefresh, CancellationToken ct)
    {
        // Defensive: a JAVBeacon instance with zero saved filter presets (or
        // an older/differently-behaving server) can serialize
        // filter_presets as JSON null rather than an empty array, which
        // System.Text.Json deserializes as a literal null - overriding
        // LibrarySyncDto.FilterPresets's own "= []" default initializer.
        // Confirmed live: this crashed the very next foreach with a
        // NullReferenceException and took down the whole scheduled task.
        // JAVBeacon itself no longer emits that null (see
        // internal/jellyfin.Service.collectionIndexAndPresets), but this
        // guard costs nothing and avoids depending on that alone.
        presets ??= [];
        prefix ??= string.Empty;
        // Same defensive shape as ReconcileCollection below: guard against two
        // BoxSets somehow carrying the same FilterPresetProviderId (a
        // duplicate/partially-failed collection creation) crashing this
        // entire task, rather than assuming it can never happen.
        var existingByPresetId = new Dictionary<string, BoxSet>(StringComparer.Ordinal);
        foreach (var group in library.GetItemList(new InternalItemsQuery
        {
            Recursive = true,
            IncludeItemTypes = [BaseItemKind.BoxSet]
        }).OfType<BoxSet>()
            .Where(x => x.ProviderIds.ContainsKey(FilterPresetProviderId))
            .GroupBy(x => x.ProviderIds[FilterPresetProviderId], StringComparer.Ordinal))
        {
            existingByPresetId[group.Key] = group.First();
            if (group.Count() > 1)
            {
                logger.LogWarning("Multiple Jellyfin collections share JAVBeaconFilterPreset id {PresetId} - using one and leaving the rest untouched", group.Key);
            }
        }

        var seenPresetIds = new HashSet<string>(StringComparer.Ordinal);
        foreach (var preset in presets)
        {
            var presetId = preset.Id.ToString(System.Globalization.CultureInfo.InvariantCulture);
            seenPresetIds.Add(presetId);
            var name = string.IsNullOrWhiteSpace(prefix) ? preset.Name : prefix + preset.Name;
            existingByPresetId.TryGetValue(presetId, out var existing);
            await ReconcileCollection(name, preset.ReleaseIds, forceImageRefresh, ct, existing, FilterPresetProviderId, presetId).ConfigureAwait(false);
        }

        // A filter set that was deleted (or whose collection lost its
        // provider-id tag some other way) has no matching entry in this
        // sync's presets anymore - remove its now-orphaned collection rather
        // than leaving a stale, no-longer-updated one behind.
        foreach (var (presetId, orphan) in existingByPresetId)
        {
            if (seenPresetIds.Contains(presetId)) continue;
            library.DeleteItem(orphan, new DeleteOptions { DeleteFileLocation = false });
            logger.LogInformation("Removed Jellyfin collection {CollectionName} for a deleted JAVBeacon filter set", orphan.Name);
        }
    }

    private async Task ReconcileCollection(string name, IReadOnlyList<long> desiredReleaseIds, bool forceImageRefresh, CancellationToken ct, BoxSet? existing = null, string? providerIdKey = null, string? providerIdValue = null)
    {
        var desiredIdStrings = desiredReleaseIds.Select(x => x.ToString(System.Globalization.CultureInfo.InvariantCulture)).ToHashSet(StringComparer.OrdinalIgnoreCase);
        var javItems = library.GetItemList(new InternalItemsQuery
        {
            Recursive = true,
            IncludeItemTypes = [BaseItemKind.Movie],
            IsVirtualItem = false
        }).Where(x => x.ProviderIds.ContainsKey("JAVBeacon")).ToArray();
        // GroupBy+pick-first, not ToDictionary: two Jellyfin library items can
        // legitimately carry the same "JAVBeacon" provider id (the same
        // release reachable through two library paths, a duplicate scan
        // entry, a symlink counted twice, etc.) - a real observed crash
        // ("An item with the same key has already been added") took down
        // this entire scheduled task over exactly that. Deterministically
        // keeping one item per release (rather than throwing) avoids adding
        // the same release twice into one collection; a warning is logged so
        // the duplicate can be found and cleaned up in the library itself.
        var javItemsByReleaseId = new Dictionary<string, BaseItem>(StringComparer.OrdinalIgnoreCase);
        foreach (var group in javItems
            .Where(x => x.ProviderIds.TryGetValue("JAVBeacon", out var id) && desiredIdStrings.Contains(id))
            .GroupBy(x => x.ProviderIds["JAVBeacon"], StringComparer.OrdinalIgnoreCase))
        {
            javItemsByReleaseId[group.Key] = group.First();
            if (group.Count() > 1)
            {
                logger.LogWarning("Multiple Jellyfin library items share JAVBeacon release id {ReleaseId} ({Paths}) - using one and ignoring the rest for collection {CollectionName}", group.Key, string.Join(", ", group.Select(x => x.Path)), name);
            }
        }
        var desiredItems = desiredReleaseIds
            .Select(x => x.ToString(System.Globalization.CultureInfo.InvariantCulture))
            .Where(javItemsByReleaseId.ContainsKey)
            .Select(id => javItemsByReleaseId[id])
            .ToArray();

        var collection = existing ?? library.GetItemList(new InternalItemsQuery
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
            if (providerIdKey is not null && providerIdValue is not null)
            {
                collection.ProviderIds[providerIdKey] = providerIdValue;
            }
            await ApplyOrder(collection, desiredItems, javItems).ConfigureAwait(false);
            await EnsureCollectionImage(collection, desiredItems, force: true, ct).ConfigureAwait(false);
            logger.LogInformation("Created Jellyfin collection {CollectionName} with {Count} JAVBeacon items", name, desiredItems.Length);
            return;
        }

        var renamed = !string.Equals(collection.Name, name, StringComparison.Ordinal);
        if (renamed)
        {
            collection.Name = name;
        }

        var currentIds = collection.GetLinkedChildren().Select(x => x.Id).ToHashSet();
        var desiredIds = desiredItems.Select(x => x.Id).ToHashSet();
        var add = desiredIds.Except(currentIds).ToArray();
        // Preserve unrelated/manual members; only stale JAVBeacon-managed items
        // are removed from an existing collection.
        var remove = javItems.Where(x => currentIds.Contains(x.Id) && !desiredIds.Contains(x.Id)).Select(x => x.Id).ToArray();
        if (add.Length > 0) await collections.AddToCollectionAsync(collection.Id, add).ConfigureAwait(false);
        if (remove.Length > 0) await collections.RemoveFromCollectionAsync(collection.Id, remove).ConfigureAwait(false);
        var reordered = await ApplyOrder(collection, desiredItems, javItems).ConfigureAwait(false);
        await EnsureCollectionImage(collection, desiredItems, forceImageRefresh, ct).ConfigureAwait(false);
        if (renamed || add.Length > 0 || remove.Length > 0 || reordered)
        {
            if (renamed) await collection.UpdateToRepositoryAsync(ItemUpdateType.MetadataEdit, CancellationToken.None).ConfigureAwait(false);
            logger.LogInformation("Synchronized Jellyfin collection {CollectionName}: added {Added}, removed {Removed}", name, add.Length, remove.Length);
        }
    }

    // Jellyfin no longer reliably auto-generates a stacked thumbnail for a
    // collection from its members' own images, so a JAVBeacon-managed
    // collection can otherwise stay imageless indefinitely. EnsureCollectionImage
    // sets one directly: with force=false it only fills a genuinely missing
    // Primary image (cheap, safe to call on every regular poll); with
    // force=true (the 6-hourly/"Run Now" catch-up task) it also rotates an
    // existing image to a freshly-picked member, so the art doesn't stay
    // fixed on whichever release happened to be first. The pick prefers
    // newer releases (by PremiereDate) without being strictly the newest
    // every time - a small weighted-random pool, not a fixed pick.
    private async Task EnsureCollectionImage(BoxSet collection, IReadOnlyList<BaseItem> desiredItems, bool force, CancellationToken ct)
    {
        if (desiredItems.Count == 0) return;
        if (!force && collection.HasImage(ImageType.Primary)) return;

        var candidates = desiredItems.OrderByDescending(x => x.PremiereDate ?? DateTime.MinValue).ToArray();
        var poolSize = Math.Max(3, (int)Math.Ceiling(candidates.Length * 0.34));
        var pool = candidates.Take(poolSize).ToArray();
        var picked = pool[Random.Shared.Next(pool.Length)];
        if (!picked.ProviderIds.TryGetValue("JAVBeacon", out var raw) || !long.TryParse(raw, System.Globalization.CultureInfo.InvariantCulture, out var releaseId))
        {
            return;
        }
        try
        {
            var dto = await client.Metadata(releaseId, ct).ConfigureAwait(false);
            if (dto is null || string.IsNullOrWhiteSpace(dto.CoverPath)) return;
            // AbsoluteWithApiKey, not Absolute - SaveImage below fetches this
            // URL itself with a bare, unauthenticated client (see its doc
            // comment in JAVBeaconClient.cs).
            var url = client.AbsoluteWithApiKey(dto.CoverPath);
            await providerManager.SaveImage(collection, url, ImageType.Primary, null, ct).ConfigureAwait(false);
        }
        catch (Exception ex)
        {
            logger.LogWarning(ex, "Unable to set a cover image for Jellyfin collection {CollectionName}", collection.Name);
        }
    }

    private static async Task<bool> ApplyOrder(BoxSet collection, IReadOnlyList<BaseItem> desiredItems, IReadOnlyList<BaseItem> allJavItems)
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
        // LinkedChildren order instead of re-sorting by premiere date. This
        // is what makes a filter-preset collection's order match JAVBeacon's
        // own sort exactly: desiredItems already arrives pre-sorted from the
        // server (see internal/jellyfin's resolveFilterReleaseIDs), so
        // LinkedChildren order is simply that sort, unchanged.
        collection.DisplayOrder = ItemSortBy.Default.ToString();
        collection.LinkedChildren = orderedLinks;
        await collection.UpdateToRepositoryAsync(ItemUpdateType.MetadataEdit, CancellationToken.None).ConfigureAwait(false);
        return true;
    }
}
