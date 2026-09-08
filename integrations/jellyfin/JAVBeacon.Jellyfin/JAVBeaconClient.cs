using System.Net.Http.Json;
using System.Text.Json;
using Jellyfin.Plugin.JAVBeacon.Configuration;
using Jellyfin.Plugin.JAVBeacon.Models;

namespace Jellyfin.Plugin.JAVBeacon;

public sealed class JAVBeaconClient(IHttpClientFactory clients)
{
    private static readonly JsonSerializerOptions Json = new(JsonSerializerDefaults.Web);

    private HttpClient Client()
    {
        var config = Plugin.Instance?.Configuration ?? new PluginConfiguration();
        var client = clients.CreateClient(nameof(JAVBeaconClient));
        client.BaseAddress = new Uri(config.JAVBeaconUrl.TrimEnd('/') + "/");
        client.Timeout = TimeSpan.FromSeconds(15);
        client.DefaultRequestHeaders.Authorization = new("Bearer", config.ApiKey);
        return client;
    }

    public async Task<MatchDto?> Match(string? path, string? query, CancellationToken ct)
    {
        using var client = Client();
        using var response = await client.PostAsJsonAsync("api/v1/media/match", new { path, query }, Json, ct).ConfigureAwait(false);
        response.EnsureSuccessStatusCode();
        return await response.Content.ReadFromJsonAsync<MatchDto>(Json, ct).ConfigureAwait(false);
    }

    public async Task<MetadataDto?> Metadata(long id, CancellationToken ct)
    {
        using var client = Client();
        return await client.GetFromJsonAsync<MetadataDto>($"api/v1/integrations/jellyfin/releases/{id}", Json, ct).ConfigureAwait(false);
    }

    public async Task<MetadataDto[]> Search(string query, CancellationToken ct)
    {
        using var client = Client();
        var result = await client.GetFromJsonAsync<SearchDto>($"api/v1/integrations/jellyfin/search?q={Uri.EscapeDataString(query)}", Json, ct).ConfigureAwait(false);
        return result?.Items ?? [];
    }

    public async Task<LibrarySyncDto?> LibrarySync(CancellationToken ct)
    {
        using var client = Client();
        return await client.GetFromJsonAsync<LibrarySyncDto>("api/v1/integrations/jellyfin/library-sync", Json, ct).ConfigureAwait(false);
    }

    public async Task SendPlayback(PlaybackDto value, CancellationToken ct)
    {
        using var client = Client();
        using var response = await client.PostAsJsonAsync("api/v1/integrations/jellyfin/playback", value, Json, ct).ConfigureAwait(false);
        response.EnsureSuccessStatusCode();
    }

    public async Task<ActivityDto?> Activity(long id, CancellationToken ct)
    {
        using var client = Client();
        return await client.GetFromJsonAsync<ActivityDto>($"api/v1/integrations/jellyfin/releases/{id}/activity", Json, ct).ConfigureAwait(false);
    }

    public async Task<ActivityDto?> AddO(long id, CancellationToken ct)
    {
        using var client = Client();
        using var response = await client.PostAsJsonAsync($"api/v1/integrations/jellyfin/releases/{id}/o", new { occurred_at = DateTime.UtcNow }, Json, ct).ConfigureAwait(false);
        response.EnsureSuccessStatusCode();
        return await response.Content.ReadFromJsonAsync<ActivityDto>(Json, ct).ConfigureAwait(false);
    }

    private static string RelativePath(string path)
    {
        return path.TrimStart('/');
    }

    private static string NormalizeImageUrl(string raw)
    {
        var value = raw.Trim();
        if (value.StartsWith("file://", StringComparison.OrdinalIgnoreCase))
        {
            value = value["file://".Length..];
        }

        return RelativePath(value);
    }

    private static string ToAbsoluteImageUrl(string? path, string fallbackBase)
    {
        var normalized = NormalizeImageUrl(path ?? string.Empty);
        if (string.IsNullOrWhiteSpace(normalized))
        {
            return normalized;
        }

        if (Uri.TryCreate(normalized, UriKind.Absolute, out var absolute) && absolute.Scheme != Uri.UriSchemeFile)
        {
            return absolute.ToString();
        }

        return new Uri(new Uri(fallbackBase), RelativePath(normalized)).ToString();
    }

    public async Task<HttpResponseMessage> GetImage(string url, CancellationToken ct)
    {
        var config = Plugin.Instance?.Configuration ?? new PluginConfiguration();
        var configuredBase = new Uri(config.JAVBeaconUrl.TrimEnd('/') + "/");
        var normalized = NormalizeImageUrl(url);
        var target = Uri.TryCreate(normalized, UriKind.Absolute, out var absolute) && absolute.Scheme != Uri.UriSchemeFile
            ? absolute
            : new Uri(configuredBase, RelativePath(normalized));
        // Never forward the JAVBeacon bearer token to a third-party backdrop
        // host. Only same-origin image requests use the authenticated client.
        var sameOrigin = Uri.Compare(target, configuredBase, UriComponents.SchemeAndServer, UriFormat.Unescaped, StringComparison.OrdinalIgnoreCase) == 0;
        var client = sameOrigin ? Client() : clients.CreateClient();
        var response = await client.GetAsync(target, HttpCompletionOption.ResponseHeadersRead, ct).ConfigureAwait(false);
        response.Content.Headers.ContentType ??= new("image/jpeg");
        return response;
    }

    public string Absolute(string path)
    {
        var config = Plugin.Instance?.Configuration ?? new PluginConfiguration();
        return ToAbsoluteImageUrl(path, config.JAVBeaconUrl.TrimEnd('/') + "/");
    }
}
