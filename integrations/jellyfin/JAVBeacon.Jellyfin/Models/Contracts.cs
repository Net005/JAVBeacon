using System.Text.Json.Serialization;

namespace Jellyfin.Plugin.JAVBeacon.Models;

public sealed record MetadataDto
{
    [JsonPropertyName("release_id")] public long ReleaseId { get; init; }
    [JsonPropertyName("stash_scene_id")] public string? StashSceneId { get; init; }
    [JsonPropertyName("code")] public string Code { get; init; } = string.Empty;
    [JsonPropertyName("title")] public string Title { get; init; } = string.Empty;
    [JsonPropertyName("original_title")] public string? OriginalTitle { get; init; }
    [JsonPropertyName("overview")] public string? Overview { get; init; }
    [JsonPropertyName("premiere_date")] public string? PremiereDate { get; init; }
    [JsonPropertyName("production_year")] public int? ProductionYear { get; init; }
    [JsonPropertyName("studio")] public string? Studio { get; init; }
    [JsonPropertyName("performers")] public string[] Performers { get; init; } = [];
    [JsonPropertyName("directors")] public string[] Directors { get; init; } = [];
    [JsonPropertyName("genres")] public string[] Genres { get; init; } = [];
    [JsonPropertyName("tags")] public string[] Tags { get; init; } = [];
    [JsonPropertyName("runtime_seconds")] public long RuntimeSeconds { get; init; }
    [JsonPropertyName("cover_path")] public string? CoverPath { get; init; }
    [JsonPropertyName("cover_backdrop_path")] public string? CoverBackdropPath { get; init; }
    [JsonPropertyName("backdrop_urls")] public string[] BackdropUrls { get; init; } = [];
    // Populated only when JAVBeacon has no cover image of its own for this
    // release (see JAVBeaconClient/enrichFromStash on the server) - a
    // JAVBeacon-proxied StashApp scene screenshot, offered as an additional
    // image candidate rather than a replacement for CoverPath/BackdropUrls.
    [JsonPropertyName("stash_screenshot_url")] public string? StashScreenshotUrl { get; init; }
    [JsonPropertyName("provider_ids")] public Dictionary<string, string> ProviderIds { get; init; } = [];
}

public sealed record MatchDto([property: JsonPropertyName("matched")] bool Matched, [property: JsonPropertyName("release")] MetadataDto? Release);
public sealed record SearchDto([property: JsonPropertyName("items")] MetadataDto[] Items);

public sealed record LibrarySyncItemDto
{
    [JsonPropertyName("release_id")] public long ReleaseId { get; init; }
    [JsonPropertyName("stash_scene_id")] public string StashSceneId { get; init; } = string.Empty;
    [JsonPropertyName("path")] public string? Path { get; init; }
    [JsonPropertyName("watchlisted_at")] public DateTimeOffset? WatchlistedAt { get; init; }
    // WatchedAt is only populated on LibrarySyncDto.Watched entries: the most
    // recent time StashApp recorded this scene as played.
    [JsonPropertyName("watched_at")] public DateTimeOffset? WatchedAt { get; init; }
}

public sealed record LibrarySyncDto
{
    [JsonPropertyName("revision")] public string Revision { get; init; } = string.Empty;
    [JsonPropertyName("watchlist")] public LibrarySyncItemDto[] Watchlist { get; init; } = [];
    // Watched lists every local, StashApp-linked release StashApp reports as
    // played at least once - independent of Watchlist. JAVBeacon always
    // includes it; the plugin only acts on it when SyncWatchedFromStash is
    // enabled.
    [JsonPropertyName("watched")] public LibrarySyncItemDto[] Watched { get; init; } = [];
    // FilterPresets mirrors every saved filter set from the Release Library,
    // each already resolved server-side to its exact, sorted membership
    // using the same filter+sort engine the web UI itself uses. One Jellyfin
    // collection is created/kept in sync per entry.
    [JsonPropertyName("filter_presets")] public FilterPresetCollectionDto[] FilterPresets { get; init; } = [];
}

public sealed record FilterPresetCollectionDto
{
    [JsonPropertyName("id")] public long Id { get; init; }
    [JsonPropertyName("name")] public string Name { get; init; } = string.Empty;
    [JsonPropertyName("release_ids")] public long[] ReleaseIds { get; init; } = [];
}

public sealed record PlaybackDto
{
    [JsonPropertyName("event")] public string Event { get; init; } = string.Empty;
    [JsonPropertyName("session_id")] public string SessionId { get; init; } = string.Empty;
    [JsonPropertyName("release_id")] public long ReleaseId { get; init; }
    [JsonPropertyName("jellyfin_item_id")] public string JellyfinItemId { get; init; } = string.Empty;
    [JsonPropertyName("jellyfin_user_id")] public string JellyfinUserId { get; init; } = string.Empty;
    [JsonPropertyName("position_seconds")] public double PositionSeconds { get; init; }
    [JsonPropertyName("runtime_seconds")] public double RuntimeSeconds { get; init; }
    [JsonPropertyName("is_paused")] public bool IsPaused { get; init; }
    [JsonPropertyName("is_played")] public bool IsPlayed { get; init; }
    [JsonPropertyName("occurred_at")] public DateTime OccurredAt { get; init; } = DateTime.UtcNow;
}

public sealed record ActivityDto
{
    [JsonPropertyName("o_count")] public int OCount { get; init; }
    [JsonPropertyName("play_count")] public int PlayCount { get; init; }
    [JsonPropertyName("play_duration_seconds")] public double PlayDurationSeconds { get; init; }
    [JsonPropertyName("resume_time_seconds")] public double ResumeTimeSeconds { get; init; }
    [JsonPropertyName("last_played_at")] public string? LastPlayedAt { get; init; }
}
