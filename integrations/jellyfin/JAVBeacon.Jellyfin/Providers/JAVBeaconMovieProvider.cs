using System.Globalization;
using Jellyfin.Data.Enums;
using Jellyfin.Plugin.JAVBeacon.Models;
using MediaBrowser.Controller.Entities.Movies;
using MediaBrowser.Controller.Library;
using MediaBrowser.Controller.Providers;
using MediaBrowser.Model.Entities;
using MediaBrowser.Model.Providers;

namespace Jellyfin.Plugin.JAVBeacon.Providers;

public sealed class JAVBeaconMovieProvider(JAVBeaconClient client) : IRemoteMetadataProvider<Movie, MovieInfo>
{
    public string Name => "JAVBeacon";

    public async Task<MetadataResult<Movie>> GetMetadata(MovieInfo info, CancellationToken ct)
    {
        if (Plugin.Instance?.Configuration.EnableMetadata != true) return new();
        MetadataDto? dto = null;
        if (info.ProviderIds.TryGetValue("JAVBeacon", out var raw) && long.TryParse(raw, CultureInfo.InvariantCulture, out var id))
            dto = await client.Metadata(id, ct).ConfigureAwait(false);
        dto ??= (await client.Match(info.Path, info.Name, ct).ConfigureAwait(false))?.Release;
        if (dto is null) return new();
        var item = Map(dto);
        var result = new MetadataResult<Movie> { HasMetadata = true, Item = item, QueriedById = info.ProviderIds.ContainsKey("JAVBeacon") };
        foreach (var name in dto.Performers.Where(x => !string.IsNullOrWhiteSpace(x))) result.AddPerson(new() { Name = name, Type = PersonKind.Actor });
        foreach (var name in dto.Directors.Where(x => !string.IsNullOrWhiteSpace(x))) result.AddPerson(new() { Name = name, Type = PersonKind.Director });
        return result;
    }

    public async Task<IEnumerable<RemoteSearchResult>> GetSearchResults(MovieInfo info, CancellationToken ct)
    {
        if (Plugin.Instance?.Configuration.EnableMetadata != true) return [];
        var rows = await client.Search(info.Name ?? string.Empty, ct).ConfigureAwait(false);
        return rows.Select(x => new RemoteSearchResult
        {
            Name = string.IsNullOrWhiteSpace(x.Overview) ? x.Code : $"{x.Code} — {x.Overview}",
            ProductionYear = x.ProductionYear,
            PremiereDate = ParseDate(x.PremiereDate),
            ImageUrl = string.IsNullOrWhiteSpace(x.CoverPath) ? null : client.Absolute(x.CoverPath),
            SearchProviderName = Name,
            ProviderIds = new Dictionary<string, string>(x.ProviderIds)
        }).ToArray();
    }

    public Task<HttpResponseMessage> GetImageResponse(string url, CancellationToken ct) => client.GetImage(url, ct);

    private static Movie Map(MetadataDto x)
    {
        var item = new Movie
        {
            Name = x.Title,
            OriginalTitle = x.OriginalTitle,
            Overview = x.Overview,
            ProductionYear = x.ProductionYear,
            PremiereDate = ParseDate(x.PremiereDate),
            RunTimeTicks = x.RuntimeSeconds > 0 ? TimeSpan.FromSeconds(x.RuntimeSeconds).Ticks : null,
            ProviderIds = new Dictionary<string, string>(x.ProviderIds),
            Studios = string.IsNullOrWhiteSpace(x.Studio) ? [] : [x.Studio],
            Genres = x.Genres.Where(v => !string.IsNullOrWhiteSpace(v)).ToArray(),
            Tags = x.Tags.Where(v => !string.IsNullOrWhiteSpace(v)).ToArray()
        };
        return item;
    }

    private static DateTime? ParseDate(string? raw) => DateTime.TryParse(raw, CultureInfo.InvariantCulture, DateTimeStyles.AssumeUniversal, out var value) ? value : null;
}
