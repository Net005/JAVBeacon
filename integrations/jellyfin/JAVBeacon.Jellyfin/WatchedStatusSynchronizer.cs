using System.Globalization;
using Jellyfin.Data.Enums;
using Jellyfin.Plugin.JAVBeacon.Models;
using MediaBrowser.Controller.Entities;
using MediaBrowser.Controller.Entities.Movies;
using MediaBrowser.Controller.Library;
using MediaBrowser.Model.Entities;
using Microsoft.Extensions.Logging;

namespace Jellyfin.Plugin.JAVBeacon;

/// <summary>
/// Marks JAVBeacon items played in Jellyfin once StashApp reports them
/// watched (play_count &gt; 0), for every tracked user (or every user when
/// none are configured). Shared by <see cref="LibrarySyncService"/>'s
/// continuous background loop and the on-demand/scheduled
/// "Sync watched status from StashApp" Jellyfin task, so both act on
/// identical, tested logic instead of two drifting copies.
///
/// Deliberately one-directional and idempotent: it only ever moves Played
/// from false to true, so it is safe to re-run as often as either caller
/// likes, and it never clears Played back to false - Jellyfin's own play
/// state always wins once set, since this sync has no way to know whether a
/// later "unwatched" in Jellyfin was intentional.
/// </summary>
public sealed class WatchedStatusSynchronizer(ILibraryManager library, IUserManager users, IUserDataManager userData, ILogger<WatchedStatusSynchronizer> logger)
{
    public int Synchronize(IReadOnlyCollection<LibrarySyncItemDto> watched, string[] trackedUserIds)
    {
        if (watched.Count == 0) return 0;
        var desiredReleaseIds = watched.Select(x => x.ReleaseId.ToString(CultureInfo.InvariantCulture)).ToHashSet(StringComparer.OrdinalIgnoreCase);
        var javItemsByReleaseId = library.GetItemList(new InternalItemsQuery
        {
            Recursive = true,
            IncludeItemTypes = [BaseItemKind.Movie],
            IsVirtualItem = false
        }).Where(x => x.ProviderIds.TryGetValue("JAVBeacon", out var id) && desiredReleaseIds.Contains(id))
          .ToDictionary(x => x.ProviderIds["JAVBeacon"], StringComparer.OrdinalIgnoreCase);
        if (javItemsByReleaseId.Count == 0) return 0;

        var targetUsers = (trackedUserIds is { Length: > 0 }
                ? trackedUserIds.Select(raw => Guid.TryParse(raw, out var id) ? users.GetUserById(id) : null)
                : users.GetUsers())
            .Where(user => user is not null)
            .Select(user => user!)
            .ToArray();
        if (targetUsers.Length == 0) return 0;

        var marked = 0;
        foreach (var entry in watched)
        {
            if (!javItemsByReleaseId.TryGetValue(entry.ReleaseId.ToString(CultureInfo.InvariantCulture), out var item)) continue;
            foreach (var user in targetUsers)
            {
                var data = userData.GetUserData(user, item);
                if (data is null || data.Played) continue;
                data.Played = true;
                data.PlayCount = Math.Max(data.PlayCount, 1);
                data.LastPlayedDate = entry.WatchedAt?.UtcDateTime ?? data.LastPlayedDate ?? DateTime.UtcNow;
                userData.SaveUserData(user, item, data, UserDataSaveReason.Import, CancellationToken.None);
                marked++;
            }
        }
        if (marked > 0)
        {
            logger.LogInformation("Marked {Count} Jellyfin item/user pairs watched from StashApp play history", marked);
        }
        return marked;
    }
}
