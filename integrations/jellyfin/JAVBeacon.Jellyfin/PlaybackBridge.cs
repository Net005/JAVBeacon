using System.Collections;
using System.Collections.Concurrent;
using System.Reflection;
using Jellyfin.Plugin.JAVBeacon.Models;
using MediaBrowser.Controller.Entities;
using MediaBrowser.Controller.Library;
using MediaBrowser.Controller.Session;
using Microsoft.Extensions.Hosting;
using Microsoft.Extensions.Logging;

namespace Jellyfin.Plugin.JAVBeacon;

public sealed class PlaybackBridge(ISessionManager sessions, JAVBeaconClient client, ILogger<PlaybackBridge> logger) : IHostedService
{
    private readonly ConcurrentDictionary<string, string> _active = new(StringComparer.OrdinalIgnoreCase);
    private readonly ConcurrentDictionary<string, DateTime> _lastProgress = new(StringComparer.OrdinalIgnoreCase);

    public Task StartAsync(CancellationToken ct)
    {
        sessions.PlaybackStart += OnStart;
        sessions.PlaybackProgress += OnProgress;
        sessions.PlaybackStopped += OnStop;
        return Task.CompletedTask;
    }

    public Task StopAsync(CancellationToken ct)
    {
        sessions.PlaybackStart -= OnStart;
        sessions.PlaybackProgress -= OnProgress;
        sessions.PlaybackStopped -= OnStop;
        return Task.CompletedTask;
    }

    private void OnStart(object? sender, PlaybackProgressEventArgs e) => _ = Forward(e, "start");
    private void OnProgress(object? sender, PlaybackProgressEventArgs e) => _ = Forward(e, "progress");
    private void OnStop(object? sender, PlaybackStopEventArgs e) => _ = Forward(e, "stop");

    private async Task Forward(object args, string eventName)
    {
        try
        {
            var config = Plugin.Instance?.Configuration;
            if (config?.EnablePlayback != true) return;
            var item = Read(args, "Item") as BaseItem;
            if (item is null) return;
            var session = Read(args, "Session");
            var userId = Text(Read(session, "UserId")) ?? FirstUserId(args) ?? string.Empty;
            if (config.TrackedUserIds is { Length: > 0 } && !config.TrackedUserIds.Contains(userId, StringComparer.OrdinalIgnoreCase)) return;
            var deviceId = Text(Read(session, "DeviceId")) ?? Text(Read(args, "DeviceId")) ?? "unknown";
            var jellyfinPlaySessionId = Text(Read(args, "PlaySessionId"));
            var key = string.IsNullOrWhiteSpace(jellyfinPlaySessionId) ? $"{deviceId}:{userId}:{item.Id:N}" : jellyfinPlaySessionId;
            if (eventName == "progress" && _lastProgress.TryGetValue(key, out var last) && DateTime.UtcNow - last < TimeSpan.FromSeconds(10)) return;
            _lastProgress[key] = DateTime.UtcNow;
            var playSessionId = !string.IsNullOrWhiteSpace(jellyfinPlaySessionId)
                ? jellyfinPlaySessionId
                : eventName == "start" ? Guid.NewGuid().ToString("N") : _active.GetOrAdd(key, _ => Guid.NewGuid().ToString("N"));
            if (eventName == "start") _active[key] = playSessionId;

            long releaseId;
            if (!item.ProviderIds.TryGetValue("JAVBeacon", out var raw) || !long.TryParse(raw, out releaseId))
            {
                var match = await client.Match(item.Path, item.Name, CancellationToken.None).ConfigureAwait(false);
                if (match?.Release is null) return;
                releaseId = match.Release.ReleaseId;
            }

            var ticks = Number(Read(args, "PlaybackPositionTicks"));
            var runtime = item.RunTimeTicks ?? Number(Read(Read(args, "MediaInfo"), "RunTimeTicks"));
            using var timeout = new CancellationTokenSource(TimeSpan.FromSeconds(15));
            await client.SendPlayback(new PlaybackDto
            {
                Event = eventName,
                SessionId = playSessionId,
                ReleaseId = releaseId,
                JellyfinItemId = item.Id.ToString("N"),
                JellyfinUserId = userId,
                PositionSeconds = ticks / TimeSpan.TicksPerSecond,
                RuntimeSeconds = runtime / TimeSpan.TicksPerSecond,
                IsPaused = Bool(Read(args, "IsPaused")) || Bool(Read(Read(session, "PlayState"), "IsPaused")),
                IsPlayed = Bool(Read(args, "PlayedToCompletion")),
                OccurredAt = DateTime.UtcNow
            }, timeout.Token).ConfigureAwait(false);
            if (eventName == "stop") { _active.TryRemove(key, out _); _lastProgress.TryRemove(key, out _); }
        }
        catch (Exception ex)
        {
            logger.LogWarning(ex, "Unable to checkpoint {EventName} playback with JAVBeacon", eventName);
        }
    }

    private static object? Read(object? value, string name) => value?.GetType().GetProperty(name, BindingFlags.Public | BindingFlags.Instance)?.GetValue(value);
    private static string? Text(object? value) => value?.ToString();
    private static long Number(object? value) => value is null ? 0 : Convert.ToInt64(value, System.Globalization.CultureInfo.InvariantCulture);
    private static bool Bool(object? value) => value is bool result && result;
    private static string? FirstUserId(object args)
    {
        if (Read(args, "Users") is not IEnumerable users) return null;
        foreach (var user in users) return Text(Read(user, "Id"));
        return null;
    }
}
