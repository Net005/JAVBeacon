using System.Globalization;
using Jellyfin.Plugin.JAVBeacon.Models;
using MediaBrowser.Controller.Entities;
using MediaBrowser.Controller.Providers;
using MediaBrowser.Model.Providers;

namespace Jellyfin.Plugin.JAVBeacon.Providers;

/// <summary>
/// Fills in a Jellyfin Person page's bio (birthdate, country, and - folded
/// into Overview, since Person has no dedicated fields for them - gender,
/// ethnicity, hair/eye colour, height, measurements, fake tits, career
/// length, and linked Stash IDs) from StashApp, the same way
/// <see cref="JAVBeaconMovieProvider"/> already gap-fills a movie's own
/// metadata. Only runs for a person JAVBeaconMovieProvider already tagged
/// with a "JAVBeacon" provider id (the StashApp performer id) - Jellyfin
/// invokes an IRemoteMetadataProvider&lt;Person, PersonLookupInfo&gt; only
/// once it has a provider id to look the person up by, so a cast member with
/// no matching StashApp performer never triggers a lookup at all.
/// </summary>
public sealed class JAVBeaconPersonProvider(JAVBeaconClient client) : IRemoteMetadataProvider<Person, PersonLookupInfo>
{
    public string Name => "JAVBeacon";

    public async Task<MetadataResult<Person>> GetMetadata(PersonLookupInfo info, CancellationToken ct)
    {
        if (Plugin.Instance?.Configuration.EnableMetadata != true) return new();
        if (!info.ProviderIds.TryGetValue("JAVBeacon", out var performerId) || string.IsNullOrWhiteSpace(performerId))
            return new();
        PerformerBioDto? dto;
        try
        {
            dto = await client.PerformerBio(performerId, ct).ConfigureAwait(false);
        }
        catch (HttpRequestException)
        {
            // StashApp not linked, the performer was deleted there, or a
            // transient network error - a missing bio is not worth failing
            // the whole metadata refresh over.
            return new();
        }
        if (dto is null) return new();
        return new MetadataResult<Person> { HasMetadata = true, Item = Map(dto), QueriedById = true };
    }

    public Task<IEnumerable<RemoteSearchResult>> GetSearchResults(PersonLookupInfo info, CancellationToken ct) =>
        Task.FromResult(Enumerable.Empty<RemoteSearchResult>());

    public Task<HttpResponseMessage> GetImageResponse(string url, CancellationToken ct) => client.GetImage(url, ct);

    private static Person Map(PerformerBioDto x)
    {
        var person = new Person
        {
            Name = x.Name,
            PremiereDate = ParseDate(x.Birthdate),
            EndDate = ParseDate(x.DeathDate),
            ProductionLocations = string.IsNullOrWhiteSpace(x.Country) ? [] : [x.Country],
            Overview = BuildOverview(x)
        };
        if (!string.IsNullOrWhiteSpace(x.Id)) person.ProviderIds["JAVBeacon"] = x.Id;
        return person;
    }

    // Jellyfin's Person entity has no dedicated fields for most of what a
    // Stash performer page shows - gender, ethnicity, hair/eye colour,
    // height, measurements, fake tits, career length, and linked Stash IDs
    // all have no equivalent property to set. Folding them into Overview as
    // plain text is the only way this data reaches the Person page at all,
    // rather than being silently dropped.
    private static string? BuildOverview(PerformerBioDto x)
    {
        var facts = new List<string>();
        if (!string.IsNullOrWhiteSpace(x.Gender)) facts.Add($"Gender: {x.Gender}");
        if (!string.IsNullOrWhiteSpace(x.Ethnicity)) facts.Add($"Ethnicity: {x.Ethnicity}");
        if (!string.IsNullOrWhiteSpace(x.HairColor)) facts.Add($"Hair colour: {x.HairColor}");
        if (!string.IsNullOrWhiteSpace(x.EyeColor)) facts.Add($"Eye colour: {x.EyeColor}");
        if (x.HeightCm > 0) facts.Add($"Height: {x.HeightCm.ToString(CultureInfo.InvariantCulture)} cm");
        if (x.WeightKg > 0) facts.Add($"Weight: {x.WeightKg.ToString(CultureInfo.InvariantCulture)} kg");
        if (!string.IsNullOrWhiteSpace(x.Measurements)) facts.Add($"Measurements: {x.Measurements}");
        if (!string.IsNullOrWhiteSpace(x.FakeTits)) facts.Add($"Fake tits: {x.FakeTits}");
        if (!string.IsNullOrWhiteSpace(x.Tattoos)) facts.Add($"Tattoos: {x.Tattoos}");
        if (!string.IsNullOrWhiteSpace(x.Piercings)) facts.Add($"Piercings: {x.Piercings}");
        if (!string.IsNullOrWhiteSpace(x.CareerLength)) facts.Add($"Career length: {x.CareerLength}");
        foreach (var stashId in x.StashIds)
        {
            if (!string.IsNullOrWhiteSpace(stashId.StashId))
                facts.Add($"Stash ID ({HostLabel(stashId.Endpoint)}): {stashId.StashId}");
        }
        var lines = new List<string>();
        if (facts.Count > 0) lines.Add(string.Join(" · ", facts));
        if (!string.IsNullOrWhiteSpace(x.Details)) lines.Add(x.Details.Trim());
        return lines.Count > 0 ? string.Join("\n\n", lines) : null;
    }

    private static string HostLabel(string endpoint)
    {
        if (Uri.TryCreate(endpoint, UriKind.Absolute, out var uri) && !string.IsNullOrWhiteSpace(uri.Host))
            return uri.Host;
        return string.IsNullOrWhiteSpace(endpoint) ? "Stash" : endpoint;
    }

    private static DateTime? ParseDate(string? raw) =>
        !string.IsNullOrWhiteSpace(raw) && DateTime.TryParse(raw, CultureInfo.InvariantCulture, DateTimeStyles.AssumeUniversal, out var value)
            ? value
            : null;
}
