using MediaBrowser.Controller.Entities;
using MediaBrowser.Controller.Entities.Movies;
using MediaBrowser.Controller.Providers;
using MediaBrowser.Model.Entities;
using MediaBrowser.Model.Providers;

namespace Jellyfin.Plugin.JAVBeacon.Providers;

public sealed class JAVBeaconExternalId : IExternalId
{
    public string ProviderName => "JAVBeacon Release";
    public string Key => "JAVBeacon";
    public ExternalIdMediaType? Type => ExternalIdMediaType.Movie;
    public bool Supports(IHasProviderIds item) => item is Movie;
}

public sealed class JAVBeaconExternalUrlProvider : IExternalUrlProvider
{
    public string Name => "JAVBeacon";

    public IEnumerable<string> GetExternalUrls(BaseItem item)
    {
        if (item is not Movie || !item.ProviderIds.TryGetValue("JAVBeacon", out var releaseId) || string.IsNullOrWhiteSpace(releaseId))
            return [];
        var baseUrl = Plugin.Instance?.Configuration.JAVBeaconUrl?.TrimEnd('/');
        return string.IsNullOrWhiteSpace(baseUrl) ? [] : [$"{baseUrl}/release/{Uri.EscapeDataString(releaseId)}"];
    }
}
